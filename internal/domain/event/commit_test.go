package event

import (
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestOf_answersTheOutcomeAndTheBalanceOfASettledBet(t *testing.T) {
	t.Parallel()
	events := eventsOf(t, commitOf(t, processedBet(t), credit(t)))
	assertTypes(t, events, TypeProcessed, TypeBalanceChanged)
	if events[0].ID().String() != outcomeUUID || events[1].ID().String() != balanceUUID {
		t.Fatalf("event ids = %s and %s, want %s and %s", events[0].ID(), events[1].ID(), outcomeUUID, balanceUUID)
	}
}

func TestOf_answersOnlyTheOutcomeWhenNothingMoved(t *testing.T) {
	t.Parallel()
	loss := commitOf(t, processedLoss(t), ledger.Entry{})
	assertTypes(t, eventsOf(t, loss), TypeProcessed)
	rejection := commitOf(t, rejectedBet(t), ledger.Entry{})
	assertTypes(t, eventsOf(t, rejection), TypeRejected)
	wait := commitOf(t, waitingWin(t), ledger.Entry{})
	assertTypes(t, eventsOf(t, wait), TypePendingReference)
}

func TestOf_answersNothingForACommitThatRecordedNoTransaction(t *testing.T) {
	t.Parallel()
	opened := commitOf(t, nil, ledger.Entry{})
	events, err := Of(opened)
	if err != nil {
		t.Fatalf("Of an opening at zero = %v, want nil", err)
	}
	if len(events) != 0 {
		t.Fatalf("an opening at zero emitted %d events, want none", len(events))
	}
}

func TestOf_refusesATransactionInAStatusNoEventNames(t *testing.T) {
	t.Parallel()
	undecided := commitOf(t, external(t, betSpec(t)), ledger.Entry{})
	if _, err := Of(undecided); !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("Of a PENDING transaction = %v, want ErrUnknownOutcome", err)
	}
}

const (
	outcomeUUID = "019974a4-0000-7000-8000-00000000f001"
	balanceUUID = "019974a4-0000-7000-8000-00000000f002"
)

func commitOf(t *testing.T, op *wager.Transaction, entry ledger.Entry) Commit {
	t.Helper()
	return Commit{
		OutcomeID:     parsedEvent(t, outcomeUUID),
		BalanceID:     parsedEvent(t, balanceUUID),
		Transaction:   op,
		Entry:         entry,
		WalletVersion: 7,
		At:            at,
	}
}

func parsedEvent(t *testing.T, text string) identity.EventID {
	t.Helper()
	parsed, err := identity.ParseEventID(text)
	if err != nil {
		t.Fatalf("ParseEventID = %v, want nil", err)
	}
	return parsed
}

func processedLoss(t *testing.T) *wager.Transaction {
	t.Helper()
	spec := betSpec(t)
	spec.Kind = wager.KindLoss
	spec.Amount = brl(t, "0.00")
	op := external(t, spec)
	if err := op.Process(brl(t, "975.00"), at); err != nil {
		t.Fatalf("Process = %v, want nil", err)
	}
	return op
}

func eventsOf(t *testing.T, commit Commit) []Envelope {
	t.Helper()
	events, err := Of(commit)
	if err != nil {
		t.Fatalf("Of = %v, want nil", err)
	}
	return events
}

func assertTypes(t *testing.T, events []Envelope, want ...Type) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("the commit emitted %d events, want %d", len(events), len(want))
	}
	for index, expected := range want {
		if events[index].Type() != expected {
			t.Fatalf("event %d = %s, want %s", index, events[index].Type(), expected)
		}
	}
}
