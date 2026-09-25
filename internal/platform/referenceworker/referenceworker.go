// Package referenceworker runs the reference wait in the background of the
// process: it scans the queue on a ticker and hands each candidate to the use
// case that decides it.
//
// Nothing about the decision is here. The scan chooses candidates and takes no
// lock; what the wait becomes is decided under the lock, by the use case, which
// is what lets that rule be tested against an injected clock instead of this
// ticker.
package referenceworker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// batch is how many waits one scan takes. It bounds the work of a single turn
// so that a queue that grew while the process was down is worked through over
// several turns instead of in one long transaction run.
const batch = 50

// Scanner chooses the waits whose scheduled instant has come.
type Scanner interface {
	DueWaits(ctx context.Context, now time.Time, limit int) ([]storage.WaitCandidate, error)
}

// Resolver decides one wait.
type Resolver interface {
	Resolve(ctx context.Context, candidate storage.WaitCandidate) error
}

// Clock reads the instant the queue is scanned against.
type Clock interface {
	Now() time.Time
}

// Worker is the background component of the process. The zero value is not
// used: New is the only constructor.
//
// It keeps the context of its own run, so that Stop ends the turn in flight
// instead of waiting for it.
type Worker struct {
	scanner  Scanner
	resolver Resolver
	clock    Clock
	log      *slog.Logger
	interval time.Duration

	// stop ends the run. It is nil before Start, which is the only state in
	// which Stop has nothing to end.
	stop context.CancelFunc
	// done is closed when the loop has left, which is what makes Stop wait for
	// the turn in flight rather than return while it is still writing.
	done chan struct{}
}

func New(scanner Scanner, resolver Resolver, clock Clock, log *slog.Logger, interval time.Duration) *Worker {
	return &Worker{scanner: scanner, resolver: resolver, clock: clock, log: log, interval: interval}
}

// Start puts the worker on its ticker and answers at once: a queue with nothing
// in it must not hold the process back from listening.
func (w *Worker) Start(starting context.Context) error {
	// The run outlives the start, so it takes the values of that context without
	// its deadline: a worker cancelled by the startup timeout would stop the
	// moment the process finished coming up.
	ctx, cancel := context.WithCancel(context.WithoutCancel(starting))
	w.stop = cancel
	w.done = make(chan struct{})
	go w.run(ctx)
	return nil
}

// Stop ends the run and waits for the turn in flight.
//
// No new wait is claimed from here on, which is what SIGTERM asks of background
// work. The turn already running either commits or rolls back inside the
// shutdown deadline, and a wait it does not finish stays PENDING_REFERENCE with
// its deadline intact, available to another replica.
func (w *Worker) Stop(ctx context.Context) error {
	if w.stop == nil {
		return nil
	}
	w.stop()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		// The shutdown deadline came first. Saying so is the honest answer: the
		// turn in flight is inside its own SQL transaction, so what it has not
		// committed is rolled back by the database when the connection goes.
		return fault.Wrap("stop reference worker", ctx.Err())
	}
}

func (w *Worker) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.turn(ctx)
		}
	}
}

// turn is one scan and the candidates it chose.
//
// A failure of the scan ends the turn and not the worker: the queue is read
// again on the next tick, and a database that is out comes back without the
// process being restarted.
func (w *Worker) turn(ctx context.Context) {
	due, err := w.scanner.DueWaits(ctx, w.clock.Now(), batch)
	if err != nil {
		w.failed(ctx, "scan the reference wait queue", err)
		return
	}
	for _, candidate := range due {
		if ctx.Err() != nil {
			// The signal came mid-batch. The rest of the candidates are left for
			// whoever scans next, here or in another replica.
			return
		}
		w.decide(ctx, candidate)
	}
}

// decide hands one candidate to the use case and is the one place a failure of
// it is logged: whoever decides the outcome logs it, and logs it once.
func (w *Worker) decide(ctx context.Context, candidate storage.WaitCandidate) {
	if err := w.resolver.Resolve(ctx, candidate); err != nil {
		w.failed(ctx, "resolve a pending reference", err,
			slog.String("transactionId", candidate.TransactionID.String()),
			slog.String("walletId", candidate.WalletID.String()),
		)
	}
}

// failed records the failure: the chain that names where it came from, and the
// frames of where it was first seen. A turn cut short by the shutdown is not a
// failure, and only the cancellation itself is read as one being stopped.
func (w *Worker) failed(ctx context.Context, message string, err error, attrs ...slog.Attr) {
	// Read off the error and not off the context: once Stop has cancelled, a
	// context test drops every failure that merely raced the signal, and that is
	// the last one the process gets to report.
	if errors.Is(err, context.Canceled) {
		return
	}
	w.log.LogAttrs(ctx, slog.LevelError, message,
		append(attrs,
			slog.String("error", err.Error()),
			slog.Any("stack", stackOf(message, err)),
		)...)
}

// stackOf answers the frames of the failure, capturing them here when the chain
// carries none.
//
// Nothing the use case refuses on its own crossed an I/O boundary, so its chain
// holds no fault and this border is where it is first seen as a failure.
func stackOf(op string, err error) []string {
	if frames := fault.Stack(err); len(frames) > 0 {
		return frames
	}
	return fault.Stack(fault.Wrap(op, err))
}
