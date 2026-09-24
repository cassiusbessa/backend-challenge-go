package wager

import (
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func later(t *testing.T) time.Time {
	t.Helper()
	return at(t).Add(time.Hour)
}

func TestProcess_recordsTheObservedBalance(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	if err := transaction.Process(brl(t, 97500), later(t)); err != nil {
		t.Fatalf("Process error = %v, want nil", err)
	}
	if transaction.Status() != Processed {
		t.Fatalf("status after Process = %s, want PROCESSED", transaction.Status())
	}
	if transaction.ObservedBalance().Amount() != "975.00" {
		t.Fatalf("observed balance = %s, want 975.00", transaction.ObservedBalance().Amount())
	}
	if !transaction.UpdatedAt().Equal(later(t)) {
		t.Fatalf("updated at = %s, want %s", transaction.UpdatedAt(), later(t))
	}
}

func TestProcess_refusesWithoutTheObservedBalance(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	if err := transaction.Process(money.Money{}, later(t)); !errors.Is(err, ErrIncompleteTransition) {
		t.Fatalf("Process without a balance error = %v, want ErrIncompleteTransition", err)
	}
	if transaction.Status() != Pending {
		t.Fatalf("status after the refused Process = %s, want PENDING", transaction.Status())
	}
}

func TestApply_refusesANewTransitionFromATerminalStatus(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	if err := transaction.Process(brl(t, 97500), later(t)); err != nil {
		t.Fatalf("Process before the terminal check error = %v, want nil", err)
	}
	err := transaction.Reject(InsufficientFunds, later(t).Add(time.Hour))
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Reject over a PROCESSED transaction error = %v, want ErrInvalidTransition", err)
	}
	if transaction.Status() != Processed {
		t.Fatalf("status after the refused transition = %s, want PROCESSED", transaction.Status())
	}
	if transaction.ObservedBalance().Amount() != "975.00" {
		t.Fatalf("observed balance after the refused transition = %s, want 975.00", transaction.ObservedBalance().Amount())
	}
	if !transaction.UpdatedAt().Equal(later(t)) {
		t.Fatalf("instant after the refused transition = %s, want the one of the Process", transaction.UpdatedAt())
	}
}

func TestReject_recordsTheCatalogToken(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	if err := transaction.Reject(InsufficientFunds, later(t)); err != nil {
		t.Fatalf("Reject error = %v, want nil", err)
	}
	if transaction.Status() != Rejected {
		t.Fatalf("status after Reject = %s, want REJECTED", transaction.Status())
	}
	if transaction.FailureCode() != InsufficientFunds {
		t.Fatalf("failure code = %s, want INSUFFICIENT_FUNDS", transaction.FailureCode())
	}
}

func TestReject_refusesWithoutAFailureCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		code FailureCode
	}{
		{name: "with the zero code", code: noFailureCode},
		{name: "with a code outside the catalog", code: FailureCode(200)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			transaction := mustExternal(t, betSpec(t))
			err := transaction.Reject(testCase.code, later(t))
			if !errors.Is(err, ErrIncompleteTransition) {
				t.Fatalf("Reject %s error = %v, want ErrIncompleteTransition", testCase.name, err)
			}
			if transaction.Status() != Pending {
				t.Fatalf("status after Reject %s = %s, want PENDING", testCase.name, transaction.Status())
			}
		})
	}
}

func TestWaitForReference_recordsTheScheduleAndTheDeadline(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	deadline := at(t).Add(15 * time.Minute)
	if err := transaction.WaitForReference(later(t), deadline, at(t)); err != nil {
		t.Fatalf("WaitForReference error = %v, want nil", err)
	}
	if transaction.Status() != PendingReference {
		t.Fatalf("status after the wait = %s, want PENDING_REFERENCE", transaction.Status())
	}
	if !transaction.NextAttemptAt().Equal(later(t)) {
		t.Fatalf("next attempt = %s, want %s", transaction.NextAttemptAt(), later(t))
	}
	if !transaction.ReferenceDeadlineAt().Equal(deadline) {
		t.Fatalf("deadline = %s, want %s", transaction.ReferenceDeadlineAt(), deadline)
	}
}

func TestWaitForReference_refusesWithoutTheScheduleOrTheDeadline(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	if err := transaction.WaitForReference(time.Time{}, at(t).Add(time.Minute), at(t)); !errors.Is(err, ErrIncompleteTransition) {
		t.Fatalf("WaitForReference without a next attempt error = %v, want ErrIncompleteTransition", err)
	}
	if err := transaction.WaitForReference(later(t), time.Time{}, at(t)); !errors.Is(err, ErrIncompleteTransition) {
		t.Fatalf("WaitForReference without a deadline error = %v, want ErrIncompleteTransition", err)
	}
	if transaction.Status() != Pending {
		t.Fatalf("status after the refused wait = %s, want PENDING", transaction.Status())
	}
}

// The deadline is written once, on entering the wait. Closing the wait must
// not erase it: the wait is part of the record, not scaffolding.
func TestAssign_keepsTheDeadlineAfterTheWaitCloses(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	deadline := at(t).Add(15 * time.Minute)
	if err := transaction.WaitForReference(later(t), deadline, at(t)); err != nil {
		t.Fatalf("WaitForReference before closing error = %v, want nil", err)
	}
	if err := transaction.Process(brl(t, 102500), later(t)); err != nil {
		t.Fatalf("Process after the wait error = %v, want nil", err)
	}
	if !transaction.ReferenceDeadlineAt().Equal(deadline) {
		t.Fatalf("deadline after the wait closed = %s, want %s", transaction.ReferenceDeadlineAt(), deadline)
	}
	if !transaction.NextAttemptAt().Equal(later(t)) {
		t.Fatalf("next attempt after the wait closed = %s, want it kept", transaction.NextAttemptAt())
	}
}

func TestFail_onlyClosesATransactionThatWasAlreadyWaiting(t *testing.T) {
	t.Parallel()
	fresh := mustExternal(t, betSpec(t))
	if err := fresh.Fail(ReferenceNotFound, later(t)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Fail straight from PENDING error = %v, want ErrInvalidTransition", err)
	}
	waiting := mustExternal(t, betSpec(t))
	if err := waiting.WaitForReference(later(t), at(t).Add(time.Minute), at(t)); err != nil {
		t.Fatalf("WaitForReference before Fail error = %v, want nil", err)
	}
	if err := waiting.Fail(ReferenceNotFound, later(t)); err != nil {
		t.Fatalf("Fail from PENDING_REFERENCE error = %v, want nil", err)
	}
	if waiting.Status() != Failed {
		t.Fatalf("status after Fail = %s, want FAILED", waiting.Status())
	}
}

func TestValidate_refusesATransitionWithoutAnInstant(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	if err := transaction.Process(brl(t, 97500), time.Time{}); !errors.Is(err, ErrIncompleteTransition) {
		t.Fatalf("Process without an instant error = %v, want ErrIncompleteTransition", err)
	}
}

func TestReplay_answersTheRecordedResultWithoutReapplying(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	if err := transaction.Process(brl(t, 97500), later(t)); err != nil {
		t.Fatalf("Process before the replay error = %v, want nil", err)
	}
	outcome, ok := transaction.Replay()
	if !ok {
		t.Fatalf("Replay of a PROCESSED transaction reported ok = false, want true")
	}
	if outcome.Status() != Processed {
		t.Fatalf("replayed status = %s, want PROCESSED", outcome.Status())
	}
	if outcome.ObservedBalance().Amount() != "975.00" {
		t.Fatalf("replayed balance = %s, want 975.00", outcome.ObservedBalance().Amount())
	}
}

func TestReplay_answersTheSameResultEveryTime(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	if err := transaction.Process(brl(t, 97500), later(t)); err != nil {
		t.Fatalf("Process before the repeated replay error = %v, want nil", err)
	}
	first, _ := transaction.Replay()
	second, _ := transaction.Replay()
	if second.ObservedBalance().Amount() != first.ObservedBalance().Amount() {
		t.Fatalf("a second replay answered %s, want the same %s", second.ObservedBalance().Amount(), first.ObservedBalance().Amount())
	}
	if !transaction.UpdatedAt().Equal(later(t)) {
		t.Fatalf("the replay moved the instant to %s, want the transaction untouched", transaction.UpdatedAt())
	}
}

func TestReplay_keepsTheCodeOfARejection(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	if err := transaction.Reject(InsufficientFunds, later(t)); err != nil {
		t.Fatalf("Reject before the replay error = %v, want nil", err)
	}
	outcome, ok := transaction.Replay()
	if !ok {
		t.Fatalf("Replay of a REJECTED transaction reported ok = false, want true")
	}
	if outcome.FailureCode() != InsufficientFunds {
		t.Fatalf("replayed code = %s, want INSUFFICIENT_FUNDS", outcome.FailureCode())
	}
}

func TestReplay_answersNothingWhileTheTransactionIsInFlight(t *testing.T) {
	t.Parallel()
	transaction := mustExternal(t, betSpec(t))
	outcome, ok := transaction.Replay()
	if ok {
		t.Fatalf("Replay of a PENDING transaction reported ok = true, want false")
	}
	if !outcome.Status().IsZero() {
		t.Fatalf("replayed status of a PENDING transaction = %s, want the zero value", outcome.Status())
	}
}

func TestRehydrate_restoresTheRowWithoutReplayingMovement(t *testing.T) {
	t.Parallel()
	restored, err := Rehydrate(State{
		ID:              transactionOf(t),
		Kind:            KindBet,
		PlayerID:        playerOf(t),
		WalletID:        walletOf(t),
		Amount:          brl(t, 2500),
		Status:          Processed,
		ObservedBalance: brl(t, 97500),
		CreatedAt:       at(t),
		UpdatedAt:       later(t),
	})
	if err != nil {
		t.Fatalf("Rehydrate error = %v, want nil", err)
	}
	if restored.Status() != Processed {
		t.Fatalf("rehydrated status = %s, want PROCESSED", restored.Status())
	}
	if restored.ObservedBalance().Amount() != "975.00" {
		t.Fatalf("rehydrated balance = %s, want 975.00", restored.ObservedBalance().Amount())
	}
	if !restored.UpdatedAt().Equal(later(t)) {
		t.Fatalf("rehydrated instant = %s, want %s", restored.UpdatedAt(), later(t))
	}
}

func TestRehydrate_refusesTheIncompleteRow(t *testing.T) {
	t.Parallel()
	complete := State{
		ID:        transactionOf(t),
		Kind:      KindBet,
		PlayerID:  playerOf(t),
		WalletID:  walletOf(t),
		Amount:    brl(t, 2500),
		Status:    Processed,
		CreatedAt: at(t),
	}
	cases := []struct {
		name  string
		spoil func(*State)
	}{
		{name: "without an id", spoil: func(s *State) { s.ID = identity.TransactionID{} }},
		{name: "without a kind", spoil: func(s *State) { s.Kind = noKind }},
		{name: "without a status", spoil: func(s *State) { s.Status = noStatus }},
		{name: "without a player", spoil: func(s *State) { s.PlayerID = identity.PlayerID{} }},
		{name: "without a wallet", spoil: func(s *State) { s.WalletID = identity.WalletID{} }},
		{name: "without a creation instant", spoil: func(s *State) { s.CreatedAt = time.Time{} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			state := complete
			testCase.spoil(&state)
			restored, err := Rehydrate(state)
			if !errors.Is(err, ErrIncompleteTransaction) {
				t.Fatalf("Rehydrate %s error = %v, want ErrIncompleteTransaction", testCase.name, err)
			}
			if restored != nil {
				t.Fatalf("Rehydrate %s produced a transaction, want none", testCase.name)
			}
		})
	}
}
