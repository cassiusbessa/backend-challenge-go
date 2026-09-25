// Package outboxrelay runs the publication of the outbox in the background of
// the process: it scans the queue on a ticker and hands each candidate to the
// use case that moves it out.
//
// Nothing about the turn is here. The scan chooses candidates and takes no lock;
// what happens to a row is decided by the use case under the lease it claims,
// which is what lets that rule be tested against an injected clock instead of
// this ticker.
//
// It is the second background component of the process and not a second use of
// a shared one: the turn of the reference worker is a single SQL transaction,
// and this turn is database, network and database with a lease in the middle.
// What the two share is a ticker, a batch and a stop, and an abstraction over
// both would have to expose the lease to one and hide it from the other.
package outboxrelay

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// batch is how many rows one scan takes. It bounds the work of a single turn so
// that an outbox that grew while the process was down is worked through over
// several turns instead of in one long run.
const batch = 50

// Scanner chooses the rows ready to be published.
type Scanner interface {
	Due(ctx context.Context, limit int) ([]storage.OutboxCandidate, error)
}

// Relayer moves one row out.
type Relayer interface {
	Relay(ctx context.Context, candidate storage.OutboxCandidate) error
}

// Relay is the background component of the process. The zero value is not used:
// New is the only constructor.
//
// The signal and the deadline of the shutdown are two different things to it,
// and that is why it keeps two: go-outbox asks that SIGTERM claim no new row and
// that the send in flight end inside the lease, so the signal closes claiming
// and only the deadline cancels the work.
type Relay struct {
	scanner  Scanner
	relayer  Relayer
	log      *slog.Logger
	interval time.Duration

	// claiming is closed by Stop and is the signal itself: the scan is cancelled
	// off it, and the loop takes no new row once it is closed. The Once is
	// because a lifecycle that stops twice must not close it twice.
	claiming chan struct{}
	stopOnce sync.Once
	// cut cancels the work of the turn in flight — the send that already has a
	// claim, and the write that ends it. Only the shutdown deadline reaches for
	// it: a send cancelled by the signal itself would be the very window the
	// lease exists to close. It is nil before Start, which is the only state in
	// which Stop has nothing to end.
	cut context.CancelFunc
	// done is closed when the loop has left, which is what makes Stop wait for
	// the turn in flight rather than return while it is still sending.
	done chan struct{}
}

func New(scanner Scanner, relayer Relayer, log *slog.Logger, interval time.Duration) *Relay {
	return &Relay{scanner: scanner, relayer: relayer, log: log, interval: interval}
}

// Start puts the relay on its ticker and answers at once: an outbox with
// nothing in it must not hold the process back from listening.
func (r *Relay) Start(starting context.Context) error {
	// The run outlives the start, so it takes the values of that context without
	// its deadline: a relay cancelled by the startup timeout would stop the
	// moment the process finished coming up.
	work, cut := context.WithCancel(context.WithoutCancel(starting))
	r.cut = cut
	r.claiming = make(chan struct{})
	r.done = make(chan struct{})
	// The scan is cancelled by the signal and the work is not. A scan cut short
	// loses nothing — the queue is read again by whoever scans next — while a
	// send cut short is a message the broker may have taken with nobody left to
	// confirm it.
	scanning, stopScanning := context.WithCancel(work)
	go func() {
		<-r.claiming
		stopScanning()
	}()
	go r.run(scanning, work)
	return nil
}

// Stop claims no new row and waits for the send in flight to end.
//
// The signal does not cancel that send: go-outbox gives it the lease to finish
// in, and a send cut at the signal is a message the broker may have taken with
// nobody left to confirm it. What cancels it is the shutdown deadline, which is
// the promise this process made to the one that signalled it.
//
// A row whose send this replica did not confirm stays unpublished, with its
// payload and its event identity intact, and another replica claims it once the
// lease expires.
func (r *Relay) Stop(ctx context.Context) error {
	if r.cut == nil {
		return nil
	}
	r.stopOnce.Do(func() { close(r.claiming) })
	select {
	case <-r.done:
		r.cut()
		return nil
	case <-ctx.Done():
		// The shutdown deadline came first. Cutting the work is what the deadline
		// is for, and saying so is the honest answer: what the turn in flight did
		// not confirm is a row that is still publishable.
		r.cut()
		return fault.Wrap("stop outbox relay", ctx.Err())
	}
}

func (r *Relay) run(scanning, work context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-scanning.Done():
			return
		case <-ticker.C:
			r.turn(scanning, work)
		}
	}
}

// turn is one scan and the candidates it chose.
//
// A failure of the scan ends the turn and not the relay: the queue is read again
// on the next tick, and a database that is out comes back without the process
// being restarted.
func (r *Relay) turn(scanning, work context.Context) {
	due, err := r.scanner.Due(scanning, batch)
	if err != nil {
		r.failed(scanning, "scan the outbox queue", err)
		return
	}
	for _, candidate := range due {
		if scanning.Err() != nil {
			// The signal came mid-batch. The rest of the candidates are left for
			// whoever scans next, here or in another replica.
			return
		}
		r.publish(work, candidate)
	}
}

// publish hands one candidate to the use case. The outcome of a row is logged
// there, by whoever decided it; what is logged here is the turn failing.
func (r *Relay) publish(ctx context.Context, candidate storage.OutboxCandidate) {
	if err := r.relayer.Relay(ctx, candidate); err != nil {
		r.failed(ctx, "relay an outbox event", err,
			slog.String("eventId", candidate.EventID.String()),
			slog.String("walletId", candidate.WalletID.String()),
		)
	}
}

// failed records the failure: the chain that names where it came from, and the
// frames of where it was first seen. A turn the shutdown deadline cut is not a
// failure, and only the cancellation itself is read as one being cut.
func (r *Relay) failed(ctx context.Context, message string, err error, attrs ...slog.Attr) {
	// Read off the error and not off the context: once the deadline has cancelled
	// the work, a context test drops every failure that merely raced it, and that
	// is the last one the process gets to report. What this drops is a send left
	// unfinished, never a send that went through: the confirmation of one that
	// did takes no cancellation at all.
	if errors.Is(err, context.Canceled) {
		return
	}
	r.log.LogAttrs(ctx, slog.LevelError, message,
		append(attrs,
			slog.String("error", err.Error()),
			slog.Any("stack", stackOf(message, err)),
		)...)
}

// stackOf answers the frames of the failure, capturing them here when the chain
// carries none.
func stackOf(op string, err error) []string {
	if frames := fault.Stack(err); len(frames) > 0 {
		return frames
	}
	return fault.Stack(fault.Wrap(op, err))
}
