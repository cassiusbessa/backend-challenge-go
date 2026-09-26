package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/app/resolvereference"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/platform/authz"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/divergencewatch"
	"github.com/junglegaming/backend-challenge-go/internal/platform/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
	"github.com/junglegaming/backend-challenge-go/internal/platform/outboxrelay"
	"github.com/junglegaming/backend-challenge-go/internal/platform/referenceworker"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
	"github.com/junglegaming/backend-challenge-go/internal/platform/wagerqueue"
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
	for _, key := range []string{"IDP_ISSUER", "CLIENTS_PATH", "QUEUE_SENDERS_PATH"} {
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
	err = New(cfg, owned(t, cfg)).Start(ctx)
	if !errors.Is(err, authz.ErrUnreadableClientMap) {
		t.Fatalf("start with no client map = %v, want %v", err, authz.ErrUnreadableClientMap)
	}
}

// A process that cannot say who may send must not open the port and must not
// consume from the queue: a sender map it cannot read would refuse every message
// as an unmapped sender, which looks exactly like a queue of nothing but junk.
func TestNew_refusesToStartWithAnUnreadableSenderMap(t *testing.T) {
	t.Parallel()
	cfg := loaded(t)
	cfg.SendersPath = filepath.Join(t.TempDir(), "absent.yaml")
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	if err := New(cfg, owned(t, cfg)).Start(ctx); !errors.Is(err, authz.ErrUnreadableSenderMap) {
		t.Fatalf("start = %v, want %v", err, authz.ErrUnreadableSenderMap)
	}
}

// The relay comes up beside the reference worker and holds nothing back: an
// outbox with nothing in it is the ordinary state of a process that is keeping
// up, and the port has to answer whether or not there is anything to publish.
func TestNew_comesUpWithTheRelayBesideTheReferenceWorker(t *testing.T) {
	t.Parallel()
	cfg := loaded(t)
	got := make(chan *outboxrelay.Relay, 1)
	application := New(cfg, owned(t, cfg), fx.Invoke(func(relay *outboxrelay.Relay) { got <- relay }))
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatalf("Start over an empty outbox = %v, want nil", err)
	}
	if relay := <-got; relay == nil {
		t.Fatalf("relay in the graph = %v, want one beside the reference worker", relay)
	}
	stopping, release := context.WithTimeout(context.Background(), stepWait)
	defer release()
	if err := application.Stop(stopping); err != nil {
		t.Fatalf("Stop after the relay came up = %v, want nil", err)
	}
}

// The consumer comes up beside the other two background components and holds
// nothing back: an ingress queue with nothing in it is the ordinary state of a
// process that is keeping up, and the port has to answer whether or not there is
// a message to decide.
//
// The case reaches the assembled consumer through the graph, so a constructor
// this wiring forgot would fail here rather than at runtime.
func TestNew_comesUpWithTheConsumerBesideTheOtherTwoBackgroundComponents(t *testing.T) {
	t.Parallel()
	cfg := loaded(t)
	got := make(chan *wagerqueue.Consumer, 1)
	application := New(cfg, owned(t, cfg), fx.Invoke(func(consumer *wagerqueue.Consumer) { got <- consumer }))
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatalf("Start over an empty ingress queue = %v, want nil", err)
	}
	if consumer := <-got; consumer == nil {
		t.Fatalf("consumer in the graph = %v, want one beside the other two", consumer)
	}
	stopping, release := context.WithTimeout(context.Background(), stepWait)
	defer release()
	if err := application.Stop(stopping); err != nil {
		t.Fatalf("Stop after the consumer came up = %v, want nil", err)
	}
}

// The ingress queue is what the consumer fetches from, so a configuration that
// cannot name it stops the whole process rather than coming up with a consumer
// that has nowhere to look.
func TestNew_refusesToStartWithoutTheAddressOfTheIngressQueue(t *testing.T) {
	t.Parallel()
	cfg := loaded(t)
	cfg.SQSQueueURL = ""
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	var missing config.MissingError
	if err := New(cfg, owned(t, cfg)).Start(ctx); !errors.As(err, &missing) || missing.Key != "SQS_QUEUE_URL" {
		t.Fatalf("Start with no ingress address = %v, want MissingError on SQS_QUEUE_URL", err)
	}
}

// The topic is what the relay publishes to, so a configuration that cannot name
// it stops the whole process rather than coming up with a relay that has
// nowhere to send.
func TestNew_refusesToStartWithoutTheAddressOfTheTopic(t *testing.T) {
	t.Parallel()
	cfg := loaded(t)
	cfg.SNSTopicARN = ""
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	var missing config.MissingError
	if err := New(cfg, owned(t, cfg)).Start(ctx); !errors.As(err, &missing) || missing.Key != "SNS_TOPIC_ARN" {
		t.Fatalf("Start with no topic address = %v, want MissingError on SNS_TOPIC_ARN", err)
	}
}

func TestSIGTERMStopsNewConnectionsAndExitsSuccessfully(t *testing.T) {
	cfg := loaded(t)
	gate := &hold{entered: make(chan struct{}), release: make(chan struct{})}
	gotSrv := make(chan *httpapi.Server, 1)
	// The pipeline comes out of the graph at build time, the way Boot takes it: Run
	// flushes it after the lifecycle and needs it in hand before anything starts.
	var pipe *telemetry.Pipeline
	application := New(cfg,
		fx.Replace(fx.Annotate(gate, fx.As(new(httpapi.PostgresChecker)))),
		fx.Replace(fx.Annotate(okCheck{}, fx.As(new(httpapi.QueueChecker)))),
		fx.Invoke(func(srv *httpapi.Server) { gotSrv <- srv }),
		fx.Populate(&pipe),
	)
	sigs := make(chan os.Signal, 1)
	errCh := make(chan error, 1)
	go func() { errCh <- Run(application, pipe, sigs, cfg.ShutdownTimeout) }()
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
	assertNotFlushed(t, pipe)
	close(gate.release)
	if err := waitExit(t, errCh); err != nil {
		t.Fatalf("exit = %v, want nil", err)
	}
	// The lifecycle flushes the buffer on the way out, so a process that exited
	// cleanly leaves the pipeline stopped.
	waitCh(t, pipe.Stopped())
}

// The Fx lifecycle returns the moment the shared budget expires and skips every
// stop it had not reached yet, so a flush registered as one of them is lost in
// exactly the shutdown that had something to report. It is not one of them.
func TestStop_flushesTheTelemetryWhenTheLifecycleOverranItsBudget(t *testing.T) {
	cfg := loaded(t)
	var pipe *telemetry.Pipeline
	application := New(cfg,
		fx.Replace(fx.Annotate(okCheck{}, fx.As(new(httpapi.PostgresChecker)))),
		fx.Replace(fx.Annotate(okCheck{}, fx.As(new(httpapi.QueueChecker)))),
		fx.Populate(&pipe),
		// Appended after the lifecycle of the process, so on the way out it is the
		// first stop taken and it spends the whole budget.
		fx.Invoke(func(lc fx.Lifecycle) {
			lc.Append(fx.Hook{OnStop: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			}})
		}),
	)
	starting, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	if err := application.Start(starting); err != nil {
		t.Fatalf("Start of the overrun case = %v, want nil", err)
	}
	// The stops the overrun skipped are still started. Taking them down afterwards
	// keeps this case from leaving a listener and two workers behind for the rest of
	// the binary; the lifecycle carries on from where the budget cut it.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), stepWait)
		defer cancel()
		_ = application.Stop(ctx)
	})
	if err := stop(application, pipe, overrunBudget); err == nil {
		t.Fatalf("stop over a lifecycle that overran = nil, want the deadline of the budget")
	}
	waitCh(t, pipe.Stopped())
}

// A budget under the sum of the shares is refused at startup, before the port
// opens: the alternative is discovering it at the one shutdown that had something
// to report, when the last stops are skipped in silence.
//
// The four claimants are the pool, the reference worker, the outbox relay and
// the divergence watcher, and the sum is written out here so that a fifth one
// added to the lifecycle has to be counted in both places.
func TestShutdownBudget_refusesABudgetThatCannotPayEveryShare(t *testing.T) {
	t.Parallel()
	consumer := 10 * time.Second
	want := serverShare + consumer + 4*claimantShare
	if err := shutdownBudget(want-time.Millisecond, consumer); err == nil {
		t.Fatalf("shutdownBudget just under the sum of the shares = nil, want a refusal")
	}
	if err := shutdownBudget(want, consumer); err != nil {
		t.Fatalf("shutdownBudget on the sum of the shares = %v, want nil", err)
	}
}

// The watcher comes up beside the other three background components and holds
// nothing back: a table with no wallet in it is the ordinary state of a fresh
// process, and the port has to answer whether or not there is anything to
// check. The case reaches the assembled watcher through the graph, so a
// constructor this wiring forgot would fail here rather than at runtime.
func TestNew_comesUpWithTheDivergenceWatcherBesideTheOtherThree(t *testing.T) {
	t.Parallel()
	cfg := loaded(t)
	got := make(chan *divergencewatch.Worker, 1)
	application := New(cfg, owned(t, cfg), fx.Invoke(func(watcher *divergencewatch.Worker) { got <- watcher }))
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatalf("Start over an empty table = %v, want nil", err)
	}
	if watcher := <-got; watcher == nil {
		t.Fatalf("watcher in the graph = %v, want one beside the other three", watcher)
	}
	stopping, release := context.WithTimeout(context.Background(), stepWait)
	defer release()
	if err := application.Stop(stopping); err != nil {
		t.Fatalf("Stop after the watcher came up = %v, want nil", err)
	}
}

// A batch that is not a positive integer blocks the listener the way every
// invalid configuration does, and the watcher with it.
func TestNew_refusesToStartWithABatchBelowOne(t *testing.T) {
	t.Parallel()
	cfg := loaded(t)
	cfg.ReconciliationBatch = 0
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	var invalid config.InvalidError
	if err := New(cfg, owned(t, cfg)).Start(ctx); !errors.As(err, &invalid) || invalid.Key != "RECONCILIATION_BATCH" {
		t.Fatalf("Start with a batch of zero = %v, want InvalidError on RECONCILIATION_BATCH", err)
	}
}

// overrunBudget is short enough that the blocking stop spends it at once. The
// budget the process comes up with is validated against the shares; this one is
// handed straight to stop, which is the seam the case is about.
const overrunBudget = 50 * time.Millisecond

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

// loaded is the configuration of this suite, pointed at addresses nothing
// answers on: every case here is about the graph and the lifecycle, not about
// the services behind them.
func loaded(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(testEnv)
	if err != nil {
		t.Fatalf("load config of the test environment = %v, want nil", err)
	}
	return cfg
}

func testEnv(key string) string {
	values := map[string]string{
		"HTTP_ADDR":                   "127.0.0.1:0",
		"DATABASE_URL":                "postgres://junglegaming:junglegaming@127.0.0.1:1/junglegaming?sslmode=disable",
		"SQS_ENDPOINT":                "http://127.0.0.1:1",
		"SQS_QUEUE_URL":               "http://127.0.0.1:1/000000000000/wager-transactions.fifo",
		"SQS_DLQ_URL":                 "http://127.0.0.1:1/000000000000/wager-transactions-dlq.fifo",
		"SNS_ENDPOINT":                "http://127.0.0.1:1",
		"SNS_TOPIC_ARN":               "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "127.0.0.1:1",
		"IDP_ISSUER":                  "http://127.0.0.1:1/realms/junglegaming",
		"CLIENTS_PATH":                clientsPath,
		"QUEUE_SENDERS_PATH":          sendersPath,
		// The budget is a ceiling and not a wait: it has to pay every share of the
		// lifecycle, and the shutdown of a case still finishes the moment the last
		// stop returns.
		"SHUTDOWN_TIMEOUT": "20s",
		"PPROF_ADDR":       "127.0.0.1:0",
	}
	return values[key]
}

// The two versioned maps the challenge ships. The graph loads both on
// construction, so a run without either would not reach the lifecycle at all.
const (
	clientsPath = moduleRoot + "/deploy/local/clients.yaml"
	sendersPath = moduleRoot + "/deploy/local/queue-senders.yaml"
)

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

// The schedule of the wait is built once and shared by the use case that writes
// one and the worker that closes it, so the TTL it measures has to be the one the
// configuration names.
func TestNewSchedule_measuresTheWaitWithTheConfiguredTTL(t *testing.T) {
	t.Parallel()
	entered := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	schedule := newSchedule(config.Config{ReferenceTTL: 30 * time.Second})
	if got := schedule.DeadlineAt(entered); !got.Equal(entered.Add(30 * time.Second)) {
		t.Fatalf("deadline = %s, want the entry plus the configured 30s", got)
	}
	next := schedule.NextAttemptAt(0, entered, schedule.DeadlineAt(entered))
	if next.Before(entered) || next.After(schedule.DeadlineAt(entered)) {
		t.Fatalf("next attempt = %s, want an instant inside the wait", next)
	}
}

// The worker is assembled from the read side, the use case and the logger of the
// process, and what it comes back as is a component that has not started: the
// lifecycle is what starts it.
func TestNewReferenceWorker_answersAWorkerTheLifecycleStarts(t *testing.T) {
	t.Parallel()
	cfg := config.Config{ReferenceInterval: time.Millisecond}
	pipe := &telemetry.Pipeline{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	// The stop of a worker that never started is the whole assertion: it reaches
	// the assembled worker, so a constructor that answered nothing would not get
	// this far.
	worker := newReferenceWorker(cfg, emptyReads{}, resolvereference.New(nil, nil, nil, nil), pipe, metrics.New(prometheus.NewRegistry()))
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop of a worker the lifecycle never started = %v, want nil", err)
	}
}

// emptyReads is the read side of a process with nothing waiting, which is all the
// assembled worker is asked for here.
type emptyReads struct {
	storage.Reads
}

// Run reaches flushed on both of its exits, and the one that failed to start
// hands it whatever Populate managed to fill — which is nothing when the graph
// itself is what broke.
func TestFlushed_answersNilForAGraphThatBuiltNoPipeline(t *testing.T) {
	t.Parallel()
	if err := flushed(nil); err != nil {
		t.Fatalf("flush with no pipeline in hand = %v, want nil", err)
	}
}

func TestFlushed_stopsThePipelineItWasHanded(t *testing.T) {
	t.Parallel()
	pipe, err := telemetry.Started(t.Context(), loaded(t))
	if err != nil {
		t.Fatalf("telemetry for the flush = %v, want a started pipeline", err)
	}
	if err := flushed(pipe); err != nil {
		t.Fatalf("flush of a pipeline in hand = %v, want nil", err)
	}
	waitCh(t, pipe.Stopped())
}

// newPipeline is what the graph provides, so what every constructor copies from
// has to come back exporting rather than merely built.
func TestNewPipeline_answersAPipelineAlreadyExporting(t *testing.T) {
	t.Parallel()
	pipe, err := newPipeline(loaded(t))
	if err != nil {
		t.Fatalf("newPipeline = %v, want a started pipeline", err)
	}
	t.Cleanup(func() { _ = flushed(pipe) })
	if !pipe.Exporting() {
		t.Fatalf("pipeline straight from the provider exporting = %t, want true", pipe.Exporting())
	}
}

// owned hands the graph a pipeline this case stops, instead of the one New
// builds for itself. A graph that fails to build leaves no hook behind to stop
// the exporters its construction already installed, and each one costs the test
// binary a batch processor and a gRPC client for the rest of the run.
func owned(t *testing.T, cfg config.Config) fx.Option {
	t.Helper()
	pipe, err := telemetry.Started(t.Context(), cfg)
	if err != nil {
		t.Fatalf("telemetry of the case = %v, want a started pipeline", err)
	}
	t.Cleanup(func() { _ = flushed(pipe) })
	return fx.Replace(pipe)
}

// newRelay and newQueueReporter copy pipe.Tracer while the graph is built, so
// the pipeline has to be started by the time they run: the provider NewPipeline
// hands out carries no exporter, and a span created on it never leaves the
// process. Exporting is what tells a pipeline that was started from one whose
// pair was merely never replaced — without it the case passes on both.
func TestNew_handsTheBackgroundWorkTheTelemetryThatStartInstalls(t *testing.T) {
	t.Parallel()
	cfg := loaded(t)
	handedOut := make(chan trace.Tracer, 1)
	handedLog := make(chan *slog.Logger, 1)
	var pipe *telemetry.Pipeline
	application := New(cfg,
		fx.Populate(&pipe),
		fx.Invoke(func(p *telemetry.Pipeline, _ *outboxrelay.Relay, _ *wagerqueue.Reporter, _ *referenceworker.Worker) {
			handedOut <- p.Tracer
			handedLog <- p.Logger
		}),
	)
	ctx, cancel := context.WithTimeout(context.Background(), stepWait)
	defer cancel()
	if err := application.Start(ctx); err != nil {
		t.Fatalf("Start for the telemetry handover = %v, want nil", err)
	}
	stopping, release := context.WithTimeout(context.Background(), stepWait)
	defer release()
	defer func() { _ = flushed(pipe) }()
	defer func() { _ = application.Stop(stopping) }()
	if !pipe.Exporting() {
		t.Fatalf("pipeline of the graph exporting = %t, want true", pipe.Exporting())
	}
	atBuild := <-handedOut
	if logAtBuild := <-handedLog; logAtBuild != pipe.Logger {
		t.Fatalf("logger given to the background work = %p, want the one Start installed, %p", logAtBuild, pipe.Logger)
	}
	if atBuild != pipe.Tracer {
		t.Fatalf("tracer given to the background work = %p, want the one Start installed, %p", atBuild, pipe.Tracer)
	}
}
