package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

func TestEmptyDatabaseURLDoesNotListen(t *testing.T) {
	t.Parallel()
	called := false
	err := LoadAndRun(envWithoutDatabase, nil, func(config.Config, <-chan os.Signal) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("error = nil, want missing DATABASE_URL")
	}
	var missing config.MissingError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want MissingError", err)
	}
	if missing.Key != "DATABASE_URL" {
		t.Fatalf("key = %s, want DATABASE_URL", missing.Key)
	}
	if called {
		t.Fatal("a subida escutou sem URL do banco")
	}
}

func TestSIGTERMStopsNewConnectionsAndExitsSuccessfully(t *testing.T) {
	cfg, err := config.Load(testEnv)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	gate := &hold{entered: make(chan struct{}), release: make(chan struct{})}
	gotSrv := make(chan *httpapi.Server, 1)
	gotPipe := make(chan *telemetry.Pipeline, 1)
	application := New(cfg,
		fx.Replace(fx.Annotate(gate, fx.As(new(httpapi.PostgresChecker)))),
		fx.Replace(fx.Annotate(okCheck{}, fx.As(new(httpapi.QueueChecker)))),
		fx.Invoke(func(srv *httpapi.Server) { gotSrv <- srv }),
		fx.Invoke(func(pipe *telemetry.Pipeline) { gotPipe <- pipe }),
	)
	sigs := make(chan os.Signal, 1)
	errCh := make(chan error, 1)
	go func() { errCh <- Run(application, sigs, cfg.ShutdownTimeout) }()
	srv := recvServer(t, gotSrv)
	waitCh(t, srv.Listening())
	base := "http://" + srv.Addr()
	go func() { _ = readyStatus(base + "/health/ready") }()
	waitCh(t, gate.entered)
	sigs <- syscall.SIGTERM
	waitRefused(t, base+"/health/live")
	pipe := <-gotPipe
	select {
	case <-pipe.Stopped():
		t.Fatal("telemetria descarregou antes do pedido em curso")
	default:
	}
	close(gate.release)
	select {
	case err = <-errCh:
	case <-time.After(12 * time.Second):
		t.Fatal("processo não encerrou dentro do prazo")
	}
	if err != nil {
		t.Fatalf("saída = %v, want nil", err)
	}
}

func TestDefaultGoTestSkipsIntegrationInTheWholeModule(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "go", "test", "-count=0", "-list", ".", "./...")
	cmd.Dir = moduleRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test -list: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "TestLiveAndReadyWithRealQueue") {
		t.Fatalf("teste de integração entrou no go test sem a tag:\n%s", out)
	}
}

const moduleRoot = "../../.."

type hold struct {
	entered chan struct{}
	release chan struct{}
}

func (h *hold) Check(ctx context.Context) error {
	close(h.entered)
	select {
	case <-h.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type okCheck struct{}

func (okCheck) Check(context.Context) error { return nil }

func envWithoutDatabase(key string) string {
	if key == "DATABASE_URL" {
		return ""
	}
	return testEnv(key)
}

func testEnv(key string) string {
	values := map[string]string{
		"HTTP_ADDR":                   "127.0.0.1:0",
		"DATABASE_URL":                "postgres://junglegaming:junglegaming@127.0.0.1:1/junglegaming?sslmode=disable",
		"SQS_ENDPOINT":                "http://127.0.0.1:1",
		"SQS_QUEUE_URL":               "http://127.0.0.1:1/000000000000/wager-transactions.fifo",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "127.0.0.1:1",
		"SHUTDOWN_TIMEOUT":            "8s",
	}
	return values[key]
}

func recvServer(t *testing.T, ch <-chan *httpapi.Server) *httpapi.Server {
	t.Helper()
	select {
	case srv := <-ch:
		return srv
	case <-time.After(3 * time.Second):
		t.Fatal("servidor não foi construído")
	}
	return nil
}

func waitCh(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("espera estourou")
	}
}

func readyStatus(rawURL string) int {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		return 0
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

func waitRefused(t *testing.T, rawURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for ctx.Err() == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		_ = res.Body.Close()
	}
	t.Fatal("a porta continuou aceitando conexão")
}
