//go:build integration

package scenarios

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// The second scenario of the statement: bets over one wallet that do not all fit
// its balance, each under a key of its own, arriving at once on different
// instances. The lock hands the wallet to one of them at a time, and each is
// decided against the balance the ones before it already committed. Sent again,
// each answers what it recorded and nothing moves.
func TestRacingBets_settleAgainstTheBalanceAlreadyCommitted(t *testing.T) {
	ctx, at := setUp(t)
	instances := at.launch(ctx, t, at.params.Instances, nil)
	db := connect(ctx, t)
	holder := at.open(ctx, t, instances.at(0), at.params.OpeningBalance.Amount())
	asks := make([]request, at.params.RacingBets)
	for index := range asks {
		asks[index] = at.wager(instances.at(index), newKey(), holder.bet(at.params.RacingAmount.Amount()))
	}

	t.Logf("%d bets of %s at once over a wallet of %s: %d fit", len(asks), at.params.RacingAmount, at.params.OpeningBalance, at.params.Fitting())
	decided := together(ctx, t, asks)
	assertVerdicts(t, "race", decided, racing(at.params, false))
	settled := assertRaceSettled(ctx, t, db, holder, at.params)

	again := together(ctx, t, asks)
	assertVerdicts(t, "resubmission", again, racing(at.params, true))
	assertSameTransactions(t, decided, again)
	if got := db.wallet(ctx, t, holder.id); got != settled {
		t.Errorf("wallet after the resubmission = %v, want the %v of the race", got, settled)
	}
	if got := db.entries(ctx, t, holder.id); got != 1+at.params.Fitting() {
		t.Errorf("entries after the resubmission = %d, want the %d of the race", got, 1+at.params.Fitting())
	}
}

// racing is the verdicts the race answers, derived from the parameters: the bets
// that fit settle and the rest are refused for the balance, and a resubmission
// answers the same two outcomes, each marked as a replay. Read refuses a race in
// which no bet fits or every bet does, so both outcomes are always there.
func racing(params Params, replay bool) map[string]int {
	fits := int(params.Fitting())
	settled, refused := "201 PROCESSED replay=false", "422 INSUFFICIENT_FUNDS replay=false"
	if replay {
		settled, refused = "200 PROCESSED replay=true", "422 INSUFFICIENT_FUNDS replay=true"
	}
	return map[string]int{settled: fits, refused: params.RacingBets - fits}
}

func assertVerdicts(t *testing.T, round string, answers []answer, want map[string]int) {
	t.Helper()
	got := map[string]int{}
	for _, answered := range answers {
		got[answered.verdict(t)]++
	}
	if !maps.Equal(got, want) {
		t.Fatalf("verdicts of the %s = %v, want %v", round, got, want)
	}
	t.Logf("verdicts of the %s: %s", round, tallied(got))
}

// tallied writes how many answers each verdict had, in a stable order.
func tallied(counts map[string]int) string {
	verdicts := slices.Sorted(maps.Keys(counts))
	written := make([]string, 0, len(verdicts))
	for _, verdict := range verdicts {
		written = append(written, fmt.Sprintf("%d× %s", counts[verdict], verdict))
	}
	return strings.Join(written, ", ")
}

// assertRaceSettled checks the wallet against what the parameters derive — the
// balance less the amount once per bet that fit, the version risen once per bet
// that fit, one debit each — and answers it.
func assertRaceSettled(ctx context.Context, t *testing.T, db store, holder owner, params Params) stored {
	t.Helper()
	remaining, err := params.Remaining()
	if err != nil {
		t.Fatalf("Remaining = %v, want nil", err)
	}
	want := stored{cents: remaining.Cents(), version: 1 + params.Fitting()}
	if got := db.wallet(ctx, t, holder.id); got != want {
		t.Fatalf("wallet after the race = %v, want %v", got, want)
	}
	debits := db.debits(ctx, t, holder.id)
	if debits != params.Fitting() {
		t.Errorf("debits after the race = %d, want %d", debits, params.Fitting())
	}
	t.Logf("wallet after the race: %v, debits: %d", want, debits)
	return want
}

// assertSameTransactions pins every replay of a bet that settled to the
// transaction the race recorded for it. A refusal answers the token and no
// identifier, so the verdicts are what pin the refused ones.
func assertSameTransactions(t *testing.T, decided, again []answer) {
	t.Helper()
	for index, first := range decided {
		if first.status != http.StatusCreated {
			continue
		}
		if first.outcome(t).TransactionID != again[index].outcome(t).TransactionID {
			t.Errorf("bet %d replayed %s, want the %s it recorded", index, again[index].outcome(t).TransactionID, first.outcome(t).TransactionID)
		}
	}
}
