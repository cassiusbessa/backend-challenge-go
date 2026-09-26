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

// readBound is how long a read of a wallet held for writing is given before the
// case calls it a wait. It is a bound, not a measurement: the read answers in
// milliseconds when it takes no lock.
const readBound = 5 * time.Second

func TestReconcile_answersConsistentAfterThreeMovements(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	wallet.bet(ctx, t, base, "25.00")
	wallet.win(ctx, t, base, "50.00")
	report, status := reconcile(ctx, t, base, internal, wallet.wallet.ID)
	assertConsistent(t, report, status, "1025.00", 3, 3)
	assertDifference(t, report, "0.00")
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
	if report.CheckedEntries != count || report.LastSequence != last {
		t.Fatalf("report = %d entries up to %d, want %d up to %d", report.CheckedEntries, report.LastSequence, count, last)
	}
}

func assertBalances(t *testing.T, report externalReconciliation, stored, rebuilt string) {
	t.Helper()
	if report.StoredBalance.Amount != stored || report.CalculatedBalance.Amount != rebuilt || report.CalculatedBalance.Currency != "BRL" {
		t.Fatalf("balances = %+v stored and %+v calculated, want %s and %s in BRL", report.StoredBalance, report.CalculatedBalance, stored, rebuilt)
	}
}

// assertDifference checks the stored balance minus the calculated one, with its
// sign and in the currency of the wallet.
func assertDifference(t *testing.T, report externalReconciliation, want string) {
	t.Helper()
	if report.Difference.Amount != want || report.Difference.Currency != "BRL" {
		t.Fatalf("difference = %+v, want %s BRL", report.Difference, want)
	}
}

func assertNoDivergenceField(ctx context.Context, t *testing.T, base, internal, walletID string) {
	t.Helper()
	_, _, raw := refusalOf(ctx, t, http.MethodPost, reconciliationURL(base, walletID), internal)
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
	assertDifference(t, report, "0.00")
}

// The former verb no longer reconciles: it falls on the route that does not
// exist, and the answer carries neither a balance nor a verdict.
func TestReconcile_isNotServedByTheFormerVerb(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	status, _, raw := refusalOf(ctx, t, http.MethodGet, reconciliationURL(base, wallet.wallet.ID), internal)
	if status == http.StatusOK {
		t.Fatalf("GET of the reconciliation = %d, want it not to answer the verdict: %s", status, raw)
	}
	for _, banned := range []string{"storedBalance", "calculatedBalance", "consistent", "1000.00"} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Fatalf("body of the former verb = %s, want it without %q", raw, banned)
		}
	}
}

func TestReconcile_answers404ForAWalletThatDoesNotExist(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	asked := suiteenv.NewID()
	status, mediaType, _ := refusalOf(ctx, t, http.MethodPost, reconciliationURL(base, asked), internal)
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
	// The bound is the assertion: nothing releases the wallet until the rollback
	// below, so a read that waited on the lock spends the deadline and dies instead
	// of answering. Measuring the elapsed time would assert the load of the machine.
	bounded, cancel := context.WithTimeout(ctx, readBound)
	defer cancel()
	report, status := reconcile(bounded, t, base, internal, wallet.wallet.ID)
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
	assertDivergences(t, report, status, "BALANCE_MISMATCH")
	assertBalances(t, report, "1100.00", "1000.00")
	assertDifference(t, report, "100.00")
}

// A stored balance written below what the ledger sums drifts the other way, and
// the difference says so with its sign.
func TestReconcile_answersANegativeDifferenceForABalanceWrittenBelowTheLedger(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	divergeBalance(ctx, t, wallet.wallet.ID, 90000)
	report, status := reconcile(ctx, t, base, internal, wallet.wallet.ID)
	assertDivergences(t, report, status, "BALANCE_MISMATCH")
	assertBalances(t, report, "900.00", "1000.00")
	assertDifference(t, report, "-100.00")
}

// assertDivergences checks a report that names exactly the tokens asked. The
// status is 200 either way: a divergence is a result the read reports, not a
// failure of the service.
func assertDivergences(t *testing.T, report externalReconciliation, status int, want ...string) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status of a divergent wallet = %d, want 200: a divergence is a result, not a failure", status)
	}
	if report.Consistent {
		t.Fatalf("consistent = %t, want false after the ledger was written past the application", report.Consistent)
	}
	if got := strings.Join(report.Divergences, ","); got != strings.Join(want, ",") {
		t.Fatalf("divergences = %q, want exactly %q", got, strings.Join(want, ","))
	}
}

// The count of entries no longer reaches the last sequence, and the chain still
// closes over the hole, so the gap is the only token: it is the aggregate arm of
// the statement, and the stored balance follows the sum so nothing else is named.
func TestReconcile_namesASequenceThatHasNoEntry(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	gap := brokenEntry{entryID: lowEntryID(), sequence: 5, amountCents: 2500, balanceBefore: 100000}
	insertEntryPastTheApplication(ctx, t, wallet.wallet.ID, gap, 102500)
	report, status := reconcile(ctx, t, base, internal, wallet.wallet.ID)
	assertDivergences(t, report, status, "SEQUENCE_GAP")
	if report.CheckedEntries != 2 || report.LastSequence != 5 {
		t.Fatalf("report = %d entries up to sequence %d, want 2 up to 5", report.CheckedEntries, report.LastSequence)
	}
	if report.FirstBreakSequence != 0 {
		t.Fatalf("first break = %d, want 0: the chain closes over the hole", report.FirstBreakSequence)
	}
}

// The entry starts from a balance the one before it did not leave. This is the
// window arm of the statement, the only one that needs the whole ledger, and the
// report answers where the chain broke and not only that it did.
func TestReconcile_pointsAtTheEntryThatDoesNotContinueTheChain(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	broken := brokenEntry{entryID: lowEntryID(), sequence: 2, amountCents: 1000, balanceBefore: 50000}
	insertEntryPastTheApplication(ctx, t, wallet.wallet.ID, broken, 101000)
	report, status := reconcile(ctx, t, base, internal, wallet.wallet.ID)
	assertDivergences(t, report, status, "CHAIN_BREAK")
	if report.FirstBreakSequence != 2 {
		t.Fatalf("first break = %d, want 2: the entry that starts from a balance the first one did not leave", report.FirstBreakSequence)
	}
	if report.CheckedEntries != 2 || report.LastSequence != 2 {
		t.Fatalf("report = %d entries up to sequence %d, want 2 up to 2: the chain broke, the sequence did not", report.CheckedEntries, report.LastSequence)
	}
}
