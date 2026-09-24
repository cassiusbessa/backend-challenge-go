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

// A suíte já cai no LocalStack quando banco e endpoint não vêm do ambiente.
// A credencial segue a mesma regra, para o portão local não depender de um
// shell preparado. Produção continua sem credencial no código.
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
		t.Fatalf("ready = %d, want 503", ready)
	}
}

func startProcess(t *testing.T, queueURL string) string {
	t.Helper()
	cfg, err := config.Load(func(key string) string {
		return integrationEnv(queueURL)[key]
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
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
