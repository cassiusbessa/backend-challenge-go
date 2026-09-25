package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestRecord_writesWhatTheCommitEmittedInOrder(t *testing.T) {
	t.Parallel()
	rows := &outboxRows{}
	if err := Record(context.Background(), &txStub{outbox: rows}, commitOf(t)); err != nil {
		t.Fatalf("Record = %v, want nil", err)
	}
	if len(rows.written) != 1 {
		t.Fatalf("events written = %d, want 1", len(rows.written))
	}
	if rows.written[0].Type() != event.TypeProcessed {
		t.Fatalf("event = %s, want %s", rows.written[0].Type(), event.TypeProcessed)
	}
}

func TestRecord_writesNothingWhenTheCommitRecordedNoTransaction(t *testing.T) {
	t.Parallel()
	rows := &outboxRows{}
	if err := Record(context.Background(), &txStub{outbox: rows}, event.Commit{}); err != nil {
		t.Fatalf("Record of a commit with no transaction = %v, want nil", err)
	}
	if len(rows.written) != 0 {
		t.Fatalf("events written for a commit with no transaction = %d, want none", len(rows.written))
	}
}

func TestRecord_answersTheFailureOfTheRowItCouldNotWrite(t *testing.T) {
	t.Parallel()
	refused := errors.New("insert outbox event: interrupted")
	err := Record(context.Background(), &txStub{outbox: &outboxRows{refuse: refused}}, commitOf(t))
	if !errors.Is(err, refused) {
		t.Fatalf("Record over a refusing outbox = %v, want %v", err, refused)
	}
}

// txStub hands out the outbox and nothing else: Record reaches no other
// repository, and a nil here is what says so.
type txStub struct {
	outbox Outbox
}

func (t *txStub) Wallets() Wallets           { return nil }
func (t *txStub) Transactions() Transactions { return nil }
func (t *txStub) Entries() Entries           { return nil }
func (t *txStub) Outbox() Outbox             { return t.outbox }

type outboxRows struct {
	written []event.Envelope
	refuse  error
}

func (r *outboxRows) Insert(_ context.Context, envelope event.Envelope) error {
	if r.refuse != nil {
		return r.refuse
	}
	r.written = append(r.written, envelope)
	return nil
}

// at is the instant every case of this file is stamped with.
var at = time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)

func commitOf(t *testing.T) event.Commit {
	t.Helper()
	return event.Commit{
		OutcomeID:   idOf(t, identity.ParseEventID, "019974a4-0000-7000-8000-00000000f001"),
		BalanceID:   idOf(t, identity.ParseEventID, "019974a4-0000-7000-8000-00000000f002"),
		Transaction: processedLoss(t, at),
		At:          at,
	}
}

// pendingOperation is an operation that was built and never decided, which is
// the only status no event speaks for.
func pendingOperation(t *testing.T) *wager.Transaction {
	t.Helper()
	return loss(t, at)
}

// processedLoss is the shortest settled operation there is: it concludes and
// moves no balance, so the commit carries one event and no entry.
func processedLoss(t *testing.T, at time.Time) *wager.Transaction {
	t.Helper()
	op := loss(t, at)
	if err := op.Process(zeroBRL(t), at); err != nil {
		t.Fatalf("Process = %v, want nil", err)
	}
	return op
}

func zeroBRL(t *testing.T) money.Money {
	t.Helper()
	zero, err := money.Parse("0.00", "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	return zero
}

func loss(t *testing.T, at time.Time) *wager.Transaction {
	t.Helper()
	op, err := wager.NewExternal(wager.ExternalSpec{
		ID:             idOf(t, identity.ParseTransactionID, "019974a4-0000-7000-8000-0000000000c1"),
		ProviderID:     idOf(t, identity.ParseProviderID, "provider-a"),
		ExternalID:     idOf(t, identity.ParseExternalTransactionID, "tx-001"),
		IdempotencyKey: idOf(t, identity.ParseIdempotencyKey, "key-001"),
		BodyHash:       "hash",
		PlayerID:       idOf(t, identity.ParsePlayerID, "019974a4-0000-7000-8000-000000000b11"),
		WalletID:       idOf(t, identity.ParseWalletID, "019974a4-0000-7000-8000-00000000a11e"),
		RoundID:        idOf(t, identity.ParseRoundID, "round-1"),
		GameID:         idOf(t, identity.ParseGameID, "crash"),
		Kind:           wager.KindLoss,
		Amount:         zeroBRL(t),
		At:             at,
	})
	if err != nil {
		t.Fatalf("wager.NewExternal = %v, want nil", err)
	}
	return op
}

func idOf[T any](t *testing.T, parse func(string) (T, error), text string) T {
	t.Helper()
	parsed, err := parse(text)
	if err != nil {
		t.Fatalf("parse of %q = %v, want nil", text, err)
	}
	return parsed
}

// A commit that decided nothing the vocabulary names is a defect: the failure
// leaves before any row is written, rather than a row being written for a
// status no consumer can read.
func TestRecord_writesNothingWhenTheCommitNamesNoOutcome(t *testing.T) {
	t.Parallel()
	rows := &outboxRows{}
	undecided := commitOf(t)
	undecided.Transaction = pendingOperation(t)
	if err := Record(context.Background(), &txStub{outbox: rows}, undecided); !errors.Is(err, event.ErrUnknownOutcome) {
		t.Fatalf("Record of an undecided commit = %v, want ErrUnknownOutcome", err)
	}
	if len(rows.written) != 0 {
		t.Fatalf("events written for an undecided commit = %d, want none", len(rows.written))
	}
}
