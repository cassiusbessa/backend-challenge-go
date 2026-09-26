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
	t.Run("over HTTP first and then on the queue", func(t *testing.T) {
		holder, key, message, bet := sameOperation(ctx, t, at, instances)
		assertSettledUnderKey(ctx, t, at, instances.at(1), key, bet)
		at.queues.send(ctx, t, holder.id, bet.envelope(message, key))
		at.queues.awaitEmpty(ctx, t)
		assertOneEffect(ctx, t, at, db, holder, key, message)
	})
	t.Run("on the queue first and then over HTTP", func(t *testing.T) {
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
		assertOneEffect(ctx, t, at, db, holder, key, message)
	})
	t.Run("another amount on the queue after HTTP", func(t *testing.T) {
		holder, key, message, bet := sameOperation(ctx, t, at, instances)
		assertSettledUnderKey(ctx, t, at, instances.at(1), key, bet)
		conflicts := keyConflicts(ctx, t, instances)
		at.queues.send(ctx, t, holder.id, otherAmount(bet).envelope(message, key))
		at.queues.awaitEmpty(ctx, t)
		if got := keyConflicts(ctx, t, instances); got != conflicts+1 {
			t.Errorf("key conflicts counted on the queue = %v, want the %v before plus the one sent", got, conflicts)
		}
		assertOneEffect(ctx, t, at, db, holder, key, message)
	})
	t.Run("another amount over HTTP after the queue", func(t *testing.T) {
		holder, key, message, bet := sameOperation(ctx, t, at, instances)
		at.queues.send(ctx, t, holder.id, bet.envelope(message, key))
		at.queues.awaitEmpty(ctx, t)
		refused := at.submit(ctx, t, instances.at(1), key, otherAmount(bet))
		if got := refused.verdict(t); got != "422 IDEMPOTENCY_CONFLICT replay=false" {
			t.Fatalf("another amount over HTTP after the queue = %s, want 422 IDEMPOTENCY_CONFLICT replay=false", got)
		}
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

// keyConflicts is what the fleet counted as a key arriving on the queue with
// another body: the conflict the consumer answers by removing the message.
func keyConflicts(ctx context.Context, t *testing.T, instances fleet) float64 {
	t.Helper()
	return instances.tally(ctx, t, "wager_duplicates_total", map[string]string{"origin": "sqs", "reason": "key_conflict"})
}
