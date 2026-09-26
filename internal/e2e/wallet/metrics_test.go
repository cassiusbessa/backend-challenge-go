//go:build integration

// The series the wallet routes and the divergence watcher move, read off
// /metrics of the process under test: the watcher finds the divergence a case
// wrote past the ledger, and the route counts its own verdicts.
package wallet

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// The watcher sweeps every wallet of the suite database, which holds the
// divergent wallets other cases left behind: the series is asserted as a floor
// and not as an equality. What is exact is the wallet of this case, read back
// untouched — the watcher corrects nothing.
func TestWatcher_findsTheBalanceWrittenPastTheLedgerAndCorrectsNothing(t *testing.T) {
	ctx, base := startWith(t, map[string]string{"RECONCILIATION_INTERVAL": "50ms", "RECONCILIATION_BATCH": "500"})
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	divergeBalance(ctx, t, wallet.wallet.ID, 110000)

	awaitSeries(ctx, t, base, "wager_reconciliation_divergences_total",
		map[string]string{"origin": "watch", "divergence": "BALANCE_MISMATCH"}, 1)
	if got := seriesOf(ctx, t, base, "wager_reconciliation_wallets_checked_total", map[string]string{"origin": "watch"}); got <= 0 {
		t.Fatalf("wallets_checked{watch} = %v, want the watcher to have checked something", got)
	}
	if cents, version := storedBalance(ctx, t, wallet.wallet.ID); cents != 110000 || version != 1 {
		t.Fatalf("stored wallet after the sweep = %d cents at version %d, want 110000 at version 1: the watcher corrects nothing", cents, version)
	}
}

// The route counts its own verdicts under its own origin: every reconciliation
// is a wallet checked, and only the divergent one moves the token.
func TestReconcile_countsTheVerdictsOfTheRoute(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	consistent := openFunded(ctx, t, base, internal)
	divergent := openFunded(ctx, t, base, internal)
	divergeBalance(ctx, t, divergent.wallet.ID, 110000)
	if _, status := reconcile(ctx, t, base, internal, consistent.wallet.ID); status != http.StatusOK {
		t.Fatalf("reconciliation of the consistent wallet = %d, want 200", status)
	}
	if _, status := reconcile(ctx, t, base, internal, divergent.wallet.ID); status != http.StatusOK {
		t.Fatalf("reconciliation of the divergent wallet = %d, want 200", status)
	}
	if got := seriesOf(ctx, t, base, "wager_reconciliation_wallets_checked_total", map[string]string{"origin": "http"}); got != 2 {
		t.Fatalf("wallets_checked{http} = %v, want the 2 verdicts of this case", got)
	}
	if got := seriesOf(ctx, t, base, "wager_reconciliation_divergences_total", map[string]string{"origin": "http", "divergence": "BALANCE_MISMATCH"}); got != 1 {
		t.Fatalf("divergences{http,BALANCE_MISMATCH} = %v, want the 1 of the divergent wallet", got)
	}
}

// seriesOf reads one series of the process under test, and answers zero for
// one the scrape does not carry: a counter nothing moved yet is absent from
// the scrape, and absent reads as zero the way the Prometheus reads it.
func seriesOf(ctx context.Context, t *testing.T, base, name string, labels map[string]string) float64 {
	t.Helper()
	scraped, err := suiteenv.ScrapeMetrics(ctx, base)
	if err != nil {
		t.Fatalf("scrape the process = %v, want nil", err)
	}
	value, _ := scraped.Value(name, labels)
	return value
}

// awaitSeries polls the series until it reaches the floor, within a deadline:
// the watcher is a background component, and there is nothing to wait on but
// the series itself.
func awaitSeries(ctx context.Context, t *testing.T, base, name string, labels map[string]string, floor float64) {
	t.Helper()
	deadline := time.Now().Add(sweepWait)
	var last float64
	for time.Now().Before(deadline) {
		if last = seriesOf(ctx, t, base, name, labels); last >= floor {
			return
		}
		time.Sleep(pollEvery)
	}
	t.Fatalf("%s%v = %v after %s, want at least %v", name, labels, last, sweepWait, floor)
}

// sweepWait bounds how long a case waits for the watcher, and pollEvery is how
// often it looks. The bound covers several passes over the whole database of
// the suite at the shortened interval.
const (
	sweepWait = 45 * time.Second
	pollEvery = 100 * time.Millisecond
)
