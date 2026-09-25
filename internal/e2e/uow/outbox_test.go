//go:build integration

// The outbox side of one commit against a real PostgreSQL: the event row lives
// or dies with the balance that produced it, and the relay only ever reads a row
// that was committed.
package uow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

func TestInsert_keepsTheEventOfACommitThatWentThrough(t *testing.T) {
	ctx, _, unit := open(t)
	opened := opening(t)
	recorded := processedEvent(t, opened)
	err := unit.Within(ctx, func(tx storage.Tx) error {
		if err := writeSet(ctx, tx, opened); err != nil {
			return err
		}
		return tx.Outbox().Insert(ctx, recorded)
	})
	if err != nil {
		t.Fatalf("Within = %v, want nil", err)
	}
	assertOutboxRows(ctx, t, opened.wallet.ID().String(), 1)
	assertStoredType(ctx, t, recorded.ID().String(), string(event.TypeProcessed))
}

func TestInsert_takesTheEventDownWithTheTransactionThatFailed(t *testing.T) {
	ctx, _, unit := open(t)
	opened := opening(t)
	broken := errors.New("record the outcome: interrupted")
	err := unit.Within(ctx, func(tx storage.Tx) error {
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
}

func writeSet(ctx context.Context, tx storage.Tx, opened set) error {
	if err := tx.Wallets().Insert(ctx, opened.wallet); err != nil {
		return err
	}
	if err := tx.Transactions().Insert(ctx, opened.transaction); err != nil {
		return err
	}
	return tx.Entries().Insert(ctx, opened.entry)
}

func processedEvent(t *testing.T, opened set) event.Envelope {
	t.Helper()
	return processedEventAt(t, opened, opened.transaction.UpdatedAt())
}

func processedEventAt(t *testing.T, opened set, at time.Time) event.Envelope {
	t.Helper()
	built, err := event.NewProcessed(event.Spec{
		ID:          eventOf(t, newID()),
		AggregateID: opened.wallet.ID(),
		At:          at,
	}, opened.transaction)
	if err != nil {
		t.Fatalf("event.NewProcessed = %v, want nil", err)
	}
	return built
}

func eventOf(t *testing.T, text string) identity.EventID {
	t.Helper()
	id, err := identity.ParseEventID(text)
	if err != nil {
		t.Fatalf("ParseEventID = %v, want nil", err)
	}
	return id
}

func assertOutboxRows(ctx context.Context, t *testing.T, walletID string, want int64) {
	t.Helper()
	assertCount(ctx, t, connect(ctx, t), "outbox events", "SELECT count(*) FROM outbox_events WHERE wallet_id = $1", walletID, want)
}

// assertStoredType reads the type off the row and off the payload, which is
// what says the column and the bytes the broker gets name the same event.
func assertStoredType(ctx context.Context, t *testing.T, eventID, want string) {
	t.Helper()
	var column, inPayload string
	query := `SELECT event_type, payload ->> 'eventType' FROM outbox_events WHERE event_id = $1`
	if err := connect(ctx, t).QueryRow(ctx, query, eventID).Scan(&column, &inPayload); err != nil {
		t.Fatalf("read the stored event = %v, want nil", err)
	}
	if column != want || inPayload != want {
		t.Fatalf("stored type = %s in the column and %s in the payload, want %s in both", column, inPayload, want)
	}
}
