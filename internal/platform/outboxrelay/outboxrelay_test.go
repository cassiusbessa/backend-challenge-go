package outboxrelay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
)

// tick is the interval the cases about the lifecycle run on. It is short because
// none of them waits for it: each is driven by the channel the fake writes to.
const tick = time.Millisecond

func TestTurn_handsEveryCandidateOfAScanToTheUseCase(t *testing.T) {
	t.Parallel()
	scanner := &queue{due: candidates(t, 3)}
	relayer := &sends{}
	New(scanner, relayer, quiet(), series(), tick).turn(context.Background(), context.Background())
	if len(relayer.seen) != 3 {
		t.Fatalf("candidates relayed = %d, want the 3 the scan chose", len(relayer.seen))
	}
	if scanner.limit != batch {
		t.Fatalf("limit asked = %d, want the batch of %d", scanner.limit, batch)
	}
}

// The signal reaching the relay mid-batch claims nothing more: the sends in
// flight finish, and what is left is for whoever scans next, here or in another
// replica.
func TestTurn_claimsNoNewRowOnceTheContextIsDone(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}, inFlight+2), make(chan struct{})
	relayer := &sends{hold: func(context.Context) {
		started <- struct{}{}
		<-release
	}}
	relay := New(&queue{due: candidates(t, inFlight+2)}, relayer, quiet(), series(), tick)
	signalled, stop := context.WithCancel(context.Background())
	turned := make(chan struct{})
	go func() {
		relay.turn(signalled, context.Background())
		close(turned)
	}()
	for range inFlight {
		<-started
	}
	stop()
	close(release)
	<-turned
	if got := relayer.count(); got != inFlight {
		t.Fatalf("rows claimed mid-batch = %d, want the %d already in flight when the signal came", got, inFlight)
	}
}

// The signal stops the run, and nothing is claimed from there on. What it does
// not do is reach the sends in flight: go-outbox gives them the lease to finish
// in, and the turn ends because they ended, not because they were cut.
func TestStop_claimsNoNewRowAfterTheSignal(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}, inFlight+2), make(chan struct{})
	relayer := &sends{hold: func(ctx context.Context) {
		started <- struct{}{}
		<-release
		if err := ctx.Err(); err != nil {
			t.Errorf("context of a send in flight = %v, want the signal not to have touched it", err)
		}
	}}
	relay := New(&queue{due: candidates(t, inFlight+2)}, relayer, quiet(), series(), tick)
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start before the signal = %v, want nil", err)
	}
	for range inFlight {
		<-started
	}
	stopped := make(chan error, 1)
	go func() { stopped <- relay.Stop(context.Background()) }()
	// The signal has landed by the time this reads: the sends are released
	// while the process is already stopping.
	<-relay.claiming
	close(release)
	if err := <-stopped; err != nil {
		t.Fatalf("Stop while sends were in flight = %v, want nil", err)
	}
	if got := relayer.count(); got != inFlight {
		t.Fatalf("rows claimed = %d, want the %d already in flight when the signal came", got, inFlight)
	}
}

// The rows of one scan belong to different wallets and are sent at once, up to
// the ceiling: sent one at a time, the relay drained far less than a load wrote.
func TestTurn_sendsTheRowsOfOneScanAtOnceUpToTheCeiling(t *testing.T) {
	t.Parallel()
	var active, most atomic.Int32
	var filled sync.Once
	full, release := make(chan struct{}), make(chan struct{})
	relayer := &sends{hold: func(context.Context) {
		now := active.Add(1)
		defer active.Add(-1)
		raise(&most, now)
		if now == inFlight {
			filled.Do(func() { close(full) })
		}
		<-release
	}}
	relay := New(&queue{due: candidates(t, inFlight+3)}, relayer, quiet(), series(), tick)
	turned := make(chan struct{})
	go func() {
		relay.turn(context.Background(), context.Background())
		close(turned)
	}()
	<-full
	close(release)
	<-turned
	if got := most.Load(); got != inFlight {
		t.Fatalf("sends at once = %d, want the ceiling of %d", got, inFlight)
	}
	if got := relayer.count(); got != inFlight+3 {
		t.Fatalf("rows relayed = %d, want every one of the %d the scan chose", got, inFlight+3)
	}
}

// raise keeps in most the highest value it was given.
func raise(most *atomic.Int32, now int32) {
	for {
		seen := most.Load()
		if now <= seen || most.CompareAndSwap(seen, now) {
			return
		}
	}
}

// A row that went through is followed by the next row of the same wallet, asked
// for by the wallet and not by another scan, until the wallet has nothing more
// to claim: a busy wallet otherwise waited a whole scan for each event.
func TestChain_followsTheWalletWhileEachSendGoesThrough(t *testing.T) {
	t.Parallel()
	head := candidates(t, 3)
	for at := range head {
		head[at].WalletID = head[0].WalletID
	}
	scanner := &queue{due: head[:1], following: map[identity.WalletID][]storage.OutboxCandidate{
		head[0].WalletID: {head[1], head[2]},
	}}
	relayer := &sends{}
	New(scanner, relayer, quiet(), series(), tick).turn(context.Background(), context.Background())
	if !slices.Equal(relayer.seen, head) {
		t.Fatalf("rows relayed = %v, want the head and the two behind it, in order: %v", relayer.seen, head)
	}
	if scanner.scans != 1 {
		t.Fatalf("scans = %d, want the one that found the head", scanner.scans)
	}
}

// One wallet keeps its slot for perWallet rows and then gives it back, so a
// wallet that never runs out does not hold the next scan forever.
func TestChain_givesTheSlotBackAfterPerWalletRows(t *testing.T) {
	t.Parallel()
	relayer := &sends{}
	New(&queue{due: candidates(t, 1), endless: true}, relayer, quiet(), series(), tick).turn(context.Background(), context.Background())
	if got := relayer.count(); got != perWallet {
		t.Fatalf("rows of one wallet in a turn = %d, want the ceiling of %d", got, perWallet)
	}
}

// A send that failed ends the chain without asking for the next row: the row is
// still the head of the wallet, and the next scan finds it again.
func TestChain_endsAtASendThatFailed(t *testing.T) {
	t.Parallel()
	scanner := &queue{due: candidates(t, 1), endless: true}
	relayer := &sends{err: errors.New("postgres: connection reset by peer")}
	New(scanner, relayer, quiet(), series(), tick).turn(context.Background(), context.Background())
	if relayer.count() != 1 || scanner.asked != 0 {
		t.Fatalf("after a failed send = %d rows relayed and %d asks, want 1 and none", relayer.count(), scanner.asked)
	}
}

// A read of the next row that fails ends the chain, logged with the wallet.
func TestChain_endsAtAReadOfTheNextRowThatFailed(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	head := candidates(t, 1)
	scanner := &queue{due: head, nextErr: errors.New("postgres: connection reset by peer")}
	relayer := &sends{}
	New(scanner, relayer, slog.New(slog.NewJSONHandler(&written, nil)), series(), tick).turn(context.Background(), context.Background())
	if relayer.count() != 1 {
		t.Fatalf("rows relayed after the read failed = %d, want the head alone", relayer.count())
	}
	for _, want := range []string{"read the next outbox event of a wallet", head[0].WalletID.String()} {
		if !strings.Contains(written.String(), want) {
			t.Fatalf("log = %q, want %q in it", written.String(), want)
		}
	}
}

// The signal ends every chain: the row in flight finishes, and none behind it
// is claimed.
func TestChain_claimsNoRowBehindOnceTheContextIsDone(t *testing.T) {
	t.Parallel()
	scanner := &queue{due: candidates(t, 1), endless: true}
	signalled, stop := context.WithCancel(context.Background())
	relayer := &sends{hold: func(context.Context) { stop() }}
	New(scanner, relayer, quiet(), series(), tick).turn(signalled, context.Background())
	if relayer.count() != 1 || scanner.asked != 0 {
		t.Fatalf("after the signal = %d rows relayed and %d asks, want the 1 in flight and none", relayer.count(), scanner.asked)
	}
}

// A scan that comes back full is followed at once, without the tick, and the
// backlog is measured after each: an outbox that grew faster than a batch per
// interval is worked through in the same turn, and the gauges follow it.
func TestTurn_scansAgainAtOnceWhileTheScanComesBackFull(t *testing.T) {
	t.Parallel()
	all := candidates(t, batch+2)
	scanner := &queue{rounds: [][]storage.OutboxCandidate{all[:batch], all[batch:]}}
	relayer := &sends{}
	New(scanner, relayer, quiet(), series(), time.Hour).turn(context.Background(), context.Background())
	if scanner.scans != 2 || relayer.count() != batch+2 {
		t.Fatalf("scans = %d relaying %d rows, want 2 scans relaying the %d of both", scanner.scans, relayer.count(), batch+2)
	}
	if scanner.measured != 2 {
		t.Fatalf("backlog read %d times in a turn of two scans, want once after each", scanner.measured)
	}
}

// The signal cancels the scan, which the send it may be holding is not. A scan
// cut short loses nothing — whoever scans next reads the same queue — while a
// shutdown that waited on a database that is out would be a process that never
// leaves.
func TestStop_cancelsTheScanAtTheSignal(t *testing.T) {
	t.Parallel()
	scanner := &queue{blocks: true, scanned: make(chan struct{}, 1)}
	relay := New(scanner, &sends{}, quiet(), series(), tick)
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
	relay := New(scanner, &sends{}, quiet(), series(), tick)
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start over an empty outbox = %v, want nil", err)
	}
	<-scanner.scanned
	if err := relay.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after an empty outbox = %v, want nil", err)
	}
}

// A relay that was only assembled holds no run and has scanned nothing: the
// lifecycle is what starts it.
func TestNew_answersARelayThatHasNotStarted(t *testing.T) {
	t.Parallel()
	scanner := &queue{due: candidates(t, 1)}
	relayer := &sends{}
	relay := New(scanner, relayer, quiet(), series(), tick)
	if err := relay.Stop(context.Background()); err != nil {
		t.Fatalf("Stop of a relay that was only assembled = %v, want nil", err)
	}
	if scanner.scans != 0 || len(relayer.seen) != 0 {
		t.Fatalf("scans = %d and relays = %d, want neither before the lifecycle starts it", scanner.scans, len(relayer.seen))
	}
}

// One turn measures the backlog after the candidates it relayed, so the gauges
// read the queue as the turn left it.
func TestTurn_measuresTheBacklogAfterRelayingItsCandidates(t *testing.T) {
	t.Parallel()
	scanner := &queue{due: candidates(t, 2), backlog: storage.Backlog{Pending: 1, OldestAge: time.Second}}
	moved := series()
	New(scanner, &sends{}, quiet(), moved, tick).turn(context.Background(), context.Background())
	if got := testutil.ToFloat64(moved.OutboxPending); got != 1 {
		t.Fatalf("pending events after a turn = %v, want the 1 the backlog answered", got)
	}
}

func TestStop_answersNilForARelayThatNeverStarted(t *testing.T) {
	t.Parallel()
	if err := New(&queue{}, &sends{}, quiet(), series(), tick).Stop(context.Background()); err != nil {
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
	relay := New(&queue{due: candidates(t, 1)}, relayer, quiet(), series(), tick)
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
	relay := New(&queue{}, relayer, slog.New(slog.NewJSONHandler(&written, nil)), series(), tick)
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
	relay := New(&queue{}, &sends{}, slog.New(slog.NewJSONHandler(&written, nil)), series(), tick)
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
	relay := New(&queue{}, &sends{}, slog.New(slog.NewJSONHandler(&written, nil)), series(), tick)
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
	relay := New(scanner, relayer, slog.New(slog.NewJSONHandler(&written, nil)), series(), tick)
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
	due []storage.OutboxCandidate
	// rounds, when set, is what each scan answers in turn, and nothing after.
	rounds   [][]storage.OutboxCandidate
	scans    int
	measured int
	// following answers, per wallet, the rows behind the one just sent, one per
	// ask, and nextErr a read of them that fails. endless answers a next row on
	// every ask.
	mu        sync.Mutex
	following map[identity.WalletID][]storage.OutboxCandidate
	asked     int
	nextErr   error
	endless   bool
	limit     int
	failFirst error
	scanned   chan struct{}
	blocks    bool
	// backlog is what the queue answers when measured, and backlogErr a read of
	// it that fails.
	backlog    storage.Backlog
	backlogErr error
}

func (q *queue) Backlog(context.Context) (storage.Backlog, error) {
	q.measured++
	if q.backlogErr != nil {
		return storage.Backlog{}, q.backlogErr
	}
	return q.backlog, nil
}

func series() *metrics.Settlement {
	return metrics.New(prometheus.NewRegistry())
}

// The backlog is read into the two gauges, and zero on both is what an empty
// queue answers.
func TestMeasure_readsTheBacklogIntoTheTwoGauges(t *testing.T) {
	t.Parallel()
	scanner := &queue{backlog: storage.Backlog{Pending: 3, OldestAge: 45 * time.Second}}
	moved := series()
	relay := New(scanner, &sends{}, quiet(), moved, tick)
	relay.measure(context.Background())
	if got := testutil.ToFloat64(moved.OutboxPending); got != 3 {
		t.Fatalf("pending events = %v, want 3", got)
	}
	if got := testutil.ToFloat64(moved.OutboxOldestAge); got != 45 {
		t.Fatalf("oldest pending age = %v, want 45", got)
	}
	scanner.backlog = storage.Backlog{}
	relay.measure(context.Background())
	if got := testutil.ToFloat64(moved.OutboxPending) + testutil.ToFloat64(moved.OutboxOldestAge); got != 0 {
		t.Fatalf("gauges with nothing pending = %v, want both 0", got)
	}
}

// A read that fails leaves the last values: a gauge that fell to zero because
// the database was out would read as a queue that emptied.
func TestMeasure_keepsTheLastBacklogWhenTheReadFails(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	scanner := &queue{backlog: storage.Backlog{Pending: 3, OldestAge: 45 * time.Second}}
	moved := series()
	relay := New(scanner, &sends{}, slog.New(slog.NewJSONHandler(&written, nil)), moved, tick)
	relay.measure(context.Background())
	scanner.backlog, scanner.backlogErr = storage.Backlog{}, errors.New("postgres: connection reset by peer")
	relay.measure(context.Background())
	if got := testutil.ToFloat64(moved.OutboxPending); got != 3 {
		t.Fatalf("pending events after a read that failed = %v, want the 3 of the last read", got)
	}
	if got := testutil.ToFloat64(moved.OutboxOldestAge); got != 45 {
		t.Fatalf("oldest pending age after a read that failed = %v, want the 45 of the last read", got)
	}
	if !strings.Contains(written.String(), "measure the outbox backlog") {
		t.Fatalf("log = %q, want the failure of the read in it", written.String())
	}
}

func (q *queue) NextOf(_ context.Context, wallet identity.WalletID) (storage.OutboxCandidate, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.asked++
	if q.nextErr != nil {
		return storage.OutboxCandidate{}, false, q.nextErr
	}
	if q.endless {
		return storage.OutboxCandidate{WalletID: wallet}, true, nil
	}
	behind := q.following[wallet]
	if len(behind) == 0 {
		return storage.OutboxCandidate{}, false, nil
	}
	q.following[wallet] = behind[1:]
	return behind[0], true, nil
}

func (q *queue) Due(ctx context.Context, limit int) ([]storage.OutboxCandidate, error) {
	q.scans++
	q.limit = limit
	q.signal()
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
	if q.scans > scansAtMost {
		return nil, errScannedTooOften
	}
	return q.answer(), nil
}

// signal tells a case waiting on the scan that one happened, and never holds
// the scan back when nobody is listening.
func (q *queue) signal() {
	if q.scanned == nil {
		return
	}
	select {
	case q.scanned <- struct{}{}:
	default:
	}
}

// scansAtMost bounds the scans of one case. A turn that never leaves its loop
// would otherwise relay the same rows forever and grow what the fake records
// until the machine runs out of memory; past the bound the scan fails, the turn
// ends, and the count the case asserts is what gives the loop away.
const scansAtMost = 20

var errScannedTooOften = errors.New("queue: scanned more often than any case asks")

// answer is what this scan finds: the round of this scan when the case set
// rounds, and the same rows on every scan otherwise.
func (q *queue) answer() []storage.OutboxCandidate {
	if q.rounds == nil {
		return q.due
	}
	if q.scans > len(q.rounds) {
		return nil
	}
	return q.rounds[q.scans-1]
}

// sends is the use case of one case: the candidates it was handed, a failure to
// answer, and a hold that runs on every send, outside the lock, so a case can act
// while sends are in flight.
type sends struct {
	mu   sync.Mutex
	seen []storage.OutboxCandidate
	err  error
	hold func(context.Context)
}

func (s *sends) Relay(ctx context.Context, candidate storage.OutboxCandidate) error {
	s.mu.Lock()
	s.seen = append(s.seen, candidate)
	s.mu.Unlock()
	if s.hold != nil {
		s.hold(ctx)
	}
	return s.err
}

func (s *sends) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

func quiet() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&strings.Builder{}, nil))
}

// candidates answers that many rows of a scan, each of a wallet of its own, as
// the scan answers them.
func candidates(t *testing.T, count int) []storage.OutboxCandidate {
	t.Helper()
	chosen := make([]storage.OutboxCandidate, 0, count)
	for at := range count {
		id, err := identity.ParseEventID(fmt.Sprintf("019974a4-0000-7000-8000-%012x", 0xe001+at))
		if err != nil {
			t.Fatalf("ParseEventID = %v, want nil", err)
		}
		wallet, err := identity.ParseWalletID(fmt.Sprintf("019974a4-0000-7000-8000-%012x", 0xa001+at))
		if err != nil {
			t.Fatalf("ParseWalletID = %v, want nil", err)
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
