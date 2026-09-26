package divergencewatch

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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
)

// tick is the interval the cases about the lifecycle run on. It is short because
// none of them waits for it: each is driven by the channel the fake writes to.
// The cases about one turn call that turn and never start a ticker at all.
const tick = time.Millisecond

// One turn is one page from the cursor, walked in order, and the cursor moves
// past it. A page shorter than the batch is the end of the sweep, and the turn
// after starts over from the first wallet.
func TestTurn_walksEachPageInOrderAndStartsOverAfterAShortOne(t *testing.T) {
	t.Parallel()
	table := &pages{wallets: wallets(t, 5)}
	verdicts := &verdicts{}
	worker := New(table, verdicts, quietReporter(), tick, 2)
	expected := [][]identity.WalletID{
		table.wallets[0:2],
		table.wallets[2:4],
		table.wallets[4:5],
		table.wallets[0:2],
	}
	for turn, want := range expected {
		verdicts.seen = nil
		worker.turn(context.Background())
		if !slices.Equal(verdicts.seen, want) {
			t.Fatalf("turn %d checked %v, want %v", turn, verdicts.seen, want)
		}
	}
	if !slices.Equal(table.asked, []identity.WalletID{{}, table.wallets[1], table.wallets[3], {}}) {
		t.Fatalf("cursors asked = %v, want the start, the end of each full page, and the start again", table.asked)
	}
}

// A verdict that could not be produced ends the turn and leaves the cursor
// where it was: the next tick reads the same page, so a database that comes
// back needs no restart and no wallet is skipped.
func TestTurn_leavesTheCursorWhereItWasWhenAVerdictFails(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	table := &pages{wallets: wallets(t, 3)}
	broken := errors.New("postgres: connection reset by peer")
	verdicts := &verdicts{failing: map[identity.WalletID]error{table.wallets[1]: broken}}
	worker := New(table, verdicts, NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), series()), tick, 2)
	worker.turn(context.Background())
	if !slices.Equal(verdicts.seen, table.wallets[0:2]) {
		t.Fatalf("wallets checked before the failure = %v, want the first two and nothing after", verdicts.seen)
	}
	if !worker.cursor.IsZero() {
		t.Fatalf("cursor after a failed verdict = %s, want it left at the start", worker.cursor)
	}
	for _, want := range []string{"reconcile a wallet", table.wallets[1].String(), "stack", broken.Error()} {
		if !strings.Contains(written.String(), want) {
			t.Fatalf("log = %q, want %q in it", written.String(), want)
		}
	}
	verdicts.seen, verdicts.failing = nil, nil
	worker.turn(context.Background())
	if !slices.Equal(verdicts.seen, table.wallets[0:2]) {
		t.Fatalf("wallets checked on the turn after = %v, want the same page again", verdicts.seen)
	}
}

// A page that could not be read ends the turn before any verdict, and the
// cursor stays put for the same reason.
func TestTurn_checksNothingWhenThePageFails(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	table := &pages{wallets: wallets(t, 2), failFirst: errors.New("postgres: connection refused")}
	verdicts := &verdicts{}
	worker := New(table, verdicts, NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), series()), tick, 2)
	worker.turn(context.Background())
	if len(verdicts.seen) != 0 {
		t.Fatalf("wallets checked after a page that failed = %v, want none", verdicts.seen)
	}
	if !strings.Contains(written.String(), "page the wallets") {
		t.Fatalf("log = %q, want the failure of the page in it", written.String())
	}
	worker.turn(context.Background())
	if len(verdicts.seen) != 2 {
		t.Fatalf("wallets checked on the turn after = %d, want 2", len(verdicts.seen))
	}
}

// The signal reaching the watcher mid-page leaves the rest for the turn after.
func TestTurn_checksNoNewWalletOnceTheContextIsDone(t *testing.T) {
	t.Parallel()
	verdicts := &verdicts{}
	worker := New(&pages{wallets: wallets(t, 3)}, verdicts, quietReporter(), tick, 3)
	signalled, stop := context.WithCancel(context.Background())
	verdicts.hold = func(context.Context) { stop() }
	worker.turn(signalled)
	if len(verdicts.seen) != 1 {
		t.Fatalf("wallets checked mid-page = %d, want the 1 already in flight when the signal came", len(verdicts.seen))
	}
}

// A table with no wallet in it must not hold the process back: the start
// answers at once and the ticker is what sweeps.
func TestStart_comesUpOverAnEmptyTableAndSweepsOnTheTicker(t *testing.T) {
	t.Parallel()
	table := &pages{paged: make(chan struct{}, 4)}
	worker := New(table, &verdicts{}, quietReporter(), tick, 2)
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("Start over an empty table = %v, want nil", err)
	}
	<-table.paged
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after an empty table = %v, want nil", err)
	}
}

// The signal stops the run, and the stop waits for the turn in flight rather
// than returning while it is still reading.
func TestStop_endsTheRunAndWaitsForTheTurnInFlight(t *testing.T) {
	t.Parallel()
	verdicts := &verdicts{}
	worker := New(&pages{wallets: wallets(t, 3)}, verdicts, quietReporter(), tick, 3)
	stopped := make(chan error, 1)
	verdicts.hold = func(ctx context.Context) {
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
	if len(verdicts.seen) != 1 {
		t.Fatalf("wallets checked = %d, want the 1 already in flight when the signal came", len(verdicts.seen))
	}
}

// A watcher that was only assembled holds no run and has read nothing: the
// lifecycle is what starts it, and until it does there is nothing to stop.
func TestNew_answersAWatcherThatHasNotStarted(t *testing.T) {
	t.Parallel()
	table := &pages{wallets: wallets(t, 2)}
	worker := New(table, &verdicts{}, quietReporter(), tick, 2)
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("Stop of a watcher that was only assembled = %v, want nil", err)
	}
	if table.pagesRead != 0 || !worker.cursor.IsZero() {
		t.Fatalf("pages read = %d from cursor %s, want none before the lifecycle starts it", table.pagesRead, worker.cursor)
	}
}

// The run leaves on a context that is already done without taking a turn, and
// it closes what the stop waits on.
func TestRun_leavesOnADoneContextWithoutTakingATurn(t *testing.T) {
	t.Parallel()
	table := &pages{wallets: wallets(t, 1)}
	worker := New(table, &verdicts{}, quietReporter(), time.Hour, 2)
	worker.done = make(chan struct{})
	stopped, stop := context.WithCancel(context.Background())
	stop()
	worker.run(stopped)
	<-worker.done
	if table.pagesRead != 0 {
		t.Fatalf("pages read = %d, want none on a run that was already done", table.pagesRead)
	}
}

// One verdict reported, and the turn goes on; one that could not be produced
// ends it, and is not counted as a wallet checked.
func TestCheck_reportsTheVerdictAndAnswersWhetherTheTurnGoesOn(t *testing.T) {
	t.Parallel()
	moved := series()
	id := wallets(t, 1)[0]
	good := New(&pages{}, &verdicts{}, NewReporter(quietLogger(), moved), tick, 2)
	if goesOn := good.check(context.Background(), id); !goesOn {
		t.Fatalf("turn after a verdict = %t, want it to go on", goesOn)
	}
	failing := &verdicts{failing: map[identity.WalletID]error{id: errors.New("postgres: connection refused")}}
	bad := New(&pages{}, failing, NewReporter(quietLogger(), moved), tick, 2)
	if goesOn := bad.check(context.Background(), id); goesOn {
		t.Fatalf("turn after a verdict that failed = %t, want it ended", goesOn)
	}
	if got := testutil.ToFloat64(moved.WalletsChecked.WithLabelValues("watch")); got != 1 {
		t.Fatalf("wallets_checked{watch} = %v, want only the verdict that was produced", got)
	}
}

// A full page moves the cursor to its last wallet; a short one is the end of
// the sweep and takes the cursor back to the start.
func TestAdvance_movesPastAFullPageAndBackToTheStartAfterAShortOne(t *testing.T) {
	t.Parallel()
	ids := wallets(t, 3)
	worker := New(&pages{}, &verdicts{}, quietReporter(), tick, 2)
	worker.advance(ids[0:2])
	if worker.cursor != ids[1] {
		t.Fatalf("cursor after a full page = %s, want its last wallet %s", worker.cursor, ids[1])
	}
	worker.advance(ids[2:3])
	if !worker.cursor.IsZero() {
		t.Fatalf("cursor after a short page = %s, want it back at the start", worker.cursor)
	}
}

func TestStop_answersNilForAWatcherThatNeverStarted(t *testing.T) {
	t.Parallel()
	if err := New(&pages{}, &verdicts{}, quietReporter(), tick, 2).Stop(context.Background()); err != nil {
		t.Fatalf("Stop of a watcher that never started = %v, want nil", err)
	}
}

// The shutdown deadline coming first is answered as the failure it is: the
// read in flight is cancelled, and nothing of it was writable.
func TestStop_answersTheFailureWhenTheShutdownDeadlineComesFirst(t *testing.T) {
	t.Parallel()
	held := make(chan struct{})
	release := make(chan struct{})
	verdicts := &verdicts{hold: func(context.Context) {
		close(held)
		<-release
	}}
	worker := New(&pages{wallets: wallets(t, 1)}, verdicts, quietReporter(), tick, 1)
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

// pages is the table of wallets in memory: it answers the page after a cursor
// the way the statement does, and keeps every cursor it was asked for.
type pages struct {
	wallets   []identity.WalletID
	asked     []identity.WalletID
	pagesRead int
	failFirst error
	// paged reports each page to the case about the watcher coming up over an
	// empty table.
	paged chan struct{}
}

func (p *pages) WalletIDsAfter(_ context.Context, after identity.WalletID, limit int) ([]identity.WalletID, error) {
	p.pagesRead++
	p.asked = append(p.asked, after)
	p.notify()
	if p.failFirst != nil && p.pagesRead == 1 {
		return nil, p.failFirst
	}
	return pageAfter(p.wallets, after, limit), nil
}

func (p *pages) notify() {
	if p.paged == nil {
		return
	}
	select {
	case p.paged <- struct{}{}:
	default:
	}
}

// pageAfter is the predicate of the statement: the identities after the
// cursor, in order, up to the limit.
func pageAfter(wallets []identity.WalletID, after identity.WalletID, limit int) []identity.WalletID {
	var page []identity.WalletID
	for _, id := range wallets {
		if id.String() > after.String() && len(page) < limit {
			page = append(page, id)
		}
	}
	return page
}

// verdicts is the use case as the watcher sees it: what it was handed, the
// wallets whose verdict fails, and a hold that runs on the first verdict so a
// case can act while a turn is open.
type verdicts struct {
	seen    []identity.WalletID
	failing map[identity.WalletID]error
	hold    func(context.Context)
}

func (v *verdicts) Reconcile(ctx context.Context, id identity.WalletID) (reconcilewallet.Report, error) {
	v.seen = append(v.seen, id)
	if hold := v.hold; hold != nil {
		v.hold = nil
		hold(ctx)
	}
	if err := v.failing[id]; err != nil {
		return reconcilewallet.Report{}, err
	}
	return reconcilewallet.Report{WalletID: id, Consistent: true}, nil
}

func series() *metrics.Settlement {
	return metrics.New(prometheus.NewRegistry())
}

func quietReporter() *Reporter {
	return NewReporter(quietLogger(), series())
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// wallets mints that many identities already in the order of the identity,
// which is the order the page comes in.
func wallets(t *testing.T, count int) []identity.WalletID {
	t.Helper()
	out := make([]identity.WalletID, 0, count)
	for at := range count {
		id, err := identity.ParseWalletID(fmt.Sprintf("1111111%d-1111-4111-8111-111111111111", at+1))
		if err != nil {
			t.Fatalf("ParseWalletID = %v, want nil", err)
		}
		out = append(out, id)
	}
	return out
}

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	return parsed
}
