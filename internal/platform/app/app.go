package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/app/listledger"
	"github.com/junglegaming/backend-challenge-go/internal/app/openwallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/readwager"
	"github.com/junglegaming/backend-challenge-go/internal/app/readwallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/receivewager"
	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/referencewait"
	"github.com/junglegaming/backend-challenge-go/internal/app/relayoutbox"
	"github.com/junglegaming/backend-challenge-go/internal/app/resolvereference"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/authz"
	"github.com/junglegaming/backend-challenge-go/internal/platform/broker"
	"github.com/junglegaming/backend-challenge-go/internal/platform/clock"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
	"github.com/junglegaming/backend-challenge-go/internal/platform/mint"
	"github.com/junglegaming/backend-challenge-go/internal/platform/outboxrelay"
	"github.com/junglegaming/backend-challenge-go/internal/platform/postgres"
	"github.com/junglegaming/backend-challenge-go/internal/platform/probe"
	"github.com/junglegaming/backend-challenge-go/internal/platform/referenceworker"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
	"github.com/junglegaming/backend-challenge-go/internal/platform/wagerapi"
	"github.com/junglegaming/backend-challenge-go/internal/platform/wagerqueue"
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
	// The pipeline is taken out of the graph here because Run flushes it after the
	// lifecycle, not as a stop of it. fx.Populate fills it while the app is built,
	// so it is in hand before anything starts.
	var pipe *telemetry.Pipeline
	return Run(New(cfg, fx.Populate(&pipe)), pipe, signals, cfg.ShutdownTimeout)
}

func Run(application *fx.App, pipe *telemetry.Pipeline, signals <-chan os.Signal, timeout time.Duration) error {
	if err := start(application, timeout); err != nil {
		// A start that failed is the one with something to report, and nothing
		// after this point would carry it out: the lifecycle never reached a stop.
		return errors.Join(err, flushed(pipe))
	}
	<-signals
	return stop(application, pipe, timeout)
}

func start(application *fx.App, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return application.Start(ctx)
}

// flushBudget is what the telemetry flush has after the lifecycle is done with it,
// and it is outside the shutdown budget on purpose.
//
// go-observability asks the buffer to be flushed on SIGTERM, and a hook cannot
// promise that: the Fx lifecycle returns the moment the shared deadline expires and
// skips every stop it had not reached, so a flush registered as a hook is the first
// thing lost in exactly the shutdown that had something to say. Bounding
// Pipeline.Shutdown from the inside would not help — the hook is never called.
const flushBudget = 3 * time.Second

// stop stops the lifecycle and then flushes the telemetry, in that order and on
// separate budgets. The flush comes last because what it has to carry is the
// shutdown itself, including the stop that overran.
func stop(application *fx.App, pipe *telemetry.Pipeline, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	stopped := application.Stop(ctx)
	return errors.Join(stopped, flushed(pipe))
}

// flushed empties the telemetry buffer on a budget of its own. It answers nil
// for a graph that never built a pipeline, which has no buffer to carry out.
func flushed(pipe *telemetry.Pipeline) error {
	if pipe == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), flushBudget)
	defer cancel()
	return pipe.Shutdown(ctx)
}

func New(cfg config.Config, opts ...fx.Option) *fx.App {
	options := []fx.Option{
		fx.NopLogger,
		fx.Supply(cfg),
		// These two are inert as the process runs today: fx reads them only inside
		// App.Run, and Run here drives Start and Stop with deadlines of its own.
		// They stay as the contract for the day the lifecycle is handed to fx,
		// because without them that day would silently take the default of the
		// library instead of the budget of this process.
		fx.StartTimeout(cfg.ShutdownTimeout),
		fx.StopTimeout(cfg.ShutdownTimeout),
		// A constructor, and not a lifecycle hook: every constructor of the graph
		// runs before any hook, so a pipeline started as a hook hands its Tracer
		// and Logger to the background work and only then replaces them.
		fx.Provide(newPipeline),
		fx.Provide(postgres.NewPool),
		fx.Provide(probe.NewPostgres),
		fx.Provide(probe.NewQueue),
		fx.Provide(broker.NewTopic),
		fx.Provide(broker.NewIngress),
		fx.Provide(func(p *probe.Postgres) httpapi.PostgresChecker { return p }),
		fx.Provide(func(q *probe.Queue) httpapi.QueueChecker { return q }),
		fx.Provide(httpapi.NewReady),
		fx.Provide(httpapi.NewMetrics),
		fx.Provide(newSettlementMetrics),
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
		fx.Provide(func() submitwager.Minter { return mint.UUIDv7{} }),
		fx.Provide(func() submitwager.Clock { return clock.UTC{} }),
		fx.Provide(newSchedule),
		fx.Provide(func(schedule referencewait.Schedule) submitwager.Schedule { return schedule }),
		fx.Provide(func(schedule referencewait.Schedule) resolvereference.Schedule { return schedule }),
		fx.Provide(func() resolvereference.Minter { return mint.UUIDv7{} }),
		fx.Provide(func() resolvereference.Clock { return clock.UTC{} }),
		fx.Provide(resolvereference.New),
		fx.Provide(newReferenceWorker),
		fx.Provide(newOutboxQueue),
		fx.Provide(newRelay),
		fx.Provide(newOutboxRelay),
		fx.Provide(openwallet.New),
		fx.Provide(readwallet.New),
		fx.Provide(listledger.New),
		fx.Provide(reconcilewallet.New),
		fx.Provide(submitwager.New),
		fx.Provide(readwager.New),
		// The client map is loaded here, so a map that is missing or malformed
		// fails the graph and the process never opens the HTTP port.
		fx.Provide(newClients),
		// The sender map, for the same reason: a process that cannot say who may
		// send must not open the port and must not consume from the queue.
		fx.Provide(newSenders),
		fx.Provide(newGuard),
		fx.Provide(newReporter),
		fx.Provide(newWagerReporter),
		fx.Provide(newQueueReporter),
		fx.Provide(newReceiver),
		fx.Provide(newConsumer),
		fx.Invoke(register),
	}
}

// newPipeline starts the telemetry before any constructor reads it, on a
// deadline of its own. The graph is built by New, which runs before Run has a
// context, so an exporter that hung here would hang against no budget at all.
func newPipeline(cfg config.Config) (*telemetry.Pipeline, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return telemetry.Started(ctx, cfg)
}

// newSchedule is the single policy of the wait: the use case that writes one and
// the worker that closes it read the same deadline and the same backoff.
func newSchedule(cfg config.Config) referencewait.Schedule {
	return referencewait.New(cfg.ReferenceTTL, referencewait.FullJitter)
}

// newReferenceWorker is the first background component of the process. It scans
// the queue of waits and hands each candidate to the use case that decides it.
func newReferenceWorker(cfg config.Config, reads storage.Reads, resolver *resolvereference.Service, pipe *telemetry.Pipeline) *referenceworker.Worker {
	return referenceworker.New(reads, resolver, clock.UTC{}, pipe.Logger, cfg.ReferenceInterval)
}

// newRelay is the use case that moves one committed event out: it claims a row
// under a lease, publishes outside any transaction, and confirms under the token
// of that claim.
func newRelay(cfg config.Config, queue storage.OutboxQueue, topic *broker.Topic, pipe *telemetry.Pipeline) *relayoutbox.Service {
	return relayoutbox.New(queue, topic, outboxrelay.Sending(pipe.Tracer), clock.UTC{}, pipe.Logger, cfg.OutboxLease)
}

// newOutboxRelay is the second background component of the process. It scans the
// publication queue and hands each candidate to the use case that relays it.
func newOutboxRelay(cfg config.Config, queue storage.OutboxQueue, relay *relayoutbox.Service, pipe *telemetry.Pipeline) *outboxrelay.Relay {
	return outboxrelay.New(queue, relay, pipe.Logger, cfg.OutboxInterval)
}

// newReceiver is the use case of the ingress: it authorizes the message by the
// identity the broker observed and hands the operation to the submission, which
// records the message in the very commit that moves the balance.
func newReceiver(senders *authz.Senders, submitter *submitwager.Service) *receivewager.Service {
	return receivewager.New(senders, submitter)
}

func newQueueReporter(pipe *telemetry.Pipeline, series *metrics.Settlement) *wagerqueue.Reporter {
	return wagerqueue.NewReporter(pipe.Logger, pipe.Tracer, series)
}

// newSettlementMetrics registers every business series on the registry of the
// process, which is the one /metrics serves beside the latency and the runtime.
// The saturation of the pool is registered beside them: it is read at scrape
// time off the pool the process shares, and needs no turn to move it.
func newSettlementMetrics(reg *prometheus.Registry, pool *postgres.Pool) *metrics.Settlement {
	reg.MustRegister(postgres.NewPoolStats(pool))
	return metrics.New(reg)
}

// newConsumer is the third background component of the process. It polls the
// ingress queue and hands each message to the use case that decides it.
func newConsumer(cfg config.Config, ingress *broker.Ingress, receiver *receivewager.Service, reporter *wagerqueue.Reporter) *wagerqueue.Consumer {
	return wagerqueue.NewConsumer(ingress, receiver, reporter, wagerqueue.Timing{
		Poll:       cfg.QueuePoll,
		Visibility: cfg.QueueVisibility,
		Timeout:    cfg.QueueTimeout,
	})
}

func newOutboxQueue(pool *postgres.Pool) storage.OutboxQueue {
	return postgres.NewOutboxQueue(pool)
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

func newSenders(cfg config.Config) (*authz.Senders, error) {
	return authz.LoadSenders(cfg.SendersPath)
}

func newGuard(cfg config.Config, clients *authz.Clients) *authz.Guard {
	return authz.NewGuard(authz.NewVerifier(cfg), clients)
}

func newReporter(pipe *telemetry.Pipeline) *walletapi.Reporter {
	return walletapi.NewReporter(pipe.Logger)
}

func newWagerReporter(pipe *telemetry.Pipeline, series *metrics.Settlement) *wagerapi.Reporter {
	return wagerapi.NewReporter(pipe.Logger, series)
}

func newServer(cfg config.Config, pipe *telemetry.Pipeline) *httpapi.Server {
	return httpapi.NewServer(cfg, http.NewServeMux(), pipe.Logger)
}

// wiring is what the routes need, gathered so that register keeps one parameter
// per component group instead of a dozen.
type wiring struct {
	fx.In

	Config        config.Config
	Pipeline      *telemetry.Pipeline
	Postgres      *postgres.Pool
	Queue         *probe.Queue
	Ready         *httpapi.Ready
	Registry      *prometheus.Registry
	Latency       *prometheus.HistogramVec
	Server        *httpapi.Server
	Guard         *authz.Guard
	Senders       *authz.Senders
	Opener        *openwallet.Service
	Reader        *readwallet.Service
	Lister        *listledger.Service
	Reconciler    *reconcilewallet.Service
	Reporter      *walletapi.Reporter
	Submitter     *submitwager.Service
	WagerReader   *readwager.Service
	WagerReporter *wagerapi.Reporter
	Reference     *referenceworker.Worker
	Topic         *broker.Topic
	Outbox        *outboxrelay.Relay
	Ingress       *broker.Ingress
	Consumer      *wagerqueue.Consumer
}

// The share each stop of the lifecycle has of the shutdown budget.
//
// Fx has no per-hook deadline. Its lifecycle hands every hook the one context of
// the Stop and returns the moment that context expires, skipping every hook it had
// not reached yet — so a stop that waits out the whole budget does not merely run
// late, it takes the stops behind it with it. A share per hook is that missing
// deadline, and it lives here rather than inside each component because the
// arithmetic belongs to the lifecycle: no component knows the total or how many
// stops come after it.
//
// The server is the one that holds a request in flight. The three background
// components only stop claiming or fetching, which is a channel close and the turn
// in hand. The consumer is the exception and it is not a number written here: it
// answers its own budget, so the share cannot drift from the two values that decide
// it.
const (
	serverShare   = 4 * time.Second
	claimantShare = time.Second
	// claimants is how many stops take the claimant share: the pool, the reference
	// worker and the outbox relay. A fourth one added below has to be counted here,
	// and the sum is what the budget is checked against.
	claimants = 3
)

// shutdownBudget refuses a budget that cannot pay every share.
//
// Without this the shares are arithmetic nobody checks: the budget is configuration
// and the shares are code, they are moved in different commits, and a budget under
// their sum fails the way it failed before — the last stops are skipped, in
// silence, in exactly the shutdown that had something to report.
func shutdownBudget(total, consumer time.Duration) error {
	if want := serverShare + consumer + claimants*claimantShare; total < want {
		return fmt.Errorf("shutdown budget of %s: want at least %s, the sum of the shares of the lifecycle", total, want)
	}
	return nil
}

// within gives one stop a deadline of its own inside the budget of the shutdown.
func within(share time.Duration, stop func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		bounded, done := context.WithTimeout(ctx, share)
		defer done()
		return stop(bounded)
	}
}

func register(lc fx.Lifecycle, parts wiring) {
	lc.Append(fx.Hook{OnStart: func(context.Context) error { return parts.Config.Validate() }})
	lc.Append(fx.Hook{OnStart: func(context.Context) error {
		return shutdownBudget(parts.Config.ShutdownTimeout, parts.Consumer.StopBudget())
	}})
	lc.Append(fx.Hook{OnStart: parts.Postgres.Open, OnStop: within(claimantShare, parts.Postgres.Close)})
	lc.Append(fx.Hook{OnStart: parts.Queue.Open})
	lc.Append(fx.Hook{OnStart: parts.Topic.Open})
	lc.Append(fx.Hook{OnStart: parts.Ingress.Open})
	// The three background components come up after the pool and before the
	// listener, so the shutdown takes them in the other order: the port stops
	// accepting first, and each of them stops claiming or fetching after it, all
	// inside the same deadline.
	//
	// None of the three holds the startup back over an empty queue: a process that
	// has nothing to do yet still has to answer the port.
	lc.Append(fx.Hook{OnStart: parts.Reference.Start, OnStop: within(claimantShare, parts.Reference.Stop)})
	lc.Append(fx.Hook{OnStart: parts.Outbox.Start, OnStop: within(claimantShare, parts.Outbox.Stop)})
	lc.Append(fx.Hook{
		OnStart: parts.Consumer.Start,
		OnStop:  within(parts.Consumer.StopBudget(), parts.Consumer.Stop),
	})
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			parts.Server.Use(routes(parts))
			return parts.Server.Start(ctx)
		},
		OnStop: within(serverShare, parts.Server.Shutdown),
	})
}

func routes(parts wiring) http.Handler {
	return httpapi.Handler(httpapi.Routes{
		Live:    http.HandlerFunc(httpapi.Live),
		Ready:   parts.Ready,
		Metrics: httpapi.MetricsHandler(parts.Registry),
		// Only the internal wallet client opens, reads, lists and reconciles a
		// wallet. A provider sends wagers and never touches these four routes.
		OpenWallet:      parts.Guard.Only(authz.InternalWallet, walletapi.Open(parts.Opener, parts.Reporter)),
		ReadWallet:      parts.Guard.Only(authz.InternalWallet, walletapi.Read(parts.Reader, parts.Reporter)),
		ListLedger:      parts.Guard.Only(authz.InternalWallet, walletapi.ListLedger(parts.Lister, parts.Reporter)),
		ReconcileWallet: parts.Guard.Only(authz.InternalWallet, walletapi.Reconcile(parts.Reconciler, parts.Reporter)),
		// Only a provider sends a wager and reads its own transaction. The client
		// of the token decides it, and the guard hands that client to the border,
		// which checks the provider of the body against it.
		SubmitWager:     parts.Guard.Only(authz.Provider, wagerapi.Submit(parts.Submitter, parts.WagerReporter)),
		ReadTransaction: parts.Guard.Only(authz.Provider, wagerapi.Read(parts.WagerReader, parts.WagerReporter)),
		Logger:          parts.Pipeline.Logger,
		Tracer:          parts.Pipeline.Tracer,
		Latency:         parts.Latency,
	})
}
