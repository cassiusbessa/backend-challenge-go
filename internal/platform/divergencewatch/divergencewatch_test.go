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
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
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

// A consistent wallet counts as checked and leaves no line: there is nothing
// to name, and a line per wallet per sweep would drown the ones that matter.
func TestChecked_countsAConsistentWalletAndLeavesNoLine(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	moved := series()
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), moved)
	reporter.Checked(context.Background(), reconcilewallet.Report{WalletID: wallets(t, 1)[0], Consistent: true, StoredBalance: brl(t, "1000.00")})
	if written.Len() != 0 {
		t.Fatalf("log of a consistent wallet = %q, want nothing", written.String())
	}
	if got := testutil.ToFloat64(moved.WalletsChecked.WithLabelValues("watch")); got != 1 {
		t.Fatalf("wallets_checked{watch} = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(moved.Divergences); got != 2*len(reconcilewallet.Vocabulary()) {
		t.Fatalf("divergence series = %d, want only the ones primed at zero", got)
	}
}

// A divergent wallet counts each token it was found with and leaves the line
// of the route: the wallet and the tokens, never a balance.
func TestChecked_logsTheDivergentWalletAndCountsEveryToken(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	moved := series()
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), moved)
	id := wallets(t, 1)[0]
	reporter.Checked(context.Background(), reconcilewallet.Report{
		WalletID:      id,
		StoredBalance: brl(t, "2000.00"),
		LedgerBalance: brl(t, "1000.00"),
		Divergences:   []reconcilewallet.Divergence{reconcilewallet.BalanceMismatch, reconcilewallet.ChainBreak},
	})
	assertLine(t, written.String(),
		[]string{`"walletId":"` + id.String() + `"`, "BALANCE_MISMATCH", "CHAIN_BREAK"},
		[]string{"2000.00", "1000.00", "storedBalance", "ledgerBalance"})
	assertCounted(t, moved, map[string]float64{"BALANCE_MISMATCH": 1, "CHAIN_BREAK": 1, "SEQUENCE_GAP": 0})
	if got := testutil.ToFloat64(moved.WalletsChecked.WithLabelValues("watch")); got != 1 {
		t.Fatalf("wallets_checked{watch} = %v, want 1", got)
	}
}

// assertLine pins what the line carries and what it must never carry.
func assertLine(t *testing.T, line string, wanted, banned []string) {
	t.Helper()
	for _, want := range wanted {
		if !strings.Contains(line, want) {
			t.Fatalf("divergence line = %q, want %q in it", line, want)
		}
	}
	for _, ban := range banned {
		if strings.Contains(line, ban) {
			t.Fatalf("divergence line = %q, want it without %q", line, ban)
		}
	}
}

// assertCounted reads the divergence series of the watcher, one per token.
func assertCounted(t *testing.T, moved *metrics.Settlement, want map[string]float64) {
	t.Helper()
	for token, count := range want {
		if got := testutil.ToFloat64(moved.Divergences.WithLabelValues("watch", token)); got != count {
			t.Fatalf("divergences{watch,%s} = %v, want %v", token, got, count)
		}
	}
}

// A turn the shutdown cut short is not a failure, and a line per turn cut would
// say it was.
func TestFailed_recordsNothingForATurnTheShutdownCutShort(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), series())
	reporter.Failed(context.Background(), "page the wallets", fmt.Errorf("page the wallets: %w", context.Canceled))
	if written.Len() != 0 {
		t.Fatalf("log of a cancelled turn = %q, want nothing", written.String())
	}
}

// The chain names where the failure came from and the frames say where it was
// first seen; a failure that crossed no boundary is captured at this border.
func TestFailed_recordsTheChainAndTheFrames(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), series())
	reporter.Failed(context.Background(), "reconcile a wallet", errors.New("rebuild ledger balance: overflow"))
	line := written.String()
	if !strings.Contains(line, "rebuild ledger balance: overflow") || !strings.Contains(line, "divergencewatch.stackOf") {
		t.Fatalf("log = %q, want the chain and frames captured at this border", line)
	}
	seen := fault.Wrap("acquire connection", errors.New("connection refused"))
	if got := stackOf("reconcile a wallet", seen); !slices.Equal(got, fault.Stack(seen)) {
		t.Fatalf("frames of a chain that carries a stack = %v, want the %v it came with", got, fault.Stack(seen))
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
	return NewReporter(slog.New(slog.NewJSONHandler(io.Discard, nil)), series())
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
