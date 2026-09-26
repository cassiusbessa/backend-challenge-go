package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

type Pipeline struct {
	Logger   *slog.Logger
	Tracer   trace.Tracer
	stopped  chan struct{}
	stop     sync.Once
	base     *slog.Logger
	tracer   *sdktrace.TracerProvider
	logs     *sdklog.LoggerProvider
	ratio    float64
	endpoint string
}

func NewPipeline(cfg config.Config) *Pipeline {
	pipe := &Pipeline{
		stopped:  make(chan struct{}),
		ratio:    cfg.SampleRatio,
		endpoint: cfg.OTELEndpoint,
	}
	pipe.tracer = sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler(cfg.SampleRatio)))
	pipe.Tracer = pipe.tracer.Tracer("wager")
	pipe.base = slog.New(Allow(slog.NewJSONHandler(os.Stdout, nil)))
	pipe.Logger = pipe.base
	return pipe
}

// Started answers a pipeline whose exporters are already installed.
//
// It exists because the moment of the start decides what the rest of the
// process gets: Start replaces the Tracer and the Logger the pipeline had
// handed out, and the provider NewPipeline builds carries no exporter, so
// anything holding the earlier pair emits into nothing.
func Started(cfg config.Config) (*Pipeline, error) {
	pipe := NewPipeline(cfg)
	if err := pipe.Start(context.Background()); err != nil {
		return nil, err
	}
	return pipe, nil
}

// Exporting reports whether Start has installed the exporters. A pipeline that
// answers false still hands out a Tracer and a Logger, and everything written
// to them stays inside the process.
func (p *Pipeline) Exporting() bool {
	return p.logs != nil
}

// Start installs the trace and log exporters. Process metrics are deliberately
// left out: they stay on /metrics so that heap and goroutines have a single
// source instead of two that can disagree.
func (p *Pipeline) Start(ctx context.Context) error {
	if err := p.installTrace(ctx); err != nil {
		return err
	}
	return p.installLogs(ctx)
}

func (p *Pipeline) Shutdown(ctx context.Context) error {
	p.stop.Do(func() { close(p.stopped) })
	p.report(errors.Join(p.shutdownTracer(ctx), p.shutdownLogs(ctx)))
	return nil
}

// report logs telemetry lost on shutdown instead of propagating it: dropped
// telemetry is not a reason for the process to exit with an error.
func (p *Pipeline) report(err error) {
	if err == nil {
		return
	}
	p.base.Error("telemetry did not flush", slog.String("status", "error"))
}

func (p *Pipeline) Stopped() <-chan struct{} {
	return p.stopped
}

func (p *Pipeline) installTrace(ctx context.Context) error {
	exp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(hostPort(p.endpoint)), otlptracegrpc.WithInsecure(), otlptracegrpc.WithTimeout(exportTimeout))
	if err != nil {
		return err
	}
	if p.tracer != nil {
		_ = p.tracer.Shutdown(ctx)
	}
	p.tracer = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithSampler(sampler(p.ratio)),
		sdktrace.WithResource(serviceResource()),
	)
	p.Tracer = p.tracer.Tracer("wager")
	return nil
}

func (p *Pipeline) installLogs(ctx context.Context) error {
	exp, err := otlploggrpc.New(ctx, otlploggrpc.WithEndpoint(hostPort(p.endpoint)), otlploggrpc.WithInsecure(), otlploggrpc.WithTimeout(exportTimeout))
	if err != nil {
		return err
	}
	p.logs = sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		sdklog.WithResource(serviceResource()),
	)
	otelHandler := otelslog.NewHandler("wager", otelslog.WithLoggerProvider(p.logs))
	jsonHandler := slog.NewJSONHandler(os.Stdout, nil)
	p.Logger = slog.New(Allow(fanout{a: jsonHandler, b: otelHandler}))
	return nil
}

func (p *Pipeline) shutdownTracer(ctx context.Context) error {
	if p.tracer == nil {
		return nil
	}
	return p.tracer.Shutdown(ctx)
}

func (p *Pipeline) shutdownLogs(ctx context.Context) error {
	if p.logs == nil {
		return nil
	}
	return p.logs.Shutdown(ctx)
}

const exportTimeout = time.Second

func sampler(ratio float64) sdktrace.Sampler {
	if ratio >= 1 {
		return sdktrace.AlwaysSample()
	}
	return ratioSampler(ratio)
}

func ratioSampler(ratio float64) sdktrace.Sampler {
	if ratio <= 0 {
		return sdktrace.NeverSample()
	}
	return sdktrace.TraceIDRatioBased(ratio)
}

func serviceResource() *resource.Resource {
	return resource.NewSchemaless(attribute.String("service.name", "wager"))
}

func hostPort(raw string) string {
	trimmed := strings.TrimPrefix(raw, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	return strings.TrimSuffix(trimmed, "/")
}

type fanout struct {
	a slog.Handler
	b slog.Handler
}

func (f fanout) Enabled(ctx context.Context, level slog.Level) bool {
	return f.a.Enabled(ctx, level) || f.b.Enabled(ctx, level)
}

func (f fanout) Handle(ctx context.Context, rec slog.Record) error {
	errA := f.a.Handle(ctx, rec.Clone())
	errB := f.b.Handle(ctx, rec.Clone())
	return errors.Join(errA, errB)
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	return fanout{a: f.a.WithAttrs(attrs), b: f.b.WithAttrs(attrs)}
}

func (f fanout) WithGroup(name string) slog.Handler {
	return fanout{a: f.a.WithGroup(name), b: f.b.WithGroup(name)}
}
