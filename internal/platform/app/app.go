package app

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/platform/probe"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

func LoadAndRun(getenv func(string) string, signals <-chan os.Signal, run func(config.Config, <-chan os.Signal) error) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	return run(cfg, signals)
}

func Boot(cfg config.Config, signals <-chan os.Signal) error {
	return Run(New(cfg), signals, cfg.ShutdownTimeout)
}

func Run(application *fx.App, signals <-chan os.Signal, timeout time.Duration) error {
	if err := start(application, timeout); err != nil {
		return err
	}
	<-signals
	return stop(application, timeout)
}

func start(application *fx.App, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return application.Start(ctx)
}

func stop(application *fx.App, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return application.Stop(ctx)
}

func New(cfg config.Config, opts ...fx.Option) *fx.App {
	options := []fx.Option{
		fx.NopLogger,
		fx.Supply(cfg),
		fx.StartTimeout(cfg.ShutdownTimeout),
		fx.StopTimeout(cfg.ShutdownTimeout),
		fx.Provide(telemetry.NewPipeline),
		fx.Provide(probe.NewPostgres),
		fx.Provide(probe.NewQueue),
		fx.Provide(func(p *probe.Postgres) httpapi.PostgresChecker { return p }),
		fx.Provide(func(q *probe.Queue) httpapi.QueueChecker { return q }),
		fx.Provide(httpapi.NewReady),
		fx.Provide(httpapi.NewMetrics),
		fx.Provide(newServer),
		fx.Invoke(register),
	}
	return fx.New(append(options, opts...)...)
}

func newServer(cfg config.Config, pipe *telemetry.Pipeline) *httpapi.Server {
	return httpapi.NewServer(cfg, http.NewServeMux(), pipe.Logger)
}

func register(lc fx.Lifecycle, cfg config.Config, pipe *telemetry.Pipeline, pg *probe.Postgres, q *probe.Queue, ready *httpapi.Ready, reg *prometheus.Registry, latency *prometheus.HistogramVec, srv *httpapi.Server) {
	lc.Append(fx.Hook{OnStart: func(context.Context) error { return cfg.Validate() }})
	lc.Append(fx.Hook{OnStart: pipe.Start, OnStop: pipe.Shutdown})
	lc.Append(fx.Hook{OnStart: pg.Open, OnStop: pg.Close})
	lc.Append(fx.Hook{OnStart: q.Open})
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			srv.Use(routes(pipe, ready, reg, latency))
			return srv.Start(ctx)
		},
		OnStop: srv.Shutdown,
	})
}

func routes(pipe *telemetry.Pipeline, ready *httpapi.Ready, reg *prometheus.Registry, latency *prometheus.HistogramVec) http.Handler {
	return httpapi.Handler(httpapi.Routes{
		Live:    http.HandlerFunc(httpapi.Live),
		Ready:   ready,
		Metrics: httpapi.MetricsHandler(reg),
		Logger:  pipe.Logger,
		Tracer:  pipe.Tracer,
		Latency: latency,
	})
}
