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
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
)

// batch is how many rows one scan takes, inFlight how many wallets one replica
// sends for at once, and perWallet how many rows of one wallet follow each other
// before the next scan.
//
// The scan answers at most one row per wallet — the oldest pending one — so the
// rows of one scan belong to different wallets, and sending them together keeps
// the order of each. The next row of a wallet is publishable only once the one
// before it is confirmed; it is asked for by the wallet right after, instead of
// waiting for a whole scan, which is what held a busy wallet to a few events a
// second. perWallet bounds how long one wallet keeps a slot (ADR 0038).
const (
	batch     = 50
	inFlight  = 8
	perWallet = 50
)

// Scanner chooses the rows ready to be published, and measures the queue they
// wait in.
type Scanner interface {
	Due(ctx context.Context, limit int) ([]storage.OutboxCandidate, error)
	NextOf(ctx context.Context, wallet identity.WalletID) (storage.OutboxCandidate, bool, error)

	// Backlog answers how many rows are pending and how old the oldest is, and
	// zero for both when nothing is pending.
	Backlog(ctx context.Context) (storage.Backlog, error)
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
	metrics  *metrics.Settlement
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

func New(scanner Scanner, relayer Relayer, log *slog.Logger, series *metrics.Settlement, interval time.Duration) *Relay {
	return &Relay{scanner: scanner, relayer: relayer, log: log, metrics: series, interval: interval}
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

// turn scans and relays until a scan comes back short of the batch, so an
// outbox that grows faster than a batch per tick is worked through without
// waiting for the next one. The backlog is measured after each scan: a long
// turn would otherwise hold the gauges still until it ends.
//
// A failure of the scan ends the turn and not the relay: the queue is read again
// on the next tick, and a database that is out comes back without the process
// being restarted.
func (r *Relay) turn(scanning, work context.Context) {
	for {
		due, err := r.scanner.Due(scanning, batch)
		if err != nil {
			r.failed(scanning, "scan the outbox queue", err)
			return
		}
		r.relayAll(scanning, work, due)
		r.measure(scanning)
		if len(due) < batch || r.halted(scanning) {
			return
		}
	}
}

// relayAll hands the candidates of one scan to the use case, inFlight at a time,
// and returns once every send it started has ended. The next scan of the same
// wallet can only follow a send that ended, which is what keeps its order.
func (r *Relay) relayAll(scanning, work context.Context, due []storage.OutboxCandidate) {
	slots := make(chan struct{}, inFlight)
	var sending sync.WaitGroup
	defer sending.Wait()
	for _, candidate := range due {
		slots <- struct{}{}
		if r.halted(scanning) {
			// The signal came mid-batch. The sends in flight end inside their
			// lease, and the rest of the candidates are left for whoever scans
			// next, here or in another replica.
			return
		}
		sending.Go(func() {
			defer func() { <-slots }()
			r.chain(scanning, work, candidate)
		})
	}
}

// chain relays the candidate and then, while each send goes through, the next
// row of the same wallet, up to perWallet of them. A row set back on the backoff
// or held by another replica ends the chain, because NextOf answers nothing for
// either; so does the signal, which claims no new row.
func (r *Relay) chain(scanning, work context.Context, candidate storage.OutboxCandidate) {
	for range perWallet {
		if !r.publish(work, candidate) || r.halted(scanning) {
			return
		}
		next, found := r.behind(scanning, candidate.WalletID)
		if !found {
			return
		}
		candidate = next
	}
}

// behind answers the row that follows in the wallet, when it can be claimed now.
// A read that fails is logged and ends the chain: the next scan finds the row.
func (r *Relay) behind(scanning context.Context, wallet identity.WalletID) (storage.OutboxCandidate, bool) {
	next, found, err := r.scanner.NextOf(scanning, wallet)
	if err != nil {
		r.failed(scanning, "read the next outbox event of a wallet", err, slog.String("walletId", wallet.String()))
		return storage.OutboxCandidate{}, false
	}
	return next, found
}

// halted reports whether no new row is to be claimed: the signal came, or the
// scan it cancels already reads as done.
func (r *Relay) halted(scanning context.Context) bool {
	return r.stopping() || scanning.Err() != nil
}

// measure reads the backlog into its two gauges, after the turn has published
// its candidates, so a row this turn confirmed is not what it reads. A read
// that fails leaves the last values: a gauge that fell to zero because the
// database was out would read as a queue that emptied.
//
// The backlog counts the row a replica holds under a lease too: it has not
// reached the topic, and its age is what whoever consumes the events feels.
// The alert absorbs a legitimate lease with its own window, not this gauge.
func (r *Relay) measure(scanning context.Context) {
	backlog, err := r.scanner.Backlog(scanning)
	if err != nil {
		r.failed(scanning, "measure the outbox backlog", err)
		return
	}
	r.metrics.OutboxPending.Set(float64(backlog.Pending))
	r.metrics.OutboxOldestAge.Set(backlog.OldestAge.Seconds())
}

// stopping reports whether the signal has already come. It reads the signal
// itself and not the context derived from it: the cancellation of that context
// lands a moment later, and one row would be claimed inside that window.
func (r *Relay) stopping() bool {
	select {
	case <-r.claiming:
		return true
	default:
		return false
	}
}

// publish hands one candidate to the use case, and reports whether the turn
// went through. The outcome of a row is logged there, by whoever decided it;
// what is logged here is the turn failing.
func (r *Relay) publish(ctx context.Context, candidate storage.OutboxCandidate) bool {
	if err := r.relayer.Relay(ctx, candidate); err != nil {
		r.failed(ctx, "relay an outbox event", err,
			slog.String("eventId", candidate.EventID.String()),
			slog.String("walletId", candidate.WalletID.String()),
		)
		return false
	}
	return true
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
