//go:build integration

package scenarios

import (
	"context"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// The statement asks HTTP and the queue to share the guarantees of idempotency.
// The same operation under the same key arrives on both channels, in both
// orders, over a fleet that consumes the queue of the case and answers HTTP; and
// the same key with another amount on the other channel is the conflict. The
// provider of the token and the provider the sender is mapped to are the same,
// so the two arrivals are the same operation of the same provider.
func TestChannels_settleTheSameOperationOnceOverHTTPAndTheQueue(t *testing.T) {
	ctx, at := setUp(t)
	instances := at.launch(ctx, t, at.params.Instances, nil)
	db := connect(ctx, t)
	t.Run("the queue after HTTP answers the replay of what HTTP recorded", func(t *testing.T) {
		holder, key, message, bet := sameOperation(ctx, t, at, instances)
		assertSettledUnderKey(ctx, t, at, instances.at(1), key, bet)
		// A key conflict on the queue also leaves one transaction and removes the
		// message, so the series are what tell the replay from it.
		before := queueDuplicates(ctx, t, instances)
		at.queues.send(ctx, t, holder.id, bet.envelope(message, key))
		at.queues.awaitEmpty(ctx, t)
		assertCountedOnTheQueue(ctx, t, instances, duplicates{replays: before.replays + 1, conflicts: before.conflicts})
		t.Log("the fleet removed the message as the replay of what HTTP recorded")
		assertOneEffect(ctx, t, at, db, holder, key, message)
	})
	t.Run("HTTP after the queue answers the replay of what the queue recorded", func(t *testing.T) {
		holder, key, message, bet := sameOperation(ctx, t, at, instances)
		at.queues.send(ctx, t, holder.id, bet.envelope(message, key))
		at.queues.awaitEmpty(ctx, t)
		replayed := at.submit(ctx, t, instances.at(1), key, bet)
		if got := replayed.verdict(t); got != "200 PROCESSED replay=true" {
			t.Fatalf("the same operation over HTTP after the queue = %s, want 200 PROCESSED replay=true", got)
		}
		if got, want := replayed.outcome(t).ID, db.keyed(ctx, t, key); got != want {
			t.Errorf("replay over HTTP answered %s, want the %s the queue recorded", got, want)
		}
		t.Logf("HTTP answered %s with the transaction the queue recorded", replayed.verdict(t))
		assertOneEffect(ctx, t, at, db, holder, key, message)
	})
	t.Run("another amount on the queue after HTTP is removed as a key conflict", func(t *testing.T) {
		holder, key, message, bet := sameOperation(ctx, t, at, instances)
		assertSettledUnderKey(ctx, t, at, instances.at(1), key, bet)
		before := queueDuplicates(ctx, t, instances)
		at.queues.send(ctx, t, holder.id, otherAmount(bet).envelope(message, key))
		at.queues.awaitEmpty(ctx, t)
		assertCountedOnTheQueue(ctx, t, instances, duplicates{replays: before.replays, conflicts: before.conflicts + 1})
		t.Logf("the fleet removed the message as a key conflict, %v of them counted on the queue so far", before.conflicts+1)
		assertOneEffect(ctx, t, at, db, holder, key, message)
	})
	t.Run("another amount over HTTP after the queue is refused as a key conflict", func(t *testing.T) {
		holder, key, message, bet := sameOperation(ctx, t, at, instances)
		at.queues.send(ctx, t, holder.id, bet.envelope(message, key))
		at.queues.awaitEmpty(ctx, t)
		refused := at.submit(ctx, t, instances.at(1), key, otherAmount(bet))
		if got := refused.verdict(t); got != "422 IDEMPOTENCY_CONFLICT replay=false" {
			t.Fatalf("another amount over HTTP after the queue = %s, want 422 IDEMPOTENCY_CONFLICT replay=false", got)
		}
		t.Logf("HTTP answered %s", refused.verdict(t))
		assertOneEffect(ctx, t, at, db, holder, key, message)
	})
}

// sameOperation is one case of the channels: a wallet of its own, the key and the
// identity of the message the operation arrives under, and the bet itself.
func sameOperation(ctx context.Context, t *testing.T, at *scene, instances fleet) (owner, string, string, operation) {
	t.Helper()
	holder := at.open(ctx, t, instances.at(0), fundedBalance)
	return holder, newKey(), suiteenv.NewID(), holder.bet(betAmount)
}

// otherAmount is the same operation with another amount, which is another body
// under the same key and the same external identifier.
func otherAmount(bet operation) operation {
	return bet.with(map[string]any{"money": map[string]string{"amount": "30.00", "currency": Currency}})
}

// duplicates is what the fleet counted as a key arriving on the queue after it
// was recorded: with the same body, the replay, and with another, the conflict.
// The consumer answers both by removing the message.
type duplicates struct {
	replays   float64
	conflicts float64
}

func queueDuplicates(ctx context.Context, t *testing.T, instances fleet) duplicates {
	t.Helper()
	return duplicates{
		replays:   instances.tally(ctx, t, "wager_duplicates_total", map[string]string{"origin": "sqs", "reason": "replay"}),
		conflicts: instances.tally(ctx, t, "wager_duplicates_total", map[string]string{"origin": "sqs", "reason": "key_conflict"}),
	}
}

// assertCountedOnTheQueue checks the fleet counted the one arrival on the queue
// as the reason the case expects, and nothing more.
func assertCountedOnTheQueue(ctx context.Context, t *testing.T, instances fleet, want duplicates) {
	t.Helper()
	if got := queueDuplicates(ctx, t, instances); got != want {
		t.Errorf("duplicates counted on the queue = %+v, want %+v", got, want)
	}
}
