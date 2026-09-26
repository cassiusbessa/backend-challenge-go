//go:build integration

package scenarios

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// citedAmount is the bet the wait of the restart cites, and the refund that
// reverses it: another number than the bet under the key, so the balance tells
// which movements happened.
const citedAmount = "40.00"

// recordedEvents is what the first fleet leaves unpublished: the two of the
// opening, the two of the bet under the key, and the one of the wait.
const recordedEvents = 5

// The eighth scenario of the statement: the whole fleet goes down and another
// comes up over the same database, with nothing in memory carried across. What
// the first fleet recorded — the outcome under a key, a wait, events not yet
// published — is what every fleet after it answers, closes and publishes.
func TestRestart_keepsIdempotencyTheWaitAndTheLedger(t *testing.T) {
	ctx, at := setUp(t)
	// The relay of the first fleet sleeps, so the events it records are still
	// unpublished when it goes down.
	first := at.launch(ctx, t, at.params.Instances, map[string]string{"OUTBOX_INTERVAL": "1h"})
	db := connect(ctx, t)
	holder := at.open(ctx, t, first.at(0), fundedBalance)
	key, bet := newKey(), holder.bet(betAmount)
	settled := assertSettledUnderKey(ctx, t, at, first.at(1), key, bet)
	cited, round := "external-"+suiteenv.NewID(), "round-"+suiteenv.NewID()
	waiting := assertWaits(ctx, t, at, first.at(2), holder.of("REFUND", citedAmount, citing(cited, round)))
	pending := db.pendingEvents(ctx, t, holder.id)
	if len(pending) != recordedEvents {
		t.Fatalf("events pending when the fleet goes down = %d, want the %d it recorded", len(pending), recordedEvents)
	}
	first.stop(ctx, t)
	t.Logf("the first fleet recorded the bet under the key, a wait and %d unpublished events, and went down", len(pending))

	recording, endpoint := front(t, snsEndpoint(), Faculties{RecordPublishes: true})
	for restart := range at.params.Restarts {
		t.Logf("restart %d of %d", restart+1, at.params.Restarts)
		next := at.launch(ctx, t, at.params.Instances, map[string]string{"SNS_ENDPOINT": endpoint})
		assertKeySurvives(ctx, t, at, db, next, key, bet, settled)
		if restart == 0 {
			assertCreated(ctx, t, at, next.at(2), holder.of("BET", citedAmount, named(cited, round)))
			db.awaitDecision(ctx, t, waiting, decision{status: "PROCESSED"})
			awaitPublished(ctx, t, db, holder, recording, pending)
		}
		assertReconciled(ctx, t, at, next.at(restart), holder)
		next.stop(ctx, t)
	}
}

func assertSettledUnderKey(ctx context.Context, t *testing.T, at *scene, in *instance, key string, bet operation) outcome {
	t.Helper()
	settled := at.submit(ctx, t, in, key, bet)
	if settled.status != http.StatusCreated {
		t.Fatalf("the bet under the key = %d, want 201: %s", settled.status, settled.body)
	}
	return settled.outcome(t)
}

// assertKeySurvives asks a fleet that never saw the key for it: the same body
// answers the outcome recorded before the restart, with the balance observed
// back then, and another body is the conflict, with no row written.
func assertKeySurvives(ctx context.Context, t *testing.T, at *scene, db store, next fleet, key string, bet operation, settled outcome) {
	t.Helper()
	replayed := at.submit(ctx, t, next.at(0), key, bet)
	want := settled
	want.IdempotentReplay = true
	if replayed.status != http.StatusOK || replayed.outcome(t) != want {
		t.Fatalf("replay after the restart = %d %s, want 200 with %+v", replayed.status, replayed.body, want)
	}
	other := bet.with(map[string]any{"money": map[string]string{"amount": "30.00", "currency": Currency}})
	refused := at.submit(ctx, t, next.at(1), key, other)
	if got := refused.verdict(t); got != "422 IDEMPOTENCY_CONFLICT replay=false" {
		t.Fatalf("another body under the key after the restart = %s, want 422 IDEMPOTENCY_CONFLICT replay=false", got)
	}
	if got := db.forKey(ctx, t, key); got != 1 {
		t.Errorf("transactions under the key after the restart = %d, want 1", got)
	}
	t.Logf("the key replayed %s with the %s observed before the restart, and another body was the conflict", settled.TransactionID, settled.Balance.Amount)
}

// awaitPublished waits until the wallet has nothing left to publish, and then
// checks each event the first fleet left behind went out through the new fleet
// under the eventId it was recorded with.
func awaitPublished(ctx context.Context, t *testing.T, db store, holder owner, recording *Proxy, pending []string) {
	t.Helper()
	until(ctx, t, "the new fleet to publish every event of the wallet", func() bool {
		return len(db.pendingEvents(ctx, t, holder.id)) == 0
	})
	sent := recording.Published()
	for _, eventID := range pending {
		if sent[eventID] < 1 {
			t.Errorf("event %s recorded before the restart went out %d times, want at least once", eventID, sent[eventID])
		}
	}
	t.Logf("the %d events the first fleet left went out through the new fleet under the eventId they were recorded with", len(pending))
}

// assertReconciled reads the reconciliation of the wallet: consistent, at the
// balance the bet under the key, the cited bet and the refund that reverses it
// leave over the opening.
func assertReconciled(ctx context.Context, t *testing.T, at *scene, in *instance, holder owner) {
	t.Helper()
	answered := call(ctx, t, request{method: http.MethodGet, url: in.base + "/wallets/" + holder.id + "/reconciliation", bearer: at.internal})
	if answered.status != http.StatusOK {
		t.Fatalf("reconciliation = %d, want 200: %s", answered.status, answered.body)
	}
	var report reconciliation
	if err := json.Unmarshal(answered.body, &report); err != nil {
		t.Fatalf("unmarshal the reconciliation = %v, want nil: %s", err, answered.body)
	}
	if !report.Consistent || report.StoredBalance.Amount != "975.00" || report.LedgerBalance.Amount != "975.00" {
		t.Errorf("reconciliation = %+v, want consistent at 975.00 on both sides", report)
	}
	t.Logf("reconciliation: consistent=%t, stored %s, ledger %s", report.Consistent, report.StoredBalance.Amount, report.LedgerBalance.Amount)
}
