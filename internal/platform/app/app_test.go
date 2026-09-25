package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/platform/authz"
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
		t.Fatalf("error = %v, want MissingError on DATABASE_URL", err)
	}
	var missing config.MissingError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want MissingError", err)
	}
	if missing.Key != "DATABASE_URL" {
		t.Fatalf("key = %s, want DATABASE_URL", missing.Key)
	}
	if called {
		t.Fatalf("boot invoked = %v, want false without a database URL", called)
	}
}

// A process that cannot name its issuer, or cannot read the versioned map, does
// not know how to authorize anybody, so it must not reach the listener.
func TestLoadAndRun_refusesToListenWithoutTheIdentityProvider(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"IDP_ISSUER", "CLIENTS_PATH"} {
		t.Run("missing "+key+" aborts startup", func(t *testing.T) {
			booted := false
			err := LoadAndRun(envWithout(key), nil, func(config.Config, <-chan os.Signal) error {
				booted = true
				return nil
			})
			var missing config.MissingError
			if !errors.As(err, &missing) {
				t.Fatalf("error = %v, want MissingError on %s", err, key)
			}
			if missing.Key != key {
				t.Fatalf("key = %s, want %s", missing.Key, key)
			}
			if booted {
				t.Fatalf("boot invoked = %v, want false without %s", booted, key)
			}
		})
	}
}

func TestNew_refusesToStartWithAnUnreadableClientMap(t *testing.T) {
	t.Parallel()
	cfg, err := config.Load(envWithClients(filepath.Join(t.TempDir(), "absent.yaml")))
	if err != nil {
		t.Fatalf("load config for the absent client map = %v, want nil", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	err = New(cfg).Start(ctx)
	if !errors.Is(err, authz.ErrUnreadableClientMap) {
		t.Fatalf("start = %v, want %v", err, authz.ErrUnreadableClientMap)
	}
}

func TestSIGTERMStopsNewConnectionsAndExitsSuccessfully(t *testing.T) {
	cfg, err := config.Load(testEnv)
	if err != nil {
		t.Fatalf("load config of the test environment = %v, want nil", err)
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
	// A start that succeeded has to wait for the signal. Run returning here would
	// leave the process up with nobody listening for the shutdown, and the
	// listener and the telemetry buffer would outlive the call that owns them.
	select {
	case early := <-errCh:
		t.Fatalf("Run returned %v before the signal, want it waiting for one", early)
	default:
	}
	sigs <- syscall.SIGTERM
	waitRefused(t, base+"/health/live")
	pipe := recvPipeline(t, gotPipe)
	assertNotFlushed(t, pipe)
	close(gate.release)
	err = waitExit(t, errCh)
	if err != nil {
		t.Fatalf("exit = %v, want nil", err)
	}
	// The lifecycle flushes the buffer on the way out, so a process that exited
	// cleanly leaves the pipeline stopped.
	waitCh(t, pipe.Stopped())
}

func recvPipeline(t *testing.T, ch <-chan *telemetry.Pipeline) *telemetry.Pipeline {
	t.Helper()
	select {
	case pipe := <-ch:
		return pipe
	case <-time.After(stepWait):
		t.Fatalf("the telemetry pipeline was not built within %s", stepWait)
	}
	return nil
}

func assertNotFlushed(t *testing.T, pipe *telemetry.Pipeline) {
	t.Helper()
	flushed := false
	select {
	case <-pipe.Stopped():
		flushed = true
	default:
	}
	if flushed {
		t.Fatalf("telemetry flushed = %v, want false while a request is in flight", flushed)
	}
}

func waitExit(t *testing.T, errCh <-chan error) error {
	t.Helper()
	select {
	case err := <-errCh:
		return err
	case <-time.After(exitWait):
		t.Fatalf("process did not exit within %s", exitWait)
	}
	return nil
}

func TestDefaultGoTestSkipsIntegrationInTheWholeModule(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "go", "test", "-count=0", "-list", ".", "./...")
	cmd.Dir = moduleRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test -list: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "TestLiveAndReadyWithRealQueue") {
		t.Fatalf("the integration suite entered go test without the tag:\n%s", out)
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
	return envWithout("DATABASE_URL")(key)
}

func envWithout(blank string) func(string) string {
	return func(key string) string {
		if key == blank {
			return ""
		}
		return testEnv(key)
	}
}

func envWithClients(path string) func(string) string {
	return func(key string) string {
		if key == "CLIENTS_PATH" {
			return path
		}
		return testEnv(key)
	}
}

func testEnv(key string) string {
	values := map[string]string{
		"HTTP_ADDR":                   "127.0.0.1:0",
		"DATABASE_URL":                "postgres://junglegaming:junglegaming@127.0.0.1:1/junglegaming?sslmode=disable",
		"SQS_ENDPOINT":                "http://127.0.0.1:1",
		"SQS_QUEUE_URL":               "http://127.0.0.1:1/000000000000/wager-transactions.fifo",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "127.0.0.1:1",
		"IDP_ISSUER":                  "http://127.0.0.1:1/realms/junglegaming",
		"CLIENTS_PATH":                clientsPath,
		"SHUTDOWN_TIMEOUT":            "8s",
		"PPROF_ADDR":                  "127.0.0.1:0",
	}
	return values[key]
}

// clientsPath is the versioned map the challenge ships. The graph loads it on
// construction, so a run without it would not reach the lifecycle at all.
const clientsPath = moduleRoot + "/deploy/local/clients.yaml"

const (
	exitWait = 12 * time.Second
	stepWait = 3 * time.Second
)

func recvServer(t *testing.T, ch <-chan *httpapi.Server) *httpapi.Server {
	t.Helper()
	select {
	case srv := <-ch:
		return srv
	case <-time.After(stepWait):
		t.Fatalf("server was not built within %s", stepWait)
	}
	return nil
}

func waitCh(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(stepWait):
		t.Fatalf("wait expired after %s", stepWait)
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
	t.Fatalf("%s kept accepting connections after SIGTERM", rawURL)
}
