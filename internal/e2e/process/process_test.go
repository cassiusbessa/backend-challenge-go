//go:build integration

package process

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/platform/app"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/platform/probe"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// TestMain falls back to LocalStack when the database and the endpoint do not
// come from the environment.
//
// The credential follows the same rule so that the local gate does not depend
// on a prepared shell. Production still carries no credential in the code.
func TestMain(m *testing.M) {
	for key, value := range localAWS {
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				panic(err)
			}
		}
	}
	os.Exit(m.Run())
}

var localAWS = map[string]string{
	"AWS_ACCESS_KEY_ID":     "test",
	"AWS_SECRET_ACCESS_KEY": "test",
	"AWS_REGION":            "us-east-1",
}

func TestLiveAndReadyWithRealQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queueURL := createQueue(ctx, t)
	base := startProcess(t, queueURL)
	live := statusCode(ctx, t, base+"/health/live")
	if live != http.StatusOK {
		t.Fatalf("live = %d, want 200", live)
	}
	ready := statusCode(ctx, t, base+"/health/ready")
	if ready != http.StatusOK {
		t.Fatalf("ready = %d, want 200", ready)
	}
}

func TestLiveStaysUpWhenQueueIsUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := startProcess(t, unknownQueue())
	live := statusCode(ctx, t, base+"/health/live")
	if live != http.StatusOK {
		t.Fatalf("live with an unknown queue = %d, want 200", live)
	}
	ready := statusCode(ctx, t, base+"/health/ready")
	if ready != http.StatusServiceUnavailable {
		t.Fatalf("ready with an unknown queue = %d, want 503", ready)
	}
}

// The readiness probe answers through the pool the process shares. An
// unreachable database must not take the process down with it.
func TestReadyFallsWhenTheDatabaseIsUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queueURL := createQueue(ctx, t)
	base := startProcessWith(t, queueURL, unreachableDatabaseURL)
	live := statusCode(ctx, t, base+"/health/live")
	if live != http.StatusOK {
		t.Fatalf("live with an unreachable database = %d, want 200", live)
	}
	ready := statusCode(ctx, t, base+"/health/ready")
	if ready != http.StatusServiceUnavailable {
		t.Fatalf("ready with the database unreachable = %d, want 503", ready)
	}
}

// The wager routes and the two wallet reads are served. Without a credential
// they answer 401, which is the guard refusing the request and not a route that
// is not there.
func TestWagerAndWalletReadRoutesAreServed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := startProcess(t, createQueue(ctx, t))
	const wallet = "/wallets/11111111-1111-4111-8111-111111111111"
	routes := []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/wagering/transactions"},
		{method: http.MethodGet, path: "/wagering/transactions/33333333-3333-4333-8333-333333333333"},
		{method: http.MethodGet, path: "/providers/provider-a/wagering/transactions/external-1"},
		{method: http.MethodGet, path: wallet + "/ledger"},
		{method: http.MethodPost, path: wallet + "/reconciliation"},
	}
	for _, route := range routes {
		if got := statusOf(ctx, t, route.method, base+route.path); got != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d, want 401 from the guard", route.method, route.path, got)
		}
	}
}

func startProcess(t *testing.T, queueURL string) string {
	t.Helper()
	return startProcessWith(t, queueURL, suiteenv.DatabaseURL())
}

// unreachableDatabaseURL names a port nothing listens on, so that readiness is
// asked about a database it cannot reach rather than one that is merely empty.
const unreachableDatabaseURL = "postgres://junglegaming:junglegaming@127.0.0.1:1/junglegaming_test?sslmode=disable"

func startProcessWith(t *testing.T, queueURL, databaseURL string) string {
	t.Helper()
	// The map is built once, outside the lookup: config.Load asks for one key at a
	// time, and building it inside re-read the whole environment for every key.
	env := integrationEnv(queueURL, databaseURL)
	cfg, err := config.Load(func(key string) string {
		return env[key]
	})
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	got := make(chan *httpapi.Server, 1)
	application := app.New(cfg, fx.Invoke(func(srv *httpapi.Server) { got <- srv }))
	startCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := application.Start(startCtx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := application.Stop(stopCtx); err != nil {
			t.Fatalf("stop: %v", err)
		}
	})
	return "http://" + (<-got).Addr()
}

func createQueue(ctx context.Context, t *testing.T) string {
	t.Helper()
	client, err := probe.NewClient(ctx, suiteenv.Or("SQS_ENDPOINT", "http://localhost:4566"))
	if err != nil {
		t.Fatalf("sqs: %v", err)
	}
	name := "wager-transactions.fifo"
	found, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err == nil {
		return aws.ToString(found.QueueUrl)
	}
	created, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(name),
		Attributes: map[string]string{
			"FifoQueue": "true",
		},
	})
	if err != nil {
		t.Fatalf("create queue: %v", err)
	}
	return aws.ToString(created.QueueUrl)
}

func unknownQueue() string {
	return strings.TrimRight(suiteenv.Or("SQS_ENDPOINT", "http://localhost:4566"), "/") + "/000000000000/missing-wager.fifo"
}

func integrationEnv(queueURL, databaseURL string) map[string]string {
	return map[string]string{
		"HTTP_ADDR":                   "127.0.0.1:0",
		"DATABASE_URL":                databaseURL,
		"SQS_ENDPOINT":                suiteenv.Or("SQS_ENDPOINT", "http://localhost:4566"),
		"SNS_ENDPOINT":                suiteenv.Or("SNS_ENDPOINT", "http://localhost:4566"),
		"SNS_TOPIC_ARN":               suiteenv.Or("SNS_TOPIC_ARN", "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo"),
		"SQS_QUEUE_URL":               queueURL,
		"SQS_DLQ_URL":                 queueURL,
		"OTEL_EXPORTER_OTLP_ENDPOINT": suiteenv.Or("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		"IDP_ISSUER":                  suiteenv.Or("IDP_ISSUER", "http://localhost:8080/realms/junglegaming"),
		"CLIENTS_PATH":                suiteenv.Or("CLIENTS_PATH", "../../../deploy/local/clients.yaml"),
		"QUEUE_SENDERS_PATH":          suiteenv.Or("QUEUE_SENDERS_PATH", "../../../deploy/local/queue-senders.yaml"),
		"PPROF_ADDR":                  suiteenv.Or("PPROF_ADDR", "127.0.0.1:0"),
	}
}

func statusCode(ctx context.Context, t *testing.T, rawURL string) int {
	t.Helper()
	return statusOf(ctx, t, http.MethodGet, rawURL)
}

func statusOf(ctx context.Context, t *testing.T, method, rawURL string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s: %v", rawURL, err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}
