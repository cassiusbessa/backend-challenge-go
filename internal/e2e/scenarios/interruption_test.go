//go:build integration

package scenarios

import (
	"context"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// The fifth scenario of the statement: an instance commits the outcome of a
// message and dies before the removal reaches the broker. A proxy in front of it
// refuses every removal, and the case stops it once one was refused, which is the
// death between the commit and the delete without a race against a clock. The
// message comes back to another instance, which finds it in the inbox, moves
// nothing and removes it.
func TestInterruption_changesNothingWhenTheRemovalNeverReachedTheBroker(t *testing.T) {
	ctx, at := setUp(t)
	refusing, endpoint := front(t, sqsEndpoint(), Faculties{RefuseDeletes: true})
	doomed := at.boot(ctx, t, map[string]string{"SQS_ENDPOINT": endpoint})
	db := connect(ctx, t)
	holder := at.open(ctx, t, doomed, fundedBalance)
	message, key := suiteenv.NewID(), newKey()
	at.queues.send(ctx, t, holder.id, holder.bet(betAmount).envelope(message, key))
	until(ctx, t, "the doomed instance to commit the bet", func() bool {
		return db.forKey(ctx, t, key) == 1
	})
	until(ctx, t, "the proxy to refuse the removal of the message", func() bool {
		return refusing.Refused() >= 1
	})
	committed := db.wallet(ctx, t, holder.id)
	t.Logf("the doomed instance %s committed the bet, leaving the wallet with %v, and the proxy refused %d removal of it", doomed.base, committed, refusing.Refused())
	doomed.stop(ctx, t)
	stopped := time.Now()

	survivor := at.boot(ctx, t, nil)
	at.queues.awaitEmpty(ctx, t)
	redeliveries := survivor.tally(ctx, t, "wager_duplicates_total", map[string]string{"origin": "sqs", "reason": "redelivery"})
	if redeliveries < 1 {
		t.Errorf("redeliveries counted by the survivor = %v, want at least 1", redeliveries)
	}
	t.Logf("the survivor %s took the redelivery and emptied the queue %s after the stop, counting %v redelivery", survivor.base, since(stopped), redeliveries)
	if committed != (stored{cents: 97500, version: 2}) {
		t.Errorf("wallet at the first commit = %v, want 975.00 at version 2", committed)
	}
	assertOneEffect(ctx, t, at, db, holder, key, message)
}

// assertOneEffect pins what one operation arriving more than once leaves: one
// transaction under the key with its events once, one debit over the opening,
// the one inbox row of the message, and nothing abandoned.
func assertOneEffect(ctx context.Context, t *testing.T, at *scene, db store, holder owner, key, message string) {
	t.Helper()
	if got := db.forKey(ctx, t, key); got != 1 {
		t.Fatalf("transactions under the key = %d, want 1", got)
	}
	if got := db.wallet(ctx, t, holder.id); got != (stored{cents: 97500, version: 2}) {
		t.Errorf("wallet after every arrival = %v, want 975.00 at version 2", got)
	}
	if got := db.entries(ctx, t, holder.id); got != 2 {
		t.Errorf("entries after every arrival = %d, want the opening and the one debit", got)
	}
	if got := db.inbox(ctx, t, message); got != 1 {
		t.Errorf("inbox rows of the message = %d, want 1", got)
	}
	if got := at.queues.depth(ctx, t, at.queues.dead); got != 0 {
		t.Errorf("messages on the dead-letter queue = %d, want 0", got)
	}
	assertEventsOnce(ctx, t, db, db.keyed(ctx, t, key))
	t.Logf("one transaction under the key, one debit, one inbox row for the message, nothing on the dead-letter queue")
}
