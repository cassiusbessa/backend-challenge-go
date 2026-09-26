//go:build integration

package scenarios

import (
	"context"
	"net/http"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// winAmount is what the win a rollback reverses credits: another number than the
// bet, so a reversal of the wrong amount could not pass for the right one.
const winAmount = "50.00"

// The seventh scenario of the statement: a reversal that arrives before the
// operation it cites. It is accepted and waits, recorded by one instance; the
// cited operation arrives on another, and the worker of any instance settles the
// reversal. When the cited operation never arrives, the deadline refuses it.
func TestEarlyReversal_waitsAndThenResolvesOrExpires(t *testing.T) {
	ctx, at := setUp(t)
	t.Run("a refund that arrives before its bet settles once the bet does", func(t *testing.T) {
		instances := at.launch(ctx, t, at.params.Instances, nil)
		db := connect(ctx, t)
		holder := at.open(ctx, t, instances.at(0), fundedBalance)
		cited, round := "external-"+suiteenv.NewID(), "round-"+suiteenv.NewID()
		waiting := assertWaits(ctx, t, at, instances.at(0), holder.of("REFUND", betAmount, citing(cited, round)))
		assertCreated(ctx, t, at, instances.at(1), holder.of("BET", betAmount, named(cited, round)))
		db.awaitDecision(ctx, t, waiting, decision{status: "PROCESSED"})
		assertBackWhereItOpened(ctx, t, db, holder, 3)
	})
	t.Run("a rollback that arrives before its win settles once the win does", func(t *testing.T) {
		instances := at.launch(ctx, t, at.params.Instances, nil)
		db := connect(ctx, t)
		holder := at.open(ctx, t, instances.at(0), fundedBalance)
		cited, round := "external-"+suiteenv.NewID(), "round-"+suiteenv.NewID()
		waiting := assertWaits(ctx, t, at, instances.at(0), holder.of("ROLLBACK", winAmount, citing(cited, round)))
		assertCreated(ctx, t, at, instances.at(1), holder.of("WIN", winAmount, named(cited, round)))
		db.awaitDecision(ctx, t, waiting, decision{status: "PROCESSED"})
		assertBackWhereItOpened(ctx, t, db, holder, 3)
	})
	t.Run("a refund and a rollback whose cited operations never come expire", func(t *testing.T) {
		// The deadline is written when the wait is recorded, so every instance
		// that could record one measures the TTL short.
		instances := at.launch(ctx, t, at.params.Instances, map[string]string{"REFERENCE_TTL": "1s"})
		db := connect(ctx, t)
		holder := at.open(ctx, t, instances.at(0), fundedBalance)
		expired := decision{status: "REJECTED", failureCode: "REFERENCE_NOT_FOUND"}
		refund := assertWaits(ctx, t, at, instances.at(0), holder.of("REFUND", betAmount, citing("external-"+suiteenv.NewID(), "round-"+suiteenv.NewID())))
		rollback := assertWaits(ctx, t, at, instances.at(1), holder.of("ROLLBACK", winAmount, citing("external-"+suiteenv.NewID(), "round-"+suiteenv.NewID())))
		db.awaitDecision(ctx, t, refund, expired)
		db.awaitDecision(ctx, t, rollback, expired)
		assertBackWhereItOpened(ctx, t, db, holder, 1)
	})
}

// citing names the operation a reversal cites, and the round both belong to: a
// reversal and what it reverses have to close on the round.
func citing(cited, round string) map[string]any {
	return map[string]any{"referenceExternalTransactionId": cited, "roundId": round}
}

// named files an operation under the identifier a reversal cites, in its round.
func named(external, round string) map[string]any {
	return map[string]any{"externalTransactionId": external, "roundId": round}
}

// assertWaits submits the operation, checks it was accepted as a wait with no
// balance observed, and answers the identity of the row.
func assertWaits(ctx context.Context, t *testing.T, at *scene, in *instance, op operation) string {
	t.Helper()
	accepted := at.submit(ctx, t, in, newKey(), op)
	if accepted.status != http.StatusAccepted {
		t.Fatalf("the operation that waits = %d, want 202: %s", accepted.status, accepted.body)
	}
	waiting := accepted.outcome(t)
	if waiting.Status != "PENDING_REFERENCE" || waiting.ObservedBalance.Amount != "" {
		t.Fatalf("wait = %+v, want PENDING_REFERENCE with no balance observed", waiting)
	}
	t.Logf("%s %s recorded as PENDING_REFERENCE by %s", op["kind"], waiting.ID, in.base)
	return waiting.ID
}

func assertCreated(ctx context.Context, t *testing.T, at *scene, in *instance, op operation) {
	t.Helper()
	created := at.submit(ctx, t, in, newKey(), op)
	if created.status != http.StatusCreated {
		t.Fatalf("the cited operation = %d, want 201: %s", created.status, created.body)
	}
	t.Logf("%s %s settled by %s", op["kind"], created.outcome(t).ID, in.base)
}

// assertBackWhereItOpened checks the balance is the one of the opening, at the
// version and with the entries the movements in between left: a reversal that
// settled undid what it cited, and one that expired moved nothing.
func assertBackWhereItOpened(ctx context.Context, t *testing.T, db store, holder owner, version int64) {
	t.Helper()
	if got := db.wallet(ctx, t, holder.id); got != (stored{cents: 100000, version: version}) {
		t.Errorf("wallet = %v, want 1000.00 at version %d", got, version)
	}
	if got := db.entries(ctx, t, holder.id); got != version {
		t.Errorf("entries = %d, want %d, one per version", got, version)
	}
	t.Logf("wallet back at the 1000.00 it opened with, at version %d", version)
}
