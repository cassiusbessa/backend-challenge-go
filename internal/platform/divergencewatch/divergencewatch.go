// Package divergencewatch runs the reconciliation in the background of the
// process: it sweeps the wallets on a ticker, a page per turn, and hands each
// one to the use case that produces the verdict.
//
// Nothing about the verdict is here. The sweep chooses the wallets and takes no
// lock; what a wallet is found to be is decided by the use case over one
// statement, the same one the route calls. It is the fourth background
// component of the process and not a fourth use of a shared one, for the
// reason ADR 0011 gives; why it sweeps by a cursor in memory and not by a
// column, and why it reads every wallet and not the ones written since, is
// ADR 0024.
package divergencewatch

import (
	"context"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// Pager answers one page of wallet identities after a cursor, in the order of
// the identity.
type Pager interface {
	WalletIDsAfter(ctx context.Context, after identity.WalletID, limit int) ([]identity.WalletID, error)
}

// Reconciler produces the verdict over one wallet.
type Reconciler interface {
	Reconcile(ctx context.Context, id identity.WalletID) (reconcilewallet.Report, error)
}

// Worker is the background component of the process. The zero value is not
// used: New is the only constructor.
//
// The cursor is the last identity the sweep read, kept in memory: a page that
// comes back short resets it, so the turn after starts over from the first
// wallet. Replicas sweep on their own and find the same divergence each, which
// is accepted: the sweep is reads, and the series sum.
type Worker struct {
	pager      Pager
	reconciler Reconciler
	reporter   *Reporter
	interval   time.Duration
	batch      int

	cursor identity.WalletID

	// stop ends the run. It is nil before Start, which is the only state in
	// which Stop has nothing to end.
	stop context.CancelFunc
	// done is closed when the loop has left, which is what makes Stop wait for
	// the turn in flight rather than return while it is still reading.
	done chan struct{}
}

func New(pager Pager, reconciler Reconciler, reporter *Reporter, interval time.Duration, batch int) *Worker {
	return &Worker{pager: pager, reconciler: reconciler, reporter: reporter, interval: interval, batch: batch}
}

// Start puts the watcher on its ticker and answers at once: a database with no
// wallet in it must not hold the process back from listening.
func (w *Worker) Start(starting context.Context) error {
	// The run outlives the start, so it takes the values of that context without
	// its deadline: a watcher cancelled by the startup timeout would stop the
	// moment the process finished coming up.
	ctx, cancel := context.WithCancel(context.WithoutCancel(starting))
	w.stop = cancel
	w.done = make(chan struct{})
	go w.run(ctx)
	return nil
}

// Stop ends the run and waits for the turn in flight.
//
// No new turn begins from here on, which is what SIGTERM asks of background
// work, and the read in flight is cancelled with it: nothing it was doing was
// writable, so there is nothing to finish.
func (w *Worker) Stop(ctx context.Context) error {
	if w.stop == nil {
		return nil
	}
	w.stop()
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return fault.Wrap("stop divergence watcher", ctx.Err())
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

// turn is one page from the cursor and the verdict over each wallet of it.
//
// A failure — of the page or of one verdict — ends the turn and leaves the
// cursor where it was: the next tick reads the same page again, and a database
// that was out comes back without the process being restarted. A page that
// comes back short is the end of the sweep, and the cursor goes back to the
// start.
func (w *Worker) turn(ctx context.Context) {
	page, err := w.pager.WalletIDsAfter(ctx, w.cursor, w.batch)
	if err != nil {
		w.reporter.Failed(ctx, "page the wallets", err)
		return
	}
	for _, id := range page {
		if ctx.Err() != nil {
			// The signal came mid-page. The rest is left for the turn after, on
			// this replica when it comes back.
			return
		}
		if !w.check(ctx, id) {
			return
		}
	}
	w.advance(page)
}

// check produces the verdict over one wallet and reports it, and reports
// whether the turn goes on: a verdict that could not be produced ends it.
func (w *Worker) check(ctx context.Context, id identity.WalletID) bool {
	report, err := w.reconciler.Reconcile(ctx, id)
	if err != nil {
		w.reporter.Failed(ctx, "reconcile a wallet", err, id)
		return false
	}
	w.reporter.Checked(ctx, report)
	return true
}

// advance moves the cursor past the page, or back to the start when the page
// was the last of the sweep.
func (w *Worker) advance(page []identity.WalletID) {
	if len(page) < w.batch {
		w.cursor = identity.WalletID{}
		return
	}
	w.cursor = page[len(page)-1]
}
