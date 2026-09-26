package telemetry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

func TestHostPortDropsTheScheme(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"http://otel-collector:4317":  "otel-collector:4317",
		"https://otel-collector:4317": "otel-collector:4317",
		"otel-collector:4317":         "otel-collector:4317",
		"http://otel-collector:4317/": "otel-collector:4317",
	}
	for raw, want := range cases {
		t.Run(raw+" becomes "+want, func(t *testing.T) {
			got := hostPort(raw)
			if got != want {
				t.Fatalf("hostPort = %s, want %s", got, want)
			}
		})
	}
}

func TestSamplerFollowsTheConfiguredRatio(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ratio float64
		want  string
	}{
		{ratio: 1, want: "AlwaysOnSampler"},
		{ratio: 0, want: "AlwaysOffSampler"},
		{ratio: 0.25, want: "TraceIDRatioBased"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			got := sampler(c.ratio).Description()
			if !strings.HasPrefix(got, c.want) {
				t.Fatalf("sampler = %s, want %s", got, c.want)
			}
		})
	}
}

func TestStartInstallsTheOTLPExportersWithoutReachingTheCollector(t *testing.T) {
	t.Parallel()
	pipe := NewPipeline(config.Config{OTELEndpoint: "http://127.0.0.1:1", SampleRatio: 1})
	ctx := context.Background()
	// The constructor already leaves a provider in place, so asserting that one
	// exists after Start says nothing about Start. What it installs has to be a
	// different one: the exporter cannot be added to a provider already built.
	beforeStart := pipe.tracer
	if err := pipe.Start(ctx); err != nil {
		t.Fatalf("start with an unreachable collector = %v, want nil", err)
	}
	if pipe.tracer == beforeStart {
		t.Fatalf("provider after start is the one the constructor built, want the one start installs")
	}
	if pipe.logs == nil {
		t.Fatalf("logger provider = %v, want one after start", pipe.logs)
	}
	if err := pipe.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown after start = %v, want nil", err)
	}
}

func TestReportLogsTheIncompleteFlushWithoutTheErrorText(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	pipe := NewPipeline(config.Config{OTELEndpoint: "127.0.0.1:1", SampleRatio: 1})
	pipe.base = slog.New(Allow(slog.NewJSONHandler(buf, nil)))
	pipe.report(errors.New("exporter refused the batch"))
	line := buf.String()
	if !strings.Contains(line, `"status":"error"`) {
		t.Fatalf("report line = %q, want a status error attribute", line)
	}
	if strings.Contains(line, "exporter refused the batch") {
		t.Fatalf("report line = %q, want the error text withheld", line)
	}
}

func TestReportStaysSilentWithoutAnError(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	pipe := NewPipeline(config.Config{OTELEndpoint: "127.0.0.1:1", SampleRatio: 1})
	pipe.base = slog.New(Allow(slog.NewJSONHandler(buf, nil)))
	pipe.report(nil)
	if buf.Len() != 0 {
		t.Fatalf("report(nil) wrote %q, want nothing", buf.String())
	}
}

func TestFanoutDeliversAttributesToBothHandlers(t *testing.T) {
	t.Parallel()
	first := &bytes.Buffer{}
	second := &bytes.Buffer{}
	handler := fanout{a: slog.NewJSONHandler(first, nil), b: slog.NewJSONHandler(second, nil)}
	logger := slog.New(handler.WithAttrs([]slog.Attr{slog.String("walletId", "w-1")}))
	logger.Info("request")
	if !strings.Contains(first.String(), "w-1") {
		t.Fatalf("first handler = %q, want walletId w-1", first.String())
	}
	if !strings.Contains(second.String(), "w-1") {
		t.Fatalf("second handler = %q, want walletId w-1", second.String())
	}
}

func TestFanoutCarriesTheGroupToBothHandlers(t *testing.T) {
	t.Parallel()
	first := &bytes.Buffer{}
	second := &bytes.Buffer{}
	handler := fanout{a: slog.NewJSONHandler(first, nil), b: slog.NewJSONHandler(second, nil)}
	logger := slog.New(handler.WithGroup("wager"))
	logger.Info("request", slog.String("status", "200"))
	if !strings.Contains(first.String(), `"wager":{"status":"200"}`) {
		t.Fatalf("first handler = %q, want a wager group", first.String())
	}
	if !strings.Contains(second.String(), `"wager":{"status":"200"}`) {
		t.Fatalf("second handler = %q, want a wager group", second.String())
	}
}

func TestShutdownWithoutStartStaysSilentAndRepeatable(t *testing.T) {
	t.Parallel()
	pipe := NewPipeline(config.Config{OTELEndpoint: "127.0.0.1:1", SampleRatio: 1})
	ctx := context.Background()
	first := pipe.Shutdown(ctx)
	if first != nil {
		t.Fatalf("first shutdown = %v, want nil", first)
	}
	second := pipe.Shutdown(ctx)
	if second != nil {
		t.Fatalf("second shutdown = %v, want nil", second)
	}
	closed := false
	select {
	case <-pipe.Stopped():
		closed = true
	default:
	}
	if !closed {
		t.Fatalf("Stopped closed = %v, want true", closed)
	}
}

// recordingProcessor answers whether the provider holding it was shut down. The
// SDK offers no way to ask a TracerProvider whether it is still running, and the
// life of the provider is what installTrace and shutdownTracer decide.
type recordingProcessor struct{ shutdown bool }

func (r *recordingProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (r *recordingProcessor) OnEnd(sdktrace.ReadOnlySpan) {}

func (r *recordingProcessor) ForceFlush(context.Context) error { return nil }

func (r *recordingProcessor) Shutdown(context.Context) error {
	r.shutdown = true
	return nil
}

// watched builds a pipeline whose current provider reports its own shutdown.
func watched(t *testing.T) (*Pipeline, *recordingProcessor) {
	t.Helper()
	pipe := NewPipeline(config.Config{OTELEndpoint: "127.0.0.1:1", SampleRatio: 1})
	watcher := &recordingProcessor{}
	pipe.tracer = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(watcher))
	return pipe, watcher
}

// A provider left running keeps its batcher and its exporter connection, and
// installTrace replaces the provider on every call.
func TestInstallTrace_shutsDownTheProviderItReplaces(t *testing.T) {
	t.Parallel()
	pipe, watcher := watched(t)
	if err := pipe.installTrace(context.Background()); err != nil {
		t.Fatalf("installTrace over a provider already in place = %v, want nil", err)
	}
	if !watcher.shutdown {
		t.Fatalf("replaced provider shut down = %t, want true", watcher.shutdown)
	}
}

func TestShutdownTracer_shutsDownTheProviderInPlace(t *testing.T) {
	t.Parallel()
	pipe, watcher := watched(t)
	if err := pipe.shutdownTracer(context.Background()); err != nil {
		t.Fatalf("shutdownTracer with a provider in place = %v, want nil", err)
	}
	if !watcher.shutdown {
		t.Fatalf("provider in place shut down = %t, want true", watcher.shutdown)
	}
}
