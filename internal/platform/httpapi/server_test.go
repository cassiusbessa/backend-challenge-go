package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

func TestOtherRoutesReturn404(t *testing.T) {
	t.Parallel()
	handler, _ := testHandler(t)
	paths := []string{"/wagers", "/wallets", "/reconciliation", "/debug/pprof/"}
	for _, path := range paths {
		t.Run(path+" responde 404", func(t *testing.T) {
			got := codeOf(t, handler, path)
			if got != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", got)
			}
		})
	}
}

func TestMetricsExposeHeapAndGoroutinesWithoutDomainLabels(t *testing.T) {
	t.Parallel()
	body := metricsBody(t)
	assertMetric(t, body, "go_goroutines")
	assertMetric(t, body, "heap")
	if strings.Contains(body, "walletId") || strings.Contains(body, "providerId") {
		t.Fatal("rótulo de domínio em /metrics")
	}
	assertMetric(t, body, "trace_id")
}

func metricsBody(t *testing.T) string {
	t.Helper()
	handler, _ := testHandler(t)
	_ = codeOf(t, handler, "/health/live")
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "application/openmetrics-text;version=1.0.0")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

func assertMetric(t *testing.T, body, name string) {
	t.Helper()
	if !strings.Contains(body, name) {
		t.Fatalf("métricas sem %s", name)
	}
}

func TestHealthLogOmitsSecrets(t *testing.T) {
	t.Parallel()
	handler, buf := testHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/health/live", strings.NewReader(`{"amount":"25.00","balance":"10.00"}`))
	req.Header.Set("Authorization", "Bearer super-secret-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	line := buf.String()
	for _, banned := range []string{"Bearer", "super-secret-token", "25.00", "10.00", "authorization"} {
		if strings.Contains(line, banned) {
			t.Fatalf("log contém %q: %s", banned, line)
		}
	}
}

func TestOpaqueCorrelationIDIsKept(t *testing.T) {
	t.Parallel()
	handler, buf := testHandler(t)
	correlation := "req-123.abc_DEF:ghi"
	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
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
		t.Run(header+" cai no trace_id", func(t *testing.T) {
			handler, buf := testHandler(t)
			req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
			req.Header.Set("X-Correlation-Id", header)
			handler.ServeHTTP(httptest.NewRecorder(), req)
			fields := logFields(t, buf.String())
			if fields["correlationId"] == header {
				t.Fatal("log usou o header inválido")
			}
			if fields["correlationId"] == "" || fields["correlationId"] != fields["trace_id"] {
				t.Fatalf("correlationId = %v, trace_id = %v", fields["correlationId"], fields["trace_id"])
			}
		})
	}
}

func TestPprofDoesNotAnswerOnAPIPort(t *testing.T) {
	t.Parallel()
	handler, _ := testHandler(t)
	got := codeOf(t, handler, "/debug/pprof/")
	if got != http.StatusNotFound {
		t.Fatalf("pprof na API = %d, want 404", got)
	}
}

func testHandler(t *testing.T) (http.Handler, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	reg, latency := NewMetrics()
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer: %v", err)
		}
	})
	handler := Handler(Routes{
		Live:    http.HandlerFunc(Live),
		Ready:   NewReady(okProbe{}, okProbe{}),
		Metrics: MetricsHandler(reg),
		Logger:  slog.New(telemetry.Allow(slog.NewJSONHandler(buf, nil))),
		Tracer:  provider.Tracer("test"),
		Latency: latency,
	})
	return handler, buf
}

func logFields(t *testing.T, raw string) map[string]any {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("log %q: %v", raw, err)
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
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
	})
	api := getStatus(t, "http://"+srv.Addr()+"/debug/pprof/")
	if api != http.StatusNotFound {
		t.Fatalf("pprof na API = %d, want 404", api)
	}
	metrics := getStatus(t, "http://"+srv.Addr()+"/metrics")
	if metrics != http.StatusOK {
		t.Fatalf("metrics = %d, want 200", metrics)
	}
}

func getStatus(t *testing.T, rawURL string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
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
