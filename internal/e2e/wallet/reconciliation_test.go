//go:build integration

package wallet

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

func TestReconcile_answersConsistentAfterThreeMovements(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	wallet.bet(ctx, t, base, "25.00")
	wallet.win(ctx, t, base, "50.00")
	report, status := reconcile(ctx, t, base, internal, wallet.wallet.ID)
	assertConsistent(t, report, status, "1025.00", 3, 3)
	if report.Version != 3 {
		t.Fatalf("version = %d, want 3 after two movements over the opening", report.Version)
	}
	assertNoDivergenceField(ctx, t, base, internal, wallet.wallet.ID)
}

// assertConsistent checks a report whose ledger closes with the balance: both
// sides at the same amount in BRL, and the count and the last sequence asked.
func assertConsistent(t *testing.T, report externalReconciliation, status int, balance string, count, last int64) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !report.Consistent {
		t.Fatalf("consistent = false with %v, want true", report.Divergences)
	}
	assertBalances(t, report, balance, balance)
	if report.EntryCount != count || report.LastSequence != last {
		t.Fatalf("report = %d entries up to %d, want %d up to %d", report.EntryCount, report.LastSequence, count, last)
	}
}

func assertBalances(t *testing.T, report externalReconciliation, stored, rebuilt string) {
	t.Helper()
	if report.StoredBalance.Amount != stored || report.LedgerBalance.Amount != rebuilt || report.LedgerBalance.Currency != "BRL" {
		t.Fatalf("balances = %+v stored and %+v rebuilt, want %s and %s in BRL", report.StoredBalance, report.LedgerBalance, stored, rebuilt)
	}
}

func assertNoDivergenceField(ctx context.Context, t *testing.T, base, internal, walletID string) {
	t.Helper()
	_, _, raw := refusalOf(ctx, t, reconciliationURL(base, walletID), internal)
	for _, absent := range []string{"divergences", "firstBreakSequence"} {
		if bytes.Contains(raw, []byte(absent)) {
			t.Fatalf("body = %s, want it without %q for a consistent wallet", raw, absent)
		}
	}
}

func TestReconcile_answersConsistentForAWalletAtZero(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	opened, status := open(ctx, t, base, internal, body(suiteenv.NewID(), "0.00", "BRL"))
	if status != http.StatusCreated {
		t.Fatalf("opening at zero = %d, want 201", status)
	}
	report, status := reconcile(ctx, t, base, internal, opened.ID)
	assertConsistent(t, report, status, "0.00", 0, 0)
}

func TestReconcile_answers404ForAWalletThatDoesNotExist(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	asked := suiteenv.NewID()
	status, mediaType, _ := refusalOf(ctx, t, reconciliationURL(base, asked), internal)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if mediaType != problem.MediaType {
		t.Fatalf("content type = %s, want %s", mediaType, problem.MediaType)
	}
	if got := count(ctx, t, "SELECT count(*) FROM wallets WHERE id = $1", asked); got != 0 {
		t.Fatalf("wallets for the identity reconciled = %d, want 0: a read writes nothing", got)
	}
}

// The read takes no lock: it answers while another transaction holds the wallet
// for writing, and the version does not move because of it.
func TestReconcile_doesNotWaitForALockedWallet(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	holding := holdForUpdate(ctx, t, wallet.wallet.ID)
	started := time.Now()
	report, status := reconcile(ctx, t, base, internal, wallet.wallet.ID)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("the read took %s behind a locked wallet, want it answered without waiting", elapsed)
	}
	assertConsistent(t, report, status, "1000.00", 1, 1)
	if err := holding.Rollback(ctx); err != nil {
		t.Fatalf("release the wallet = %v, want nil", err)
	}
	if _, version := storedBalance(ctx, t, wallet.wallet.ID); version != 1 {
		t.Fatalf("version after the read = %d, want 1: a read moves nothing", version)
	}
}

// holdForUpdate locks the wallet the way a submission does, in a transaction the
// case releases, so the read is asked while the row is held for writing.
func holdForUpdate(ctx context.Context, t *testing.T, walletID string) pgx.Tx {
	t.Helper()
	holding, err := connect(ctx, t).Begin(ctx)
	if err != nil {
		t.Fatalf("begin the holding transaction = %v, want nil", err)
	}
	t.Cleanup(func() { _ = holding.Rollback(context.WithoutCancel(ctx)) })
	if _, err := holding.Exec(ctx, "SELECT id FROM wallets WHERE id = $1 FOR UPDATE", walletID); err != nil {
		t.Fatalf("lock the wallet = %v, want nil", err)
	}
	return holding
}

// The only way to produce what the route exists to detect is a write that went
// around the application and the schema: the stored balance differs from the
// ledger, the report names it, and the read corrects nothing.
func TestReconcile_namesABalanceWrittenPastTheLedger(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	divergeBalance(ctx, t, wallet.wallet.ID, 110000)
	report, status := reconcile(ctx, t, base, internal, wallet.wallet.ID)
	assertBalanceMismatch(t, report, status)
	if cents, _ := storedBalance(ctx, t, wallet.wallet.ID); cents != 110000 {
		t.Fatalf("stored balance after the read = %d, want 110000: the read corrects nothing", cents)
	}
}

func assertBalanceMismatch(t *testing.T, report externalReconciliation, status int) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: a divergence is a result, not a failure", status)
	}
	if report.Consistent {
		t.Fatalf("consistent = true, want false after the balance was written past the ledger")
	}
	if strings.Join(report.Divergences, ",") != "BALANCE_MISMATCH" {
		t.Fatalf("divergences = %v, want exactly BALANCE_MISMATCH", report.Divergences)
	}
	assertBalances(t, report, "1100.00", "1000.00")
}
