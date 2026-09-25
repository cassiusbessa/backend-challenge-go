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
// It keeps the context of its own run, so that Stop ends the turn in flight
// instead of waiting for it.
type Relay struct {
	scanner  Scanner
	relayer  Relayer
	log      *slog.Logger
	interval time.Duration

	// stop ends the run. It is nil before Start, which is the only state in
	// which Stop has nothing to end.
	stop context.CancelFunc
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
	ctx, cancel := context.WithCancel(context.WithoutCancel(starting))
	r.stop = cancel
	r.done = make(chan struct{})
	go r.run(ctx)
	return nil
}

// Stop ends the run and waits for the turn in flight.
//
// No new row is claimed from here on, which is what SIGTERM asks of background
// work. A row whose send this replica did not confirm stays unpublished, with
// its payload and its event identity intact, and another replica claims it once
// the lease expires.
func (r *Relay) Stop(ctx context.Context) error {
	if r.stop == nil {
		return nil
	}
	r.stop()
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		// The shutdown deadline came first. Saying so is the honest answer: what
		// the turn in flight did not confirm is a row that is still publishable.
		return fault.Wrap("stop outbox relay", ctx.Err())
	}
}

func (r *Relay) run(ctx context.Context) {
	defer close(r.done)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.turn(ctx)
		}
	}
}

// turn is one scan and the candidates it chose.
//
// A failure of the scan ends the turn and not the relay: the queue is read again
// on the next tick, and a database that is out comes back without the process
// being restarted.
func (r *Relay) turn(ctx context.Context) {
	due, err := r.scanner.Due(ctx, batch)
	if err != nil {
		r.failed(ctx, "scan the outbox queue", err)
		return
	}
	for _, candidate := range due {
		if ctx.Err() != nil {
			// The signal came mid-batch. The rest of the candidates are left for
			// whoever scans next, here or in another replica.
			return
		}
		r.publish(ctx, candidate)
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
// frames of where it was first seen. A turn cut short by the shutdown is not a
// failure, and only the cancellation itself is read as one being stopped.
func (r *Relay) failed(ctx context.Context, message string, err error, attrs ...slog.Attr) {
	// Read off the error and not off the context: once Stop has cancelled, a
	// context test drops every failure that merely raced the signal, and that is
	// the last one the process gets to report.
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
