//go:build integration

// The journey of an operation that cites another, against a real PostgreSQL, a
// real IdP and the worker of the process itself: what the border answers while
// the cited operation has not arrived, what the worker does when it does, and
// what the deadline does when it never does.
//
// Every case opens its own wallet, so two runs of the suite never collide on the
// unique index of one wallet per player and currency.
package wager

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// A WIN citing a bet that has not arrived is accepted and waits. The bet
// concludes, and the worker carries the credit out in a commit of its own.
func TestSubmit_waitsForTheCitedBetAndTheWorkerCreditsTheWin(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	cited := "external-" + newID()
	round := "round-" + newID()

	waiting := assertWaits(ctx, t, at, wallet.win("50.00", citing(cited, round)))
	assertWallet(ctx, t, wallet.id, 100000, 1)
	assertEntries(ctx, t, wallet.id, 1)

	placed := submitBody(ctx, t, at, wallet.bet("25.00", named(cited, round)))
	if placed.status != http.StatusCreated {
		t.Fatalf("the awaited bet = %d, want 201: %s", placed.status, placed.body)
	}

	awaitStatus(ctx, t, at, waiting, "PROCESSED")
	assertWallet(ctx, t, wallet.id, 102500, 3)
	assertEntries(ctx, t, wallet.id, 3)
}

// The deadline closes the wait with the token of what was missing, and the two
// cases differ only in whether the cited operation ever arrived.
func TestWorker_closesTheWaitAtTheDeadlineWithTheTokenOfWhatWasMissing(t *testing.T) {
	ctx, closing := startWith(t, shortWait)
	// The operation the second wait cites is written by a process that measures
	// the TTL of the rule and never scans, so it is still waiting when the
	// deadline of the one citing it comes. Written with the same short TTL, which
	// of the two the worker reached first would decide the token.
	_, lasting := startWith(t, map[string]string{"REFERENCE_TTL": "15m", "REFERENCE_INTERVAL": "1h"})
	wallet := openWallet(ctx, t, closing)

	round := "round-" + newID()
	cited := "external-" + newID()
	awaited := assertWaits(ctx, t, lasting, wallet.win("10.00", both(named(cited, round), citing("external-"+newID(), round))))
	absent := assertWaits(ctx, t, closing, wallet.win("50.00", citing("external-"+newID(), "round-"+newID())))
	running := assertWaits(ctx, t, closing, wallet.refund("10.00", citing(cited, round)))

	awaitRejection(ctx, t, closing, absent, "REFERENCE_NOT_FOUND")
	awaitRejection(ctx, t, closing, running, "REFERENCE_NOT_PROCESSED")
	assertStillWaiting(ctx, t, closing, awaited)
	assertWallet(ctx, t, wallet.id, 100000, 1)
	assertEntries(ctx, t, wallet.id, 1)
}

// The wait whose deadline is still far off is left where it is: a scan that
// reaches it only moves its schedule.
func assertStillWaiting(ctx context.Context, t *testing.T, at suite, id string) {
	t.Helper()
	answered := read(ctx, t, at, at.provider, id)
	if answered.status != http.StatusOK {
		t.Fatalf("read of the wait %s = %d, want 200: %s", id, answered.status, answered.body)
	}
	if got := answered.transaction(t).Status; got != "PENDING_REFERENCE" {
		t.Fatalf("status of the wait that has not expired = %s, want PENDING_REFERENCE", got)
	}
}

// A reversal of an operation that already concluded is decided in the very
// commit of the submission: the cited operation is there, so nothing waits.
func TestSubmit_settlesTheReversalOfAnOperationThatAlreadyConcluded(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	round := "round-" + newID()
	placed := "external-" + newID()
	won := "external-" + newID()

	placeBet(ctx, t, at, wallet, "25.00", named(placed, round))
	assertWallet(ctx, t, wallet.id, 97500, 2)

	assertProcessed(ctx, t, at, wallet.refund("25.00", citing(placed, round)), "1000.00")
	assertWallet(ctx, t, wallet.id, 100000, 3)

	assertProcessed(ctx, t, at, wallet.win("50.00", named(won, round)), "1050.00")
	assertProcessed(ctx, t, at, wallet.rollback("50.00", citing(won, round)), "1000.00")
	assertWallet(ctx, t, wallet.id, 100000, 5)
	assertEntries(ctx, t, wallet.id, 5)
}

// Partial reversal does not exist: an amount other than the whole of the cited
// operation is refused, with the row kept and nothing moved.
func TestSubmit_refusesAReversalThatIsNotTheWholeOfTheCitedOperation(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	round := "round-" + newID()
	placed := "external-" + newID()
	placeBet(ctx, t, at, wallet, "25.00", named(placed, round))

	key := "key-" + newID()
	refused := submit(ctx, t, at, at.provider, key, wallet.refund("10.00", citing(placed, round)))
	if refused.status != http.StatusUnprocessableEntity {
		t.Fatalf("a partial reversal = %d, want 422: %s", refused.status, refused.body)
	}
	if got := refused.refusal(t).FailureCode; got != "REVERSAL_AMOUNT_MISMATCH" {
		t.Fatalf("failureCode = %s, want REVERSAL_AMOUNT_MISMATCH", got)
	}
	assertRowsForKey(ctx, t, key, 1)
	assertWallet(ctx, t, wallet.id, 97500, 2)
	assertEntries(ctx, t, wallet.id, 2)
}

// One reversal per cited operation. The second keeps its row REJECTED with the
// token, writes no entry, and leaves the first exactly as it was.
func TestSubmit_refusesTheSecondReversalAndLeavesTheFirstIntact(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	round := "round-" + newID()
	placed := "external-" + newID()
	placeBet(ctx, t, at, wallet, "25.00", named(placed, round))
	first := assertProcessed(ctx, t, at, wallet.refund("25.00", citing(placed, round)), "1000.00")

	key := "key-" + newID()
	refused := submit(ctx, t, at, at.provider, key, wallet.rollback("25.00", citing(placed, round)))
	if refused.status != http.StatusUnprocessableEntity {
		t.Fatalf("the second reversal = %d, want 422: %s", refused.status, refused.body)
	}
	if got := refused.refusal(t).FailureCode; got != "ALREADY_REVERSED" {
		t.Fatalf("failureCode = %s, want ALREADY_REVERSED", got)
	}
	assertRowsForKey(ctx, t, key, 1)
	assertRead(ctx, t, at, first, "PROCESSED", "")
	assertWallet(ctx, t, wallet.id, 100000, 3)
	assertEntries(ctx, t, wallet.id, 3)
}

// Two waits on the same wallet and two replicas of the worker. Each wait is
// decided once, and the balance the wallet ends with is the one the last entry
// left.
func TestWorker_decidesEachWaitOnceAcrossTwoReplicas(t *testing.T) {
	ctx, at := startWith(t, map[string]string{"REFERENCE_INTERVAL": "10ms"})
	wallet := openWallet(ctx, t, at)
	// A second process over the same database, which is what makes the two
	// workers contend for the same rows.
	boot(ctx, t, map[string]string{"REFERENCE_INTERVAL": "10ms"})

	round := "round-" + newID()
	first := "external-" + newID()
	second := "external-" + newID()
	waits := []string{
		assertWaits(ctx, t, at, wallet.win("50.00", citing(first, round))),
		assertWaits(ctx, t, at, wallet.win("30.00", citing(second, round))),
	}
	together(ctx, t, at,
		wallet.bet("25.00", named(first, round)),
		wallet.bet("25.00", named(second, round)),
	)
	for _, waiting := range waits {
		awaitStatus(ctx, t, at, waiting, "PROCESSED")
	}
	// Two bets of 25.00 and two wins of 50.00 and 30.00, over the opening of
	// 1000.00: four movements, and the version rose once per movement.
	assertWallet(ctx, t, wallet.id, 103000, 5)
	assertEntries(ctx, t, wallet.id, 5)
	assertBalanceMatchesLastEntry(ctx, t, wallet.id)
}

// The wait is durable and lasts until its deadline, so the same key and the same
// body answer the wait already recorded. The replay decides nothing: the
// schedule of the row is exactly where it was.
func TestSubmit_replaysTheWaitAndThenTheOutcomeItEndedWith(t *testing.T) {
	// The TTL is a moment and the worker is kept off: the deadline is written on
	// entry, so the wait has to be recorded by a process that measures it short,
	// while the schedule of that row is what this case asserts and a scan in the
	// middle of it would be the one moving it.
	ctx, at := startWith(t, map[string]string{"REFERENCE_TTL": "1s", "REFERENCE_INTERVAL": "1h"})
	wallet := openWallet(ctx, t, at)
	key := "key-" + newID()
	payload := wallet.win("50.00", citing("external-"+newID(), "round-"+newID()))

	accepted := submit(ctx, t, at, at.provider, key, payload)
	if accepted.status != http.StatusAccepted {
		t.Fatalf("the operation that waits under a key of its own = %d, want 202: %s", accepted.status, accepted.body)
	}
	waiting := accepted.transaction(t).ID
	before := schedule(ctx, t, waiting)

	replayed := submit(ctx, t, at, at.provider, key, payload)
	if replayed.status != http.StatusOK {
		t.Fatalf("the replay of a wait = %d, want 200: %s", replayed.status, replayed.body)
	}
	assertWaitReplayed(t, replayed.transaction(t))
	if got := schedule(ctx, t, waiting); got != before {
		t.Fatalf("schedule after the replay = %v, want the %v it already had", got, before)
	}
	assertRowsForKey(ctx, t, key, 1)
	assertReplayAfterTheWaitEnds(ctx, t, at, key, payload, waiting)
}

// The replay of a wait answers the wait itself, marked, and carries no balance:
// no commit has closed it.
func assertWaitReplayed(t *testing.T, answered externalTransaction) {
	t.Helper()
	if answered.Status != "PENDING_REFERENCE" || !answered.IdempotentReplay {
		t.Fatalf("replayed = %+v, want the wait marked as a replay", answered)
	}
	if answered.ObservedBalance.Amount != "" {
		t.Fatalf("observed balance = %s, want none while nothing has closed the wait", answered.ObservedBalance.Amount)
	}
}

// Once the deadline has closed the wait, the same key and the same body answer
// the outcome that closed it, marked as a replay.
func assertReplayAfterTheWaitEnds(ctx context.Context, t *testing.T, at suite, key, payload, waiting string) {
	t.Helper()
	// A second process, this one with a TTL of a moment, is what closes the wait:
	// the one the case submits through keeps its worker off so the schedule it
	// asserted above stayed where it was. The context of this case is the one
	// every call here carries; the second process only needs its tokens.
	_, closer := startWith(t, shortWait)
	awaitRejection(ctx, t, closer, waiting, "REFERENCE_NOT_FOUND")
	replayed := submit(ctx, t, at, at.provider, key, payload)
	if replayed.status != http.StatusUnprocessableEntity {
		t.Fatalf("the replay of a closed wait = %d, want 422: %s", replayed.status, replayed.body)
	}
	refusal := replayed.refusal(t)
	if refusal.FailureCode != "REFERENCE_NOT_FOUND" || !refusal.IdempotentReplay {
		t.Fatalf("replayed refusal = %+v, want REFERENCE_NOT_FOUND with the marker", refusal)
	}
	assertRowsForKey(ctx, t, key, 1)
}

// shortWait is the configuration of a case about the deadline: the TTL is a
// moment instead of the fifteen minutes of the rule, so the case reads the
// outcome of an expiry rather than waiting one out.
var shortWait = map[string]string{"REFERENCE_TTL": "1s", "REFERENCE_INTERVAL": "50ms"}

// citing names the operation a body cites, and the round both belong to: a
// reversal and the operation it reverses have to close on the round.
func citing(cited, round string) map[string]any {
	return map[string]any{"referenceExternalTransactionId": cited, "roundId": round}
}

// named files a body under the identifier another one will cite, in the same
// round.
func named(external, round string) map[string]any {
	return map[string]any{"externalTransactionId": external, "roundId": round}
}

// both is the body that is named and cites at once, which is what a wait whose
// cited operation is itself waiting has to be.
func both(changes ...map[string]any) map[string]any {
	merged := map[string]any{}
	for _, change := range changes {
		for field, value := range change {
			merged[field] = value
		}
	}
	return merged
}

// placeBet submits one bet under the identifier a later operation will cite.
func placeBet(ctx context.Context, t *testing.T, at suite, wallet owner, amount string, changes map[string]any) {
	t.Helper()
	placed := submitBody(ctx, t, at, wallet.bet(amount, changes))
	if placed.status != http.StatusCreated {
		t.Fatalf("the bet to be cited = %d, want 201: %s", placed.status, placed.body)
	}
}

// assertWaits submits the body, checks the wait was accepted and recorded, and
// answers the identity of the row.
func assertWaits(ctx context.Context, t *testing.T, at suite, payload string) string {
	t.Helper()
	accepted := submitBody(ctx, t, at, payload)
	if accepted.status != http.StatusAccepted {
		t.Fatalf("the operation that waits = %d, want 202: %s", accepted.status, accepted.body)
	}
	answered := accepted.transaction(t)
	if answered.Status != "PENDING_REFERENCE" {
		t.Fatalf("status = %s, want PENDING_REFERENCE", answered.Status)
	}
	if got := accepted.header.Get("Location"); got != wagerRoute+"/"+answered.ID {
		t.Fatalf("location = %s, want %s", got, wagerRoute+"/"+answered.ID)
	}
	if answered.ObservedBalance.Amount != "" {
		t.Fatalf("observed balance = %s, want none: no commit closed the operation", answered.ObservedBalance.Amount)
	}
	return answered.ID
}

// assertProcessed submits the body, checks it concluded in that very commit and
// answers the identity of the row.
func assertProcessed(ctx context.Context, t *testing.T, at suite, payload, balance string) string {
	t.Helper()
	settled := submitBody(ctx, t, at, payload)
	if settled.status != http.StatusCreated {
		t.Fatalf("the operation that concludes = %d, want 201: %s", settled.status, settled.body)
	}
	answered := settled.transaction(t)
	if answered.Status != "PROCESSED" || answered.ObservedBalance.Amount != balance {
		t.Fatalf("settled %s with %s observed, want PROCESSED with %s", answered.Status, answered.ObservedBalance.Amount, balance)
	}
	return answered.ID
}

// pollFor bounds how long a case waits on the worker, and pollEvery is how often
// it looks. Both are far above the interval the suite runs the worker at, so a
// loaded machine does not turn a decision that came into a failure.
const (
	pollFor   = 30 * time.Second
	pollEvery = 25 * time.Millisecond
)

// awaitStatus polls the transaction until the worker has decided it, which is
// what a background component is read through: there is nothing to wait on but
// the outcome itself.
func awaitStatus(ctx context.Context, t *testing.T, at suite, id, want string) externalTransaction {
	t.Helper()
	deadline := time.Now().Add(pollFor)
	var last externalTransaction
	for time.Now().Before(deadline) {
		answered := read(ctx, t, at, at.provider, id)
		if answered.status != http.StatusOK {
			t.Fatalf("read of %s = %d, want 200: %s", id, answered.status, answered.body)
		}
		last = answered.transaction(t)
		if last.Status == want {
			return last
		}
		time.Sleep(pollEvery)
	}
	t.Fatalf("transaction %s is %s after %s, want %s", id, last.Status, pollFor, want)
	return last
}

func awaitRejection(ctx context.Context, t *testing.T, at suite, id, code string) {
	t.Helper()
	decided := awaitStatus(ctx, t, at, id, "REJECTED")
	if decided.FailureCode != code {
		t.Fatalf("failureCode of %s = %s, want %s", id, decided.FailureCode, code)
	}
	if decided.ObservedBalance.Amount != "" {
		t.Fatalf("observed balance = %s, want none: the deadline moved nothing", decided.ObservedBalance.Amount)
	}
}

// scheduling is the pair a case about the wait compares before and after: the
// instant of the next attempt and the deadline written on entry.
type scheduling struct {
	next     time.Time
	deadline time.Time
}

func schedule(ctx context.Context, t *testing.T, id string) scheduling {
	t.Helper()
	var found scheduling
	err := connect(ctx, t).QueryRow(ctx,
		"SELECT next_attempt_at, reference_deadline_at FROM wager_transactions WHERE id = $1", id).
		Scan(&found.next, &found.deadline)
	if err != nil {
		t.Fatalf("read the schedule of the wait = %v, want nil", err)
	}
	return found
}

// assertBalanceMatchesLastEntry is what the deferred trigger only checks at each
// commit: read back afterwards, it says the wallet ended where the ledger left
// it, however many workers wrote to it.
func assertBalanceMatchesLastEntry(ctx context.Context, t *testing.T, walletID string) {
	t.Helper()
	var stored, last int64
	err := connect(ctx, t).QueryRow(ctx, `
SELECT w.balance_cents,
       (SELECT e.balance_after_cents FROM ledger_entries e
         WHERE e.wallet_id = w.id ORDER BY e.sequence_number DESC, e.id DESC LIMIT 1)
  FROM wallets w WHERE w.id = $1`, walletID).Scan(&stored, &last)
	if err != nil {
		t.Fatalf("read the balance and the last entry = %v, want nil", err)
	}
	if stored != last {
		t.Fatalf("balance = %d and the last entry left %d, want the two to agree", stored, last)
	}
}
