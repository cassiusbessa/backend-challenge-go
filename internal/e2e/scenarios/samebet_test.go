//go:build integration

package scenarios

import (
	"context"
	"net/http"
	"slices"
	"testing"
)

// The amounts of the scenarios no parameter sizes. The opening covers any number
// of copies of the same bet, because only one of them debits.
const (
	fundedBalance = "1000.00"
	betAmount     = "25.00"
)

// settledEvents is what one processed bet leaves in the outbox, in the order the
// store reads them: the movement of the balance and the outcome.
var settledEvents = []string{"WagerTransactionProcessed", "WalletBalanceChanged"}

// The first scenario of the statement: one bet, under one key and one body,
// arriving many times at once over every instance. The lock of the wallet
// serializes them, the unique index of the key picks the one that settles, and
// every other arrival answers what that one recorded.
func TestSameBet_debitsOnceWhenItArrivesManyTimesAtOnce(t *testing.T) {
	ctx, at := setUp(t)
	instances := at.launch(ctx, t, at.params.Instances, nil)
	db := connect(ctx, t)
	holder := at.open(ctx, t, instances.at(0), fundedBalance)
	key := newKey()
	bet := holder.bet(betAmount)
	asks := make([]request, at.params.SameBetCopies)
	for index := range asks {
		asks[index] = at.wager(instances.at(index), key, bet)
	}

	t.Logf("sent %d copies of one bet under one key at once", len(asks))
	settled := assertOneSettledAndTheRestReplayed(t, together(ctx, t, asks))
	t.Logf("1 settled as %s with %s observed, %d answered the replay of it", settled.ID, settled.ObservedBalance.Amount, len(asks)-1)
	assertDebitedOnce(ctx, t, db, holder, key, settled)
	assertEveryInstanceTookItsShare(ctx, t, instances, at.params.SameBetCopies)
}

// assertOneSettledAndTheRestReplayed answers the transaction that settled, after
// checking that every other answer is the replay of it: the same identifier and
// the same observed balance, marked.
func assertOneSettledAndTheRestReplayed(t *testing.T, answers []answer) outcome {
	t.Helper()
	settled, replayed := byStatus(t, answers)
	if len(settled) != 1 {
		t.Fatalf("arrivals that settled = %d, want exactly 1", len(settled))
	}
	want := settled[0]
	want.IdempotentReplay = true
	for _, each := range replayed {
		if each != want {
			t.Fatalf("replay = %+v, want %+v", each, want)
		}
	}
	return settled[0]
}

// byStatus splits the answers into the ones that created the transaction and
// the ones that found it recorded.
func byStatus(t *testing.T, answers []answer) (created, found []outcome) {
	t.Helper()
	for _, answered := range answers {
		switch answered.status {
		case http.StatusCreated:
			created = append(created, answered.outcome(t))
		case http.StatusOK:
			found = append(found, answered.outcome(t))
		default:
			t.Fatalf("status of an arrival = %d, want 201 or 200: %s", answered.status, answered.body)
		}
	}
	return created, found
}

// assertDebitedOnce pins the single effect: one row under the key, one debit, the
// balance fallen once by the bet and the version risen once, and the events of
// the transaction in the outbox once.
func assertDebitedOnce(ctx context.Context, t *testing.T, db store, holder owner, key string, settled outcome) {
	t.Helper()
	if settled.ObservedBalance.Amount != "975.00" {
		t.Errorf("observed balance = %s, want the 975.00 left by the one debit", settled.ObservedBalance.Amount)
	}
	if got := db.forKey(ctx, t, key); got != 1 {
		t.Errorf("transactions under the key = %d, want 1", got)
	}
	if got := db.debits(ctx, t, holder.id); got != 1 {
		t.Errorf("debits of the wallet = %d, want 1", got)
	}
	wallet := db.wallet(ctx, t, holder.id)
	if wallet != (stored{cents: 97500, version: 2}) {
		t.Errorf("wallet = %v, want 975.00 at version 2", wallet)
	}
	assertEventsOnce(ctx, t, db, settled.ID)
	t.Logf("wallet: %v, with one transaction under the key and one debit", wallet)
}

func assertEventsOnce(ctx context.Context, t *testing.T, db store, transactionID string) {
	t.Helper()
	got := db.eventsOf(ctx, t, transactionID)
	if !slices.Equal(got, settledEvents) {
		t.Errorf("outbox rows of the transaction = %v, want %v once each", got, settledEvents)
	}
	t.Logf("outbox of %s: %v", transactionID, got)
}

// assertEveryInstanceTookItsShare reads what each instance counted: every one of
// them decided some of the arrivals, and together they decided all of them. The
// series are the proof the arrivals were spread, and not only that the requests
// were addressed that way.
func assertEveryInstanceTookItsShare(ctx context.Context, t *testing.T, instances fleet, copies int) {
	t.Helper()
	var total float64
	shares := make([]float64, 0, len(instances))
	for index, each := range instances {
		decided := each.tally(ctx, t, "wager_settlements_total", map[string]string{"origin": "http", "kind": "BET"}) +
			each.tally(ctx, t, "wager_duplicates_total", map[string]string{"origin": "http", "reason": "replay"})
		if decided < 1 {
			t.Errorf("arrivals decided by instance %d = %v, want at least 1", index, decided)
		}
		shares = append(shares, decided)
		total += decided
	}
	if total != float64(copies) {
		t.Errorf("arrivals decided by the fleet = %v, want the %d sent", total, copies)
	}
	t.Logf("arrivals each instance decided, by its own series: %v, %v in all", shares, total)
}
