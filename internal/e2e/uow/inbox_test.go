//go:build integration

// The inbox side of one commit against a real PostgreSQL: the row of the message
// lives or dies with the balance it caused, and the unicity of the pair is what
// decides the redelivery.
package uow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
)

// ingressConsumer is the consumer of the queue, as the row names it.
const ingressConsumer = "wager-ingress"

func TestInsert_keepsTheInboxRowOfACommitThatWentThrough(t *testing.T) {
	ctx, _, unit := open(t)
	opened := opening(t)
	arrived := message(t)
	err := unit.Within(ctx, func(tx storage.Tx) error {
		if _, err := tx.Inbox().Insert(ctx, arrived); err != nil {
			return err
		}
		if err := writeSet(ctx, tx, opened); err != nil {
			return err
		}
		return tx.Outbox().Insert(ctx, processedEvent(t, opened))
	})
	if err != nil {
		t.Fatalf("Within = %v, want nil", err)
	}
	assertRows(ctx, t, opened.wallet.ID().String(), 1, 1, 1)
	assertOutboxRows(ctx, t, opened.wallet.ID().String(), 1)
	assertInboxRows(ctx, t, arrived.MessageID, 1)
}

func TestInsert_takesTheInboxRowDownWithTheTransactionThatFailed(t *testing.T) {
	ctx, _, unit := open(t)
	opened := opening(t)
	arrived := message(t)
	broken := errors.New("record the outcome: interrupted")
	err := unit.Within(ctx, func(tx storage.Tx) error {
		if _, err := tx.Inbox().Insert(ctx, arrived); err != nil {
			return err
		}
		if err := writeSet(ctx, tx, opened); err != nil {
			return err
		}
		if err := tx.Outbox().Insert(ctx, processedEvent(t, opened)); err != nil {
			return err
		}
		return broken
	})
	if !errors.Is(err, broken) {
		t.Fatalf("Within = %v, want %v", err, broken)
	}
	assertRows(ctx, t, opened.wallet.ID().String(), 0, 0, 0)
	assertOutboxRows(ctx, t, opened.wallet.ID().String(), 0)
	assertInboxRows(ctx, t, arrived.MessageID, 0)
}

// The refusal of the unicity answers the row that won it and leaves the
// transaction able to go on, which is what lets the caller compare the hash and
// decide what kind of redelivery this is.
func TestInsert_answersTheRecordedRowAndKeepsTheTransactionUsable(t *testing.T) {
	ctx, _, unit := open(t)
	first := message(t)
	if err := unit.Within(ctx, func(tx storage.Tx) error {
		_, err := tx.Inbox().Insert(ctx, first)
		return err
	}); err != nil {
		t.Fatalf("first commit = %v, want nil", err)
	}
	again := first
	again.BodyHash = "hash-of-another-body"
	opened := opening(t)
	var recorded storage.Message
	var refusal error
	err := unit.Within(ctx, func(tx storage.Tx) error {
		recorded, refusal = tx.Inbox().Insert(ctx, again)
		// The transaction goes on after the refusal, which is the whole point of
		// the savepoint: a read here would fail on an aborted transaction.
		return writeSet(ctx, tx, opened)
	})
	if err != nil {
		t.Fatalf("Within after the refusal = %v, want nil", err)
	}
	if !errors.Is(refusal, storage.ErrMessageRecorded) {
		t.Fatalf("second insert = %v, want %v", refusal, storage.ErrMessageRecorded)
	}
	if recorded.BodyHash != first.BodyHash {
		t.Fatalf("hash answered = %q, want the recorded %q", recorded.BodyHash, first.BodyHash)
	}
	assertRows(ctx, t, opened.wallet.ID().String(), 1, 1, 1)
	assertInboxRows(ctx, t, first.MessageID, 1)
}

func message(t *testing.T) storage.Message {
	t.Helper()
	return storage.Message{
		Consumer:  ingressConsumer,
		MessageID: newID(),
		BodyHash:  "hash-of-the-body",
		At:        time.Date(2026, time.September, 25, 12, 0, 0, 0, time.UTC),
	}
}

func assertInboxRows(ctx context.Context, t *testing.T, messageID string, want int64) {
	t.Helper()
	const query = "SELECT count(*) FROM inbox_messages WHERE message_id = $1"
	assertCount(ctx, t, connect(ctx, t), "inbox rows", query, messageID, want)
}
