package app

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/app/openwallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/readwallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/platform/authz"
	"github.com/junglegaming/backend-challenge-go/internal/platform/clock"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/platform/mint"
	"github.com/junglegaming/backend-challenge-go/internal/platform/postgres"
	"github.com/junglegaming/backend-challenge-go/internal/platform/probe"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
	"github.com/junglegaming/backend-challenge-go/internal/platform/walletapi"
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
		fx.Provide(postgres.NewPool),
		fx.Provide(probe.NewPostgres),
		fx.Provide(probe.NewQueue),
		fx.Provide(func(p *probe.Postgres) httpapi.PostgresChecker { return p }),
		fx.Provide(func(q *probe.Queue) httpapi.QueueChecker { return q }),
		fx.Provide(httpapi.NewReady),
		fx.Provide(httpapi.NewMetrics),
		fx.Provide(newServer),
	}
	return fx.New(append(append(options, business()...), opts...)...)
}

// business is the wiring of the use cases and their border. It is separate from
// the process wiring so that adding a use case does not touch the lifecycle.
func business() []fx.Option {
	return []fx.Option{
		fx.Provide(newUnitOfWork),
		fx.Provide(newReads),
		fx.Provide(func() openwallet.Minter { return mint.UUIDv7{} }),
		fx.Provide(func() openwallet.Clock { return clock.UTC{} }),
		fx.Provide(openwallet.New),
		fx.Provide(readwallet.New),
		// The client map is loaded here, so a map that is missing or malformed
		// fails the graph and the process never opens the HTTP port.
		fx.Provide(newClients),
		fx.Provide(newGuard),
		fx.Provide(newReporter),
		fx.Invoke(register),
	}
}

func newUnitOfWork(pool *postgres.Pool) storage.UnitOfWork {
	return postgres.NewUnitOfWork(pool)
}

func newReads(pool *postgres.Pool) storage.Reads {
	return postgres.NewReads(pool)
}

func newClients(cfg config.Config) (*authz.Clients, error) {
	return authz.LoadClients(cfg.ClientsPath)
}

func newGuard(cfg config.Config, clients *authz.Clients) *authz.Guard {
	return authz.NewGuard(authz.NewVerifier(cfg), clients)
}

func newReporter(pipe *telemetry.Pipeline) *walletapi.Reporter {
	return walletapi.NewReporter(pipe.Logger)
}

func newServer(cfg config.Config, pipe *telemetry.Pipeline) *httpapi.Server {
	return httpapi.NewServer(cfg, http.NewServeMux(), pipe.Logger)
}

// wiring is what the routes need, gathered so that register keeps one parameter
// per component group instead of a dozen.
type wiring struct {
	fx.In

	Config   config.Config
	Pipeline *telemetry.Pipeline
	Postgres *postgres.Pool
	Queue    *probe.Queue
	Ready    *httpapi.Ready
	Registry *prometheus.Registry
	Latency  *prometheus.HistogramVec
	Server   *httpapi.Server
	Guard    *authz.Guard
	Opener   *openwallet.Service
	Reader   *readwallet.Service
	Reporter *walletapi.Reporter
}

func register(lc fx.Lifecycle, parts wiring) {
	lc.Append(fx.Hook{OnStart: func(context.Context) error { return parts.Config.Validate() }})
	lc.Append(fx.Hook{OnStart: parts.Pipeline.Start, OnStop: parts.Pipeline.Shutdown})
	lc.Append(fx.Hook{OnStart: parts.Postgres.Open, OnStop: parts.Postgres.Close})
	lc.Append(fx.Hook{OnStart: parts.Queue.Open})
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			parts.Server.Use(routes(parts))
			return parts.Server.Start(ctx)
		},
		OnStop: parts.Server.Shutdown,
	})
}

func routes(parts wiring) http.Handler {
	return httpapi.Handler(httpapi.Routes{
		Live:    http.HandlerFunc(httpapi.Live),
		Ready:   parts.Ready,
		Metrics: httpapi.MetricsHandler(parts.Registry),
		// Only the internal wallet client opens and reads a wallet. A provider
		// sends wagers and never touches these two routes.
		OpenWallet: parts.Guard.Only(authz.InternalWallet, walletapi.Open(parts.Opener, parts.Reporter)),
		ReadWallet: parts.Guard.Only(authz.InternalWallet, walletapi.Read(parts.Reader, parts.Reporter)),
		Logger:     parts.Pipeline.Logger,
		Tracer:     parts.Pipeline.Tracer,
		Latency:    parts.Latency,
	})
}
