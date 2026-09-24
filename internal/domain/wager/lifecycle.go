package wager

import (
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// transition is a move already decided, waiting for the machine to accept it.
// It exists so every destination is checked the same way, by one function per
// target, instead of a branch per pair of statuses.
type transition struct {
	target              Status
	observedBalance     money.Money
	failureCode         FailureCode
	nextAttemptAt       time.Time
	referenceDeadlineAt time.Time
	at                  time.Time
}

// Process closes the transaction with the balance the wallet reported.
func (t *Transaction) Process(observedBalance money.Money, at time.Time) error {
	return t.apply(transition{target: Processed, observedBalance: observedBalance, at: at})
}

// Reject closes the transaction on a business rule, with a catalog token.
func (t *Transaction) Reject(code FailureCode, at time.Time) error {
	return t.apply(transition{target: Rejected, failureCode: code, at: at})
}

// Fail closes a durable transaction that can no longer be completed because of
// a permanent infrastructure failure. A transient failure rolls the SQL
// transaction back and retries instead of landing here.
func (t *Transaction) Fail(code FailureCode, at time.Time) error {
	return t.apply(transition{target: Failed, failureCode: code, at: at})
}

// WaitForReference records the wait for an operation that has not arrived
// through the other channel yet. The deadline is written once, on entry.
func (t *Transaction) WaitForReference(nextAttemptAt, deadlineAt, at time.Time) error {
	return t.apply(transition{
		target:              PendingReference,
		nextAttemptAt:       nextAttemptAt,
		referenceDeadlineAt: deadlineAt,
		at:                  at,
	})
}

func (t *Transaction) apply(move transition) error {
	if !t.status.CanMoveTo(move.target) {
		return ErrInvalidTransition
	}
	if err := move.validate(); err != nil {
		return err
	}
	t.assign(move)
	return nil
}

func (m transition) validate() error {
	if m.at.IsZero() {
		return ErrIncompleteTransition
	}
	switch m.target {
	case Processed:
		return m.checkProcessed()
	case Rejected, Failed:
		return m.checkFailureCode()
	case PendingReference:
		return m.checkWait()
	}
	return ErrInvalidTransition
}

func (m transition) checkProcessed() error {
	if m.observedBalance.Currency().IsZero() {
		return ErrIncompleteTransition
	}
	return nil
}

func (m transition) checkFailureCode() error {
	if m.failureCode.String() == "" {
		return ErrIncompleteTransition
	}
	return nil
}

func (m transition) checkWait() error {
	if m.nextAttemptAt.IsZero() || m.referenceDeadlineAt.IsZero() {
		return ErrIncompleteTransition
	}
	return nil
}

// assign never clears a field the target does not carry, so the deadline
// written on entering PENDING_REFERENCE survives the move to PROCESSED. The
// wait is part of the record, not scaffolding.
func (t *Transaction) assign(move transition) {
	t.status = move.target
	t.updatedAt = move.at
	if !move.observedBalance.Currency().IsZero() {
		t.observedBalance = move.observedBalance
	}
	if !move.failureCode.IsZero() {
		t.failureCode = move.failureCode
	}
	if !move.nextAttemptAt.IsZero() {
		t.nextAttemptAt = move.nextAttemptAt
	}
	if !move.referenceDeadlineAt.IsZero() {
		t.referenceDeadlineAt = move.referenceDeadlineAt
	}
}

// Outcome is the result recorded for a terminal transaction.
type Outcome struct {
	status          Status
	observedBalance money.Money
	failureCode     FailureCode
}

func (o Outcome) Status() Status {
	return o.status
}

func (o Outcome) ObservedBalance() money.Money {
	return o.observedBalance
}

func (o Outcome) FailureCode() FailureCode {
	return o.failureCode
}

// Replay answers the recorded result without reapplying the operation,
// touching the wallet or producing an entry. The second value reports whether
// the transaction is terminal: one still in flight has no result to replay.
func (t *Transaction) Replay() (Outcome, bool) {
	if !t.status.IsTerminal() {
		return Outcome{}, false
	}
	return Outcome{
		status:          t.status,
		observedBalance: t.observedBalance,
		failureCode:     t.failureCode,
	}, true
}

// State is the persisted row, for rehydration.
type State struct {
	ID                  identity.TransactionID
	Kind                Kind
	PlayerID            identity.PlayerID
	WalletID            identity.WalletID
	Amount              money.Money
	ProviderID          identity.ProviderID
	ExternalID          identity.ExternalTransactionID
	IdempotencyKey      identity.IdempotencyKey
	BodyHash            string
	RoundID             identity.RoundID
	GameID              identity.GameID
	ReferenceExternalID identity.ExternalTransactionID
	Status              Status
	FailureCode         FailureCode
	ObservedBalance     money.Money
	NextAttemptAt       time.Time
	ReferenceDeadlineAt time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Rehydrate restores the transaction from the row. It replays no movement and
// runs no invariant of the kind: the row already passed those on the way in,
// and recomputing them here would reject a row the database accepted.
func Rehydrate(state State) (*Transaction, error) {
	if err := state.validate(); err != nil {
		return nil, err
	}
	return state.build(), nil
}

func (s State) validate() error {
	if s.ID.IsZero() || s.Kind.String() == "" || s.Status.String() == "" {
		return ErrIncompleteTransaction
	}
	return s.checkOwner()
}

func (s State) checkOwner() error {
	if s.PlayerID.IsZero() || s.WalletID.IsZero() || s.CreatedAt.IsZero() {
		return ErrIncompleteTransaction
	}
	return nil
}

func (s State) build() *Transaction {
	return &Transaction{
		id:                  s.ID,
		kind:                s.Kind,
		playerID:            s.PlayerID,
		walletID:            s.WalletID,
		amount:              s.Amount,
		providerID:          s.ProviderID,
		externalID:          s.ExternalID,
		idempotencyKey:      s.IdempotencyKey,
		bodyHash:            s.BodyHash,
		roundID:             s.RoundID,
		gameID:              s.GameID,
		referenceExternalID: s.ReferenceExternalID,
		status:              s.Status,
		failureCode:         s.FailureCode,
		observedBalance:     s.ObservedBalance,
		nextAttemptAt:       s.NextAttemptAt,
		referenceDeadlineAt: s.ReferenceDeadlineAt,
		createdAt:           s.CreatedAt,
		updatedAt:           s.UpdatedAt,
	}
}
