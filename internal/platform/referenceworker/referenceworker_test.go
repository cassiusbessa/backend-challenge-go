package referenceworker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// tick is the interval the cases about the lifecycle run on. It is short because
// none of them waits for it: each is driven by the channel the fake writes to.
// The cases about one turn call that turn and never start a ticker at all.
const tick = time.Millisecond

// One turn is one scan and the candidates it chose, handed over in the order the
// scan answered them.
func TestTurn_handsEveryCandidateOfAScanToTheUseCase(t *testing.T) {
	t.Parallel()
	scanner := &queue{due: candidates(t, 3)}
	resolver := &decisions{}
	worker := New(scanner, resolver, frozen{}, quiet(), tick)
	worker.turn(context.Background())
	if len(resolver.seen) != 3 {
		t.Fatalf("candidates decided = %d, want the 3 the scan chose", len(resolver.seen))
	}
	for at, candidate := range resolver.seen {
		if candidate != scanner.due[at] {
			t.Fatalf("candidate %d = %v, want %v", at, candidate, scanner.due[at])
		}
	}
}

// The scan is bounded, so a queue that grew while the process was down is worked
// through over several turns instead of one long run.
func TestTurn_asksTheScanForNoMoreThanOneBatch(t *testing.T) {
	t.Parallel()
	scanner := &queue{}
	New(scanner, &decisions{}, frozen{}, quiet(), tick).turn(context.Background())
	if scanner.limit != batch {
		t.Fatalf("limit asked = %d, want the batch of %d", scanner.limit, batch)
	}
}

// A scan that failed ends the turn and decides nothing: the queue is read again
// on the next tick, so a database that comes back needs no restart.
func TestTurn_decidesNothingWhenTheScanFailed(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	resolver := &decisions{}
	scanner := &queue{due: candidates(t, 2), failFirst: errors.New("postgres: connection reset by peer")}
	worker := New(scanner, resolver, frozen{}, slog.New(slog.NewJSONHandler(&written, nil)), tick)
	worker.turn(context.Background())
	if len(resolver.seen) != 0 {
		t.Fatalf("candidates decided after a scan that failed = %d, want 0", len(resolver.seen))
	}
	if !strings.Contains(written.String(), "scan the reference wait queue") {
		t.Fatalf("log = %q, want the failure of the scan in it", written.String())
	}
	worker.turn(context.Background())
	if len(resolver.seen) != 2 {
		t.Fatalf("candidates decided on the turn after = %d, want 2", len(resolver.seen))
	}
}

// The signal reaching the worker mid-batch leaves the rest of the candidates for
// whoever scans next, here or in another replica.
func TestTurn_claimsNoNewWaitOnceTheContextIsDone(t *testing.T) {
	t.Parallel()
	resolver := &decisions{}
	worker := New(&queue{due: candidates(t, 3)}, resolver, frozen{}, quiet(), tick)
	signalled, stop := context.WithCancel(context.Background())
	// The first decision is the one in flight when the signal comes, so what the
	// loop does after it is what the case reads.
	resolver.hold = func(context.Context) { stop() }
	worker.turn(signalled)
	if len(resolver.seen) != 1 {
		t.Fatalf("waits claimed mid-batch = %d, want the 1 already in flight when the signal came", len(resolver.seen))
	}
}

// One wait that could not be decided does not stop the turn, and the failure is
// logged once, with the identities and the frames of where it was first seen.
func TestDecide_logsTheFailureWithTheIdentitiesOfTheWait(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	resolver := &decisions{err: errors.New("postgres: connection reset by peer")}
	worker := New(&queue{}, resolver, frozen{}, slog.New(slog.NewJSONHandler(&written, nil)), tick)
	candidate := candidates(t, 1)[0]
	worker.decide(context.Background(), candidate)
	logged := written.String()
	for _, want := range []string{"resolve a pending reference", candidate.TransactionID.String(), "stack"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log = %q, want %q in it", logged, want)
		}
	}
}

// A decision that went through is not logged here: whoever decides the outcome
// logs it, and the worker only decides that there was work to do.
func TestDecide_logsNothingForAWaitThatWasDecided(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	worker := New(&queue{}, &decisions{}, frozen{}, slog.New(slog.NewJSONHandler(&written, nil)), tick)
	worker.decide(context.Background(), candidates(t, 1)[0])
	if written.Len() != 0 {
		t.Fatalf("log = %q, want nothing for a wait that was decided", written.String())
	}
}

// A turn the shutdown cut short is not a failure: nothing is broken about a
// worker that was told to stop, and a line per candidate left would say it was.
func TestFailed_recordsNothingForATurnTheShutdownCutShort(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	worker := New(&queue{}, &decisions{}, frozen{}, slog.New(slog.NewJSONHandler(&written, nil)), tick)
	stopped, stop := context.WithCancel(context.Background())
	stop()
	cancelled := fmt.Errorf("claim reference wait: %w", context.Canceled)
	worker.failed(stopped, "resolve a pending reference", cancelled)
	if written.Len() != 0 {
		t.Fatalf("log of a cancelled turn = %q, want nothing once the worker was told to stop", written.String())
	}
}

// The shutdown silences the cancellation and nothing else. A failure that merely
// happened to race the signal is often the last one the process can report, and
// reading the context instead of the error would drop exactly that one.
func TestFailed_recordsAFailureThatMerelyRacedTheShutdown(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	worker := New(&queue{}, &decisions{}, frozen{}, slog.New(slog.NewJSONHandler(&written, nil)), tick)
	stopped, stop := context.WithCancel(context.Background())
	stop()
	worker.failed(stopped, "resolve a pending reference", errors.New("connection refused"))
	if !strings.Contains(written.String(), "connection refused") {
		t.Fatalf("log of a failure racing the shutdown = %q, want the chain of the failure in it", written.String())
	}
}

// The chain is what names where the failure came from, and it is the whole reason
// go-errors builds it. A line with frames and no chain cannot tell a connection
// refusal from a lock timeout.
func TestFailed_recordsTheChainAndNotOnlyTheFrames(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	worker := New(&queue{}, &decisions{}, frozen{}, slog.New(slog.NewJSONHandler(&written, nil)), tick)
	refusal := fmt.Errorf("resolve pending reference: %w", errors.New("kind cannot be waiting"))
	worker.failed(context.Background(), "resolve a pending reference", refusal)
	line := written.String()
	if !strings.Contains(line, refusal.Error()) {
		t.Fatalf("log of a refusal = %q, want the chain %q in it", line, refusal.Error())
	}
	// A refusal of the use case crossed no I/O boundary, so its chain carries no
	// fault and this border is where the frames are captured.
	if !strings.Contains(line, "referenceworker.stackOf") {
		t.Fatalf("log of a refusal = %q, want frames captured at this border", line)
	}
}

// A queue with nothing in it must not hold the process back: the start answers at
// once and the ticker is what scans.
func TestStart_comesUpOverAnEmptyQueueAndScansOnTheTicker(t *testing.T) {
	t.Parallel()
	scanner := &queue{scanned: make(chan struct{}, 4)}
	worker := New(scanner, &decisions{}, frozen{}, quiet(), tick)
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start over an empty queue = %v, want nil", err)
	}
	<-scanner.scanned
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after an empty queue = %v, want nil", err)
	}
}

// The signal stops the run, and the stop waits for the turn in flight rather than
// returning while it is still writing.
func TestStop_endsTheRunAndWaitsForTheTurnInFlight(t *testing.T) {
	t.Parallel()
	resolver := &decisions{}
	worker := New(&queue{due: candidates(t, 3)}, resolver, frozen{}, quiet(), tick)
	stopped := make(chan error, 1)
	resolver.hold = func(ctx context.Context) {
		// The stop takes the values of the turn without its cancellation, because
		// cancelling the run is exactly what it is about to do.
		stopping := context.WithoutCancel(ctx)
		go func() { stopped <- worker.Stop(stopping) }()
		<-ctx.Done()
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start before the signal = %v, want nil", err)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("Stop while a turn was in flight = %v, want nil", err)
	}
	if len(resolver.seen) != 1 {
		t.Fatalf("waits claimed = %d, want the 1 already in flight when the signal came", len(resolver.seen))
	}
}

// Stopping a worker that was never started has nothing to end, which is what lets
// a graph that failed before the start still shut down.
func TestStop_answersNilForAWorkerThatNeverStarted(t *testing.T) {
	t.Parallel()
	worker := New(&queue{}, &decisions{}, frozen{}, quiet(), tick)
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop of a worker that never started = %v, want nil", err)
	}
}

// The shutdown deadline coming first is answered as the failure it is: what the
// turn in flight has not committed is rolled back by the database.
func TestStop_answersTheFailureWhenTheShutdownDeadlineComesFirst(t *testing.T) {
	t.Parallel()
	held := make(chan struct{})
	release := make(chan struct{})
	resolver := &decisions{hold: func(context.Context) {
		close(held)
		<-release
	}}
	worker := New(&queue{due: candidates(t, 1)}, resolver, frozen{}, quiet(), tick)
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start before the deadline = %v, want nil", err)
	}
	<-held
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	err := worker.Stop(expired)
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop past its deadline = %v, want %v", err, context.Canceled)
	}
}

// A worker that was only assembled holds no run: the lifecycle is what starts
// one, and until it does there is nothing to stop.
func TestNew_answersAWorkerThatHasNotStarted(t *testing.T) {
	t.Parallel()
	scanner := &queue{due: candidates(t, 1)}
	resolver := &decisions{}
	worker := New(scanner, resolver, frozen{}, quiet(), tick)
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop of a worker that was only assembled = %v, want nil", err)
	}
	if scanner.scans != 0 || len(resolver.seen) != 0 {
		t.Fatalf("scans = %d and decisions = %d, want neither before the lifecycle starts it", scanner.scans, len(resolver.seen))
	}
}

// The run leaves on a context that is already done without taking a turn, and it
// closes what the stop waits on: a worker cancelled before its first tick must
// not leave the shutdown hanging.
func TestRun_leavesOnADoneContextWithoutTakingATurn(t *testing.T) {
	t.Parallel()
	scanner := &queue{due: candidates(t, 1)}
	worker := New(scanner, &decisions{}, frozen{}, quiet(), time.Hour)
	worker.done = make(chan struct{})
	stopped, stop := context.WithCancel(context.Background())
	stop()
	worker.run(stopped)
	<-worker.done
	if scanner.scans != 0 {
		t.Fatalf("scans = %d, want none on a run that was already done", scanner.scans)
	}
}

// queue is the scan of one case: what it answers, the limit it was asked for, and
// a first answer that fails.
type queue struct {
	due       []storage.WaitCandidate
	scans     int
	limit     int
	failFirst error
	// scanned reports each scan to the case about the worker coming up over a
	// queue with nothing in it.
	scanned chan struct{}
}

func (q *queue) DueWaits(_ context.Context, _ time.Time, limit int) ([]storage.WaitCandidate, error) {
	q.scans++
	q.limit = limit
	if q.scanned != nil {
		select {
		case q.scanned <- struct{}{}:
		default:
		}
	}
	if q.failFirst != nil && q.scans == 1 {
		return nil, q.failFirst
	}
	return q.due, nil
}

// decisions is the use case as the worker sees it: what it was handed, what it
// answers, and a hold that runs on the first decision so a case can act while a
// turn is open.
type decisions struct {
	seen []storage.WaitCandidate
	err  error
	hold func(context.Context)
}

func (d *decisions) Resolve(ctx context.Context, candidate storage.WaitCandidate) error {
	d.seen = append(d.seen, candidate)
	if hold := d.hold; hold != nil {
		d.hold = nil
		hold(ctx)
	}
	return d.err
}

// frozen is the clock of a case that is not about an instant: the scan reads it
// and nothing here asserts on what it read.
type frozen struct{}

func (frozen) Now() time.Time {
	return time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
}

func quiet() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func candidates(t *testing.T, count int) []storage.WaitCandidate {
	t.Helper()
	walletID, err := identity.ParseWalletID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	due := make([]storage.WaitCandidate, 0, count)
	for at := range count {
		id, err := identity.ParseTransactionID(transactionIDs[at])
		if err != nil {
			t.Fatalf("ParseTransactionID = %v, want nil", err)
		}
		due = append(due, storage.WaitCandidate{TransactionID: id, WalletID: walletID})
	}
	return due
}

var transactionIDs = []string{
	"33333333-3333-4333-8333-333333333331",
	"33333333-3333-4333-8333-333333333332",
	"33333333-3333-4333-8333-333333333333",
}

// One failure keeps one stack: a chain that already crossed an I/O boundary
// carries the frames of where it was first seen, and this border adds the
// operation without capturing them again.
func TestStackOf_keepsTheFramesTheChainAlreadyCarries(t *testing.T) {
	t.Parallel()
	seen := fault.Wrap("acquire connection", errors.New("connection refused"))
	captured := fault.Stack(seen)
	frames := stackOf("resolve a pending reference", seen)
	if !slices.Equal(frames, captured) {
		t.Fatalf("frames of a chain that carries a stack = %v, want the %v it came with", frames, captured)
	}
}

// A refusal of the use case crossed no I/O boundary, so its chain holds no fault
// and this border is the first place it is seen as a failure: without the capture
// here the line would carry no frames at all.
func TestStackOf_capturesTheFramesOfAChainThatCarriesNone(t *testing.T) {
	t.Parallel()
	frames := stackOf("resolve a pending reference", errors.New("kind cannot be waiting"))
	if len(frames) == 0 {
		t.Fatalf("frames of a chain with no stack = %v, want them captured here", frames)
	}
}
