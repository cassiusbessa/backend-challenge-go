package outboxrelay

import (
	"context"
	"errors"
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
const tick = time.Millisecond

func TestTurn_handsEveryCandidateOfAScanToTheUseCase(t *testing.T) {
	t.Parallel()
	scanner := &queue{due: candidates(t, 3)}
	relayer := &sends{}
	New(scanner, relayer, quiet(), tick).turn(context.Background(), context.Background())
	if len(relayer.seen) != 3 {
		t.Fatalf("candidates relayed = %d, want the 3 the scan chose", len(relayer.seen))
	}
	if scanner.limit != batch {
		t.Fatalf("limit asked = %d, want the batch of %d", scanner.limit, batch)
	}
}

// The signal reaching the relay mid-batch claims nothing more: what is left is
// for whoever scans next, here or in another replica.
func TestTurn_claimsNoNewRowOnceTheContextIsDone(t *testing.T) {
	t.Parallel()
	relayer := &sends{}
	relay := New(&queue{due: candidates(t, 3)}, relayer, quiet(), tick)
	signalled, stop := context.WithCancel(context.Background())
	relayer.hold = func(context.Context) { stop() }
	relay.turn(signalled, context.Background())
	if len(relayer.seen) != 1 {
		t.Fatalf("rows claimed mid-batch = %d, want the 1 already in flight when the signal came", len(relayer.seen))
	}
}

// The signal stops the run, and nothing is claimed from there on. What it does
// not do is reach the send in flight: go-outbox gives that one the lease to
// finish in, and the turn ends because it ended, not because it was cut.
func TestStop_claimsNoNewRowAfterTheSignal(t *testing.T) {
	t.Parallel()
	relayer := &sends{}
	relay := New(&queue{due: candidates(t, 3)}, relayer, quiet(), tick)
	stopped := make(chan error, 1)
	relayer.hold = func(ctx context.Context) {
		// The stop takes the values of the turn without its cancellation, because
		// a test that handed its own cancellation in could not tell the two apart.
		stopping := context.WithoutCancel(ctx)
		go func() { stopped <- relay.Stop(stopping) }()
		// The signal has landed by the time this reads: what is asserted next is
		// the state of the send while the process is already stopping.
		<-relay.claiming
		if err := ctx.Err(); err != nil {
			t.Errorf("context of the send in flight = %v, want the signal not to have touched it", err)
		}
	}
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start before the signal = %v, want nil", err)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("Stop while a turn was in flight = %v, want nil", err)
	}
	if len(relayer.seen) != 1 {
		t.Fatalf("rows claimed = %d, want the 1 already in flight when the signal came", len(relayer.seen))
	}
}

// The signal cancels the scan, which the send it may be holding is not. A scan
// cut short loses nothing — whoever scans next reads the same queue — while a
// shutdown that waited on a database that is out would be a process that never
// leaves.
func TestStop_cancelsTheScanAtTheSignal(t *testing.T) {
	t.Parallel()
	scanner := &queue{blocks: true, scanned: make(chan struct{}, 1)}
	relay := New(scanner, &sends{}, quiet(), tick)
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start before the scan that does not answer = %v, want nil", err)
	}
	<-scanner.scanned
	// The context of the stop carries no deadline: what has to end the wait is
	// the scan answering the signal, not a deadline running out over it.
	if err := relay.Stop(context.Background()); err != nil {
		t.Fatalf("Stop over a scan that does not answer = %v, want nil", err)
	}
}

// An outbox with nothing in it must not hold the process back: the start answers
// at once and the ticker is what scans.
func TestStart_comesUpOverAnEmptyOutboxAndScansOnTheTicker(t *testing.T) {
	t.Parallel()
	scanner := &queue{scanned: make(chan struct{}, 4)}
	relay := New(scanner, &sends{}, quiet(), tick)
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start over an empty outbox = %v, want nil", err)
	}
	<-scanner.scanned
	if err := relay.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after an empty outbox = %v, want nil", err)
	}
}

func TestStop_answersNilForARelayThatNeverStarted(t *testing.T) {
	t.Parallel()
	if err := New(&queue{}, &sends{}, quiet(), tick).Stop(context.Background()); err != nil {
		t.Fatalf("Stop of a relay that never started = %v, want nil", err)
	}
}

// The shutdown deadline coming first is answered as the failure it is: what the
// turn in flight did not confirm is a row that is still publishable.
func TestStop_answersTheFailureWhenTheShutdownDeadlineComesFirst(t *testing.T) {
	t.Parallel()
	held := make(chan struct{})
	cut := make(chan error, 1)
	relayer := &sends{hold: func(ctx context.Context) {
		close(held)
		// The deadline is the one thing that does cancel the work: the process
		// promised to be gone, and a send still running is no longer its to
		// finish.
		<-ctx.Done()
		cut <- ctx.Err()
	}}
	relay := New(&queue{due: candidates(t, 1)}, relayer, quiet(), tick)
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start before the deadline = %v, want nil", err)
	}
	<-held
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	err := relay.Stop(expired)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop past its deadline = %v, want %v", err, context.Canceled)
	}
	if cutWith := <-cut; !errors.Is(cutWith, context.Canceled) {
		t.Fatalf("context of the send past the deadline = %v, want %v", cutWith, context.Canceled)
	}
}

// One row that could not be relayed does not stop the turn, and the failure is
// logged once, with the identities and the frames of where it was first seen.
func TestPublish_logsTheFailureWithTheIdentitiesOfTheRow(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	relayer := &sends{err: errors.New("postgres: connection reset by peer")}
	relay := New(&queue{}, relayer, slog.New(slog.NewJSONHandler(&written, nil)), tick)
	candidate := candidates(t, 1)[0]
	relay.publish(context.Background(), candidate)
	for _, want := range []string{"relay an outbox event", candidate.EventID.String(), "stack"} {
		if !strings.Contains(written.String(), want) {
			t.Fatalf("log = %q, want %q in it", written.String(), want)
		}
	}
}

// A send that went through is not logged here: whoever decides the outcome of a
// row logs it, and the relay only decides that there was work to do.
func TestPublish_logsNothingForARowThatWasRelayed(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	relay := New(&queue{}, &sends{}, slog.New(slog.NewJSONHandler(&written, nil)), tick)
	relay.publish(context.Background(), candidates(t, 1)[0])
	if written.Len() != 0 {
		t.Fatalf("log = %q, want nothing for a row that was relayed", written.String())
	}
}

// A turn the shutdown cut short is not a failure, and a line per candidate left
// would say it was.
func TestFailed_recordsNothingForATurnTheShutdownCutShort(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	relay := New(&queue{}, &sends{}, slog.New(slog.NewJSONHandler(&written, nil)), tick)
	stopped, stop := context.WithCancel(context.Background())
	stop()
	relay.failed(stopped, "relay an outbox event", context.Canceled)
	if written.Len() != 0 {
		t.Fatalf("log of a cancelled turn = %q, want nothing once the relay was told to stop", written.String())
	}
}

// A failure of the scan ends the turn and relays nothing: the queue is read
// again on the next tick, so a database that comes back needs no restart.
func TestTurn_relaysNothingWhenTheScanFailed(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	relayer := &sends{}
	scanner := &queue{due: candidates(t, 2), failFirst: errors.New("postgres: connection reset by peer")}
	relay := New(scanner, relayer, slog.New(slog.NewJSONHandler(&written, nil)), tick)
	relay.turn(context.Background(), context.Background())
	if len(relayer.seen) != 0 {
		t.Fatalf("rows relayed after a scan that failed = %d, want 0", len(relayer.seen))
	}
	if !strings.Contains(written.String(), "scan the outbox queue") {
		t.Fatalf("log = %q, want the failure of the scan in it", written.String())
	}
	relay.turn(context.Background(), context.Background())
	if len(relayer.seen) != 2 {
		t.Fatalf("rows relayed on the turn after = %d, want 2", len(relayer.seen))
	}
}

// queue is the scan of one case: what it answers, the limit it was asked for,
// and a first answer that fails.
type queue struct {
	due       []storage.OutboxCandidate
	scans     int
	limit     int
	failFirst error
	scanned   chan struct{}
	blocks    bool
}

func (q *queue) Due(ctx context.Context, limit int) ([]storage.OutboxCandidate, error) {
	q.scans++
	q.limit = limit
	if q.scanned != nil {
		select {
		case q.scanned <- struct{}{}:
		default:
		}
	}
	if q.blocks {
		// A database that does not answer. The adapter comes back with the error
		// of the context, and a fake that returned at once would not be a scan
		// the shutdown has to get out of.
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if q.failFirst != nil && q.scans == 1 {
		return nil, q.failFirst
	}
	return q.due, nil
}

// sends is the use case of one case: the candidates it was handed, a failure to
// answer, and a hold that lets a case act while a turn is in flight.
type sends struct {
	seen []storage.OutboxCandidate
	err  error
	hold func(context.Context)
}

func (s *sends) Relay(ctx context.Context, candidate storage.OutboxCandidate) error {
	s.seen = append(s.seen, candidate)
	if s.hold != nil {
		s.hold(ctx)
	}
	return s.err
}

func quiet() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&strings.Builder{}, nil))
}

func candidates(t *testing.T, count int) []storage.OutboxCandidate {
	t.Helper()
	ids := []string{
		"019974a4-0000-7000-8000-00000000e001",
		"019974a4-0000-7000-8000-00000000e002",
		"019974a4-0000-7000-8000-00000000e003",
	}
	wallet, err := identity.ParseWalletID("019974a4-0000-7000-8000-00000000a11e")
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	var chosen []storage.OutboxCandidate
	for _, text := range ids[:count] {
		id, err := identity.ParseEventID(text)
		if err != nil {
			t.Fatalf("ParseEventID = %v, want nil", err)
		}
		chosen = append(chosen, storage.OutboxCandidate{EventID: id, WalletID: wallet})
	}
	return chosen
}

// A failure that already crossed an I/O boundary carries the frames of where it
// was first seen, and this border does not capture them again: one failure, one
// stack.
func TestStackOf_keepsTheFramesTheChainAlreadyCarries(t *testing.T) {
	t.Parallel()
	seen := fault.Wrap("claim outbox event", errors.New("connection reset by peer"))
	if got := stackOf("relay an outbox event", seen); !slices.Equal(got, fault.Stack(seen)) {
		t.Fatalf("frames = %v, want the %v the chain already carried", got, fault.Stack(seen))
	}
}

// A refusal the use case decided on its own crossed no boundary, so its chain
// holds no fault and this border is where the frames are captured.
func TestStackOf_capturesTheFramesWhenTheChainCarriesNone(t *testing.T) {
	t.Parallel()
	got := stackOf("relay an outbox event", errors.New("kind is not published by this relay"))
	if len(got) == 0 {
		t.Fatalf("frames = %v, want them captured at this border", got)
	}
	if !strings.Contains(got[0], "outboxrelay.") {
		t.Fatalf("first frame = %q, want it captured in this package", got[0])
	}
}
