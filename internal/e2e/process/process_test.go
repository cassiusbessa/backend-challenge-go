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
	base := startProcessWith(t, queueURL, "postgres://junglegaming:junglegaming@127.0.0.1:1/junglegaming?sslmode=disable")
	live := statusCode(ctx, t, base+"/health/live")
	if live != http.StatusOK {
		t.Fatalf("live with an unreachable database = %d, want 200", live)
	}
	ready := statusCode(ctx, t, base+"/health/ready")
	if ready != http.StatusServiceUnavailable {
		t.Fatalf("ready with the database unreachable = %d, want 503", ready)
	}
}

// The wager routes of this delivery are served. Without a credential they answer
// 401, which is the guard refusing the request and not a route that is not there,
// and the reconciliation route still answers 404 because it is not delivered.
func TestWagerRoutesAreServedAndReconciliationIsNot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := startProcess(t, createQueue(ctx, t))
	submit := statusOf(ctx, t, http.MethodPost, base+"/wagering/transactions")
	if submit != http.StatusUnauthorized {
		t.Fatalf("POST /wagering/transactions = %d, want 401 from the guard", submit)
	}
	read := statusOf(ctx, t, http.MethodGet, base+"/wagering/transactions/33333333-3333-4333-8333-333333333333")
	if read != http.StatusUnauthorized {
		t.Fatalf("GET /wagering/transactions/{transactionId} = %d, want 401 from the guard", read)
	}
	if got := statusCode(ctx, t, base+"/reconciliation"); got != http.StatusNotFound {
		t.Fatalf("GET /reconciliation = %d, want 404 while it is not delivered", got)
	}
}

func startProcess(t *testing.T, queueURL string) string {
	t.Helper()
	return startProcessWith(t, queueURL, envOr("DATABASE_URL", "postgres://junglegaming:junglegaming@localhost:5432/junglegaming?sslmode=disable"))
}

func startProcessWith(t *testing.T, queueURL, databaseURL string) string {
	t.Helper()
	cfg, err := config.Load(func(key string) string {
		env := integrationEnv(queueURL)
		env["DATABASE_URL"] = databaseURL
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
	client, err := probe.NewClient(ctx, envOr("SQS_ENDPOINT", "http://localhost:4566"))
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
	return strings.TrimRight(envOr("SQS_ENDPOINT", "http://localhost:4566"), "/") + "/000000000000/missing-wager.fifo"
}

func integrationEnv(queueURL string) map[string]string {
	return map[string]string{
		"HTTP_ADDR":                   "127.0.0.1:0",
		"DATABASE_URL":                envOr("DATABASE_URL", "postgres://junglegaming:junglegaming@localhost:5432/junglegaming?sslmode=disable"),
		"SQS_ENDPOINT":                envOr("SQS_ENDPOINT", "http://localhost:4566"),
		"SQS_QUEUE_URL":               queueURL,
		"OTEL_EXPORTER_OTLP_ENDPOINT": envOr("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		"IDP_ISSUER":                  envOr("IDP_ISSUER", "http://localhost:8080/realms/junglegaming"),
		"CLIENTS_PATH":                envOr("CLIENTS_PATH", "../../../deploy/local/clients.yaml"),
		"PPROF_ADDR":                  envOr("PPROF_ADDR", "127.0.0.1:0"),
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
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
