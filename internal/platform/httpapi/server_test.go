package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

func TestOtherRoutesReturn404(t *testing.T) {
	t.Parallel()
	handler, _ := testHandler(t)
	paths := []string{"/wagers", "/reconciliation", "/debug/pprof/"}
	for _, path := range paths {
		t.Run(path+" answers 404", func(t *testing.T) {
			got := codeOf(t, handler, path)
			if got != http.StatusNotFound {
				t.Fatalf("%s = %d, want 404", path, got)
			}
		})
	}
}

// The wallet routes of this delivery are served, so they no longer answer the
// 404 of a route that does not exist.
func TestHandler_servesTheWalletRoutes(t *testing.T) {
	t.Parallel()
	handler, _ := testHandler(t)
	if got := codeOf(t, handler, "/wallets/11111111-1111-4111-8111-111111111111"); got != http.StatusOK {
		t.Fatalf("GET /wallets/{walletId} = %d, want the route to answer", got)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/wallets", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /wallets = %d, want the route to answer", rec.Code)
	}
}

// The wager routes of this delivery are served too. The span name is the pattern
// of the route and not the path, so a transaction identity does not give every
// request a series of its own.
func TestHandler_servesTheWagerRoutes(t *testing.T) {
	t.Parallel()
	handler, _ := testHandler(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/wagering/transactions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /wagering/transactions = %d, want the route to answer", rec.Code)
	}
	got := codeOf(t, handler, "/wagering/transactions/33333333-3333-4333-8333-333333333333")
	if got != http.StatusOK {
		t.Fatalf("GET /wagering/transactions/{transactionId} = %d, want the route to answer", got)
	}
}

func TestMetricsExposeHeapAndGoroutinesWithoutDomainLabels(t *testing.T) {
	t.Parallel()
	body := metricsBody(t)
	assertMetric(t, body, "go_goroutines")
	assertMetric(t, body, "heap")
	for _, banned := range []string{"walletId", "providerId"} {
		if strings.Contains(body, banned) {
			t.Fatalf("domain label %q in /metrics", banned)
		}
	}
	assertMetric(t, body, "trace_id")
}

func metricsBody(t *testing.T) string {
	t.Helper()
	handler, _ := testHandler(t)
	_ = codeOf(t, handler, "/wagers")
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text;version=1.0.0")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

func assertMetric(t *testing.T, body, name string) {
	t.Helper()
	if !strings.Contains(body, name) {
		t.Fatalf("metrics are missing %s", name)
	}
}

func TestRequestLogOmitsSecrets(t *testing.T) {
	t.Parallel()
	handler, buf := testHandler(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/wagers", strings.NewReader(`{"amount":"25.00","balance":"10.00"}`))
	req.Header.Set("Authorization", "Bearer super-secret-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/wagers = %d, want 404", rec.Code)
	}
	line := buf.String()
	for _, banned := range []string{"Bearer", "super-secret-token", "25.00", "10.00", "authorization"} {
		if strings.Contains(line, banned) {
			t.Fatalf("log contains %q: %s", banned, line)
		}
	}
}

func TestOpaqueCorrelationIDIsKept(t *testing.T) {
	t.Parallel()
	handler, buf := testHandler(t)
	correlation := "req-123.abc_DEF:ghi"
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/wagers", nil)
	req.Header.Set("X-Correlation-Id", correlation)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	fields := logFields(t, buf.String())
	if fields["correlationId"] != correlation {
		t.Fatalf("correlationId = %v, want %s", fields["correlationId"], correlation)
	}
}

func TestInvalidCorrelationIDFallsBackToTrace(t *testing.T) {
	t.Parallel()
	cases := []string{"bad id", strings.Repeat("a", 65)}
	for _, header := range cases {
		t.Run(header+" falls back to trace_id", func(t *testing.T) {
			handler, buf := testHandler(t)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/wagers", nil)
			req.Header.Set("X-Correlation-Id", header)
			handler.ServeHTTP(httptest.NewRecorder(), req)
			fields := logFields(t, buf.String())
			if fields["correlationId"] == header {
				t.Fatalf("correlationId = %v, want anything but the header %q", fields["correlationId"], header)
			}
			if fields["correlationId"] == "" || fields["correlationId"] != fields["trace_id"] {
				t.Fatalf("correlationId = %v, trace_id = %v", fields["correlationId"], fields["trace_id"])
			}
		})
	}
}

func TestOperationalRoutesLeaveNoLogLine(t *testing.T) {
	t.Parallel()
	paths := []string{"/health/live", "/health/ready", "/metrics"}
	for _, path := range paths {
		t.Run(path+" writes no log line", func(t *testing.T) {
			handler, buf := testHandler(t)
			code := codeOf(t, handler, path)
			if code != http.StatusOK {
				t.Fatalf("%s = %d, want 200", path, code)
			}
			if buf.Len() != 0 {
				t.Fatalf("log = %q, want empty", buf.String())
			}
		})
	}
}

func TestPprofDoesNotAnswerOnAPIPort(t *testing.T) {
	t.Parallel()
	handler, _ := testHandler(t)
	got := codeOf(t, handler, "/debug/pprof/")
	if got != http.StatusNotFound {
		t.Fatalf("pprof on the API mux = %d, want 404", got)
	}
}

func testHandler(t *testing.T) (http.Handler, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	reg, latency := NewMetrics()
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer = %v, want nil", err)
		}
	})
	handler := Handler(Routes{
		Live:            http.HandlerFunc(Live),
		Ready:           NewReady(okProbe{}, okProbe{}),
		Metrics:         MetricsHandler(reg),
		OpenWallet:      answering(http.StatusCreated),
		ReadWallet:      answering(http.StatusOK),
		SubmitWager:     answering(http.StatusCreated),
		ReadTransaction: answering(http.StatusOK),
		Logger:          slog.New(telemetry.Allow(slog.NewJSONHandler(buf, nil))),
		Tracer:          provider.Tracer("test"),
		Latency:         latency,
	})
	return handler, buf
}

// answering stands in for the wallet border, so the routing test checks the
// wiring and not the handler behind it.
func answering(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	})
}

func logFields(t *testing.T, raw string) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("decode log %q: %v", raw, err)
	}
	return fields
}

func TestPprofListensApartFromTheAPI(t *testing.T) {
	t.Parallel()
	reg, latency := NewMetrics()
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler := Handler(Routes{
		Live:    http.HandlerFunc(Live),
		Ready:   NewReady(okProbe{}, okProbe{}),
		Metrics: MetricsHandler(reg),
		Logger:  logger,
		Tracer:  provider.Tracer("test"),
		Latency: latency,
	})
	srv := NewServer(config.Config{HTTPAddr: "127.0.0.1:0", PPROFAddr: "127.0.0.1:0"}, handler, logger)
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start with both ports = %v, want nil", err)
	}
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
	})
	api := getStatus(t, "http://"+srv.Addr()+"/debug/pprof/")
	if api != http.StatusNotFound {
		t.Fatalf("pprof on the API port = %d, want 404", api)
	}
	metrics := getStatus(t, "http://"+srv.Addr()+"/metrics")
	if metrics != http.StatusOK {
		t.Fatalf("metrics = %d, want 200", metrics)
	}
}

func TestUseSwapsTheHandlerAndListeningClosesAfterStart(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := NewServer(config.Config{HTTPAddr: "127.0.0.1:0", PPROFAddr: "127.0.0.1:0"}, http.NotFoundHandler(), logger)
	srv.Use(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start = %v, want nil", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	listening := false
	select {
	case <-srv.Listening():
		listening = true
	default:
	}
	if !listening {
		t.Fatalf("Listening closed = %v, want true after start", listening)
	}
	got := getStatus(t, "http://"+srv.Addr()+"/anything")
	if got != http.StatusTeapot {
		t.Fatalf("swapped handler = %d, want 418", got)
	}
}

func TestStartFailsWhenAPortCannotBeBound(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := NewServer(config.Config{HTTPAddr: "127.0.0.1:0", PPROFAddr: "127.0.0.1:999999"}, http.NotFoundHandler(), logger)
	err := srv.Start(context.Background())
	if err == nil {
		t.Fatalf("start on an invalid pprof port = %v, want a listen error", err)
	}
}

func getStatus(t *testing.T, rawURL string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("build request = %v, want nil", err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s: %v", rawURL, err)
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

// listening gives serve a listener of its own, so the case can decide how Serve
// ends: Start hides the listener inside the goroutine it launches, and from
// outside there is no way to close that one or to know when it returned.
func listening(t *testing.T) (*Server, *http.Server, net.Listener, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))
	server := NewServer(config.Config{HTTPAddr: "127.0.0.1:0", PPROFAddr: "127.0.0.1:0"}, http.NotFoundHandler(), logger)
	srv := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for the fixture = %v, want nil", err)
	}
	return server, srv, ln, buf
}

func TestServe_reportsAnEndThatIsNotTheCleanClose(t *testing.T) {
	t.Parallel()
	server, srv, ln, buf := listening(t)
	// Closing the listener out from under Serve ends it with net.ErrClosed, which
	// is the shape of a server that stopped without being asked to.
	if err := ln.Close(); err != nil {
		t.Fatalf("close of the listener = %v, want nil", err)
	}
	server.serve(srv, ln)
	if !strings.Contains(buf.String(), `"status":"error"`) {
		t.Fatalf("serve after a broken listener wrote %q, want a status error attribute", buf.String())
	}
}

func TestServe_saysNothingWhenTheServerWasAskedToStop(t *testing.T) {
	t.Parallel()
	server, srv, ln, buf := listening(t)
	t.Cleanup(func() { _ = ln.Close() })
	// Close before Serve makes it end with http.ErrServerClosed, the same error a
	// shutdown produces, and that end is the ordinary one.
	if err := srv.Close(); err != nil {
		t.Fatalf("close of the server = %v, want nil", err)
	}
	server.serve(srv, ln)
	if buf.Len() != 0 {
		t.Fatalf("serve after a clean close wrote %q, want nothing", buf.String())
	}
}
