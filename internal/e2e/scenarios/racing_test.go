//go:build integration

package scenarios

import (
	"context"
	"fmt"
	"maps"
	"net/http"
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

	decided := together(ctx, t, asks)
	assertVerdicts(t, decided, racing(at.params, false))
	settled := assertRaceSettled(ctx, t, db, holder, at.params)

	again := together(ctx, t, asks)
	assertVerdicts(t, again, racing(at.params, true))
	assertSameTransactions(t, decided, again)
	if got := db.wallet(ctx, t, holder.id); got != settled {
		t.Errorf("wallet after the resubmission = %+v, want the %+v of the race", got, settled)
	}
	if got := db.entries(ctx, t, holder.id); got != 1+at.params.Fitting() {
		t.Errorf("entries after the resubmission = %d, want the %d of the race", got, 1+at.params.Fitting())
	}
}

// racing is the verdicts the race answers, derived from the parameters: the bets
// that fit settle and the rest are refused for the balance, and a resubmission
// answers the same two outcomes, each marked as a replay.
func racing(params Params, replay bool) map[string]int {
	fits := int(params.Fitting())
	settled := fmt.Sprintf("201 PROCESSED replay=%t", replay)
	if replay {
		settled = "200 PROCESSED replay=true"
	}
	want := map[string]int{settled: fits}
	if refused := params.RacingBets - fits; refused > 0 {
		want[fmt.Sprintf("422 INSUFFICIENT_FUNDS replay=%t", replay)] = refused
	}
	return want
}

func assertVerdicts(t *testing.T, answers []answer, want map[string]int) {
	t.Helper()
	got := map[string]int{}
	for _, answered := range answers {
		got[answered.verdict(t)]++
	}
	if !maps.Equal(got, want) {
		t.Fatalf("verdicts = %v, want %v", got, want)
	}
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
		t.Fatalf("wallet after the race = %+v, want %+v", got, want)
	}
	if got := db.debits(ctx, t, holder.id); got != params.Fitting() {
		t.Errorf("debits after the race = %d, want %d", got, params.Fitting())
	}
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
		if first.outcome(t).ID != again[index].outcome(t).ID {
			t.Errorf("bet %d replayed %s, want the %s it recorded", index, again[index].outcome(t).ID, first.outcome(t).ID)
		}
	}
}
