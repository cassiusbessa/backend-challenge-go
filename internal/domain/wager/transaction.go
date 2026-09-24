package wager

import (
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

var ErrIncompleteTransaction = errors.New("wager: transaction is missing a required field")

// Transaction is the root that holds the provider operation, where it stands
// and the result recorded for it. The zero value is invalid: NewExternal,
// NewOpening and Rehydrate are the only ways to build one.
//
// It holds the wallet by identity and never a pointer to the wallet itself.
type Transaction struct {
	id       identity.TransactionID
	kind     Kind
	playerID identity.PlayerID
	walletID identity.WalletID
	amount   money.Money

	// The provider side. An OPENING is internal and carries none of it.
	providerID          identity.ProviderID
	externalID          identity.ExternalTransactionID
	idempotencyKey      identity.IdempotencyKey
	bodyHash            string
	roundID             identity.RoundID
	gameID              identity.GameID
	referenceExternalID identity.ExternalTransactionID

	status              Status
	failureCode         FailureCode
	observedBalance     money.Money
	nextAttemptAt       time.Time
	referenceDeadlineAt time.Time

	createdAt time.Time
	updatedAt time.Time
}

// ExternalSpec is an operation arriving from a provider, over HTTP or the
// queue. ReferenceExternalID is set only when the operation cites another one.
type ExternalSpec struct {
	ID                  identity.TransactionID
	ProviderID          identity.ProviderID
	ExternalID          identity.ExternalTransactionID
	IdempotencyKey      identity.IdempotencyKey
	BodyHash            string
	PlayerID            identity.PlayerID
	WalletID            identity.WalletID
	RoundID             identity.RoundID
	GameID              identity.GameID
	Kind                Kind
	Amount              money.Money
	ReferenceExternalID identity.ExternalTransactionID
	At                  time.Time
}

// OpeningSpec is the internal operation that records a wallet being opened
// with a positive balance. It carries no provider field at all.
type OpeningSpec struct {
	ID       identity.TransactionID
	PlayerID identity.PlayerID
	WalletID identity.WalletID
	Amount   money.Money
	At       time.Time
}

// NewExternal builds the transaction of an operation that arrived from a
// provider. It is born PENDING: on the happy path that status only exists in
// memory, and the commit writes the terminal one.
//
// OPENING is refused here with OPENING_NOT_ALLOWED, because the row would
// violate the invariant that keeps an opening internal.
func NewExternal(spec ExternalSpec) (*Transaction, error) {
	if spec.Kind == KindOpening {
		return nil, NewRejection(OpeningNotAllowed, nil)
	}
	if spec.Kind.String() == "" {
		return nil, ErrUnknownKind
	}
	if err := spec.checkFields(); err != nil {
		return nil, err
	}
	if err := checkAmountForKind(spec.Kind, spec.Amount); err != nil {
		return nil, err
	}
	if err := checkReferenceForKind(spec.Kind, spec.ReferenceExternalID); err != nil {
		return nil, err
	}
	return spec.build(), nil
}

// NewOpening builds the internal transaction of a wallet opened with a
// positive balance. An opening at zero creates no transaction at all.
func NewOpening(spec OpeningSpec) (*Transaction, error) {
	if spec.ID.IsZero() || spec.PlayerID.IsZero() || spec.WalletID.IsZero() || spec.At.IsZero() {
		return nil, ErrIncompleteTransaction
	}
	if err := checkAmountForKind(KindOpening, spec.Amount); err != nil {
		return nil, err
	}
	return &Transaction{
		id:        spec.ID,
		kind:      KindOpening,
		playerID:  spec.PlayerID,
		walletID:  spec.WalletID,
		amount:    spec.Amount,
		status:    Pending,
		createdAt: spec.At,
		updatedAt: spec.At,
	}, nil
}

func (s ExternalSpec) checkFields() error {
	if s.ID.IsZero() || s.PlayerID.IsZero() || s.WalletID.IsZero() || s.At.IsZero() {
		return ErrIncompleteTransaction
	}
	return s.checkProviderFields()
}

func (s ExternalSpec) checkProviderFields() error {
	if s.ProviderID.IsZero() || s.ExternalID.IsZero() || s.IdempotencyKey.IsZero() {
		return ErrIncompleteTransaction
	}
	return s.checkRoundFields()
}

func (s ExternalSpec) checkRoundFields() error {
	if s.BodyHash == "" || s.RoundID.IsZero() || s.GameID.IsZero() {
		return ErrIncompleteTransaction
	}
	return nil
}

// checkAmountForKind is a business rejection, not malformed input: the body is
// well formed and the amount is valid money. What is refused is the coupling
// between kind and amount, which go-db-invariants also guards as a CHECK.
func checkAmountForKind(kind Kind, amount money.Money) error {
	if amount.Currency().IsZero() {
		return ErrIncompleteTransaction
	}
	if kind.MovesMoney() != amount.IsPositive() {
		return NewRejection(AmountNotAllowedForKind, nil)
	}
	return nil
}

func checkReferenceForKind(kind Kind, reference identity.ExternalTransactionID) error {
	if kind.IsReversal() && reference.IsZero() {
		return NewRejection(ReferenceRequired, nil)
	}
	return nil
}

func (s ExternalSpec) build() *Transaction {
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
		status:              Pending,
		createdAt:           s.At,
		updatedAt:           s.At,
	}
}

func (t *Transaction) ID() identity.TransactionID {
	return t.id
}

func (t *Transaction) Kind() Kind {
	return t.kind
}

func (t *Transaction) PlayerID() identity.PlayerID {
	return t.playerID
}

func (t *Transaction) WalletID() identity.WalletID {
	return t.walletID
}

func (t *Transaction) Amount() money.Money {
	return t.amount
}

func (t *Transaction) ProviderID() identity.ProviderID {
	return t.providerID
}

func (t *Transaction) ExternalID() identity.ExternalTransactionID {
	return t.externalID
}

func (t *Transaction) IdempotencyKey() identity.IdempotencyKey {
	return t.idempotencyKey
}

func (t *Transaction) BodyHash() string {
	return t.bodyHash
}

func (t *Transaction) RoundID() identity.RoundID {
	return t.roundID
}

func (t *Transaction) GameID() identity.GameID {
	return t.gameID
}

// ReferenceExternalID answers the cited operation and reports whether there is
// one: only a reversal always has it, and a WIN may or may not.
func (t *Transaction) ReferenceExternalID() (identity.ExternalTransactionID, bool) {
	return t.referenceExternalID, !t.referenceExternalID.IsZero()
}

func (t *Transaction) Status() Status {
	return t.status
}

func (t *Transaction) FailureCode() FailureCode {
	return t.failureCode
}

func (t *Transaction) ObservedBalance() money.Money {
	return t.observedBalance
}

func (t *Transaction) NextAttemptAt() time.Time {
	return t.nextAttemptAt
}

func (t *Transaction) ReferenceDeadlineAt() time.Time {
	return t.referenceDeadlineAt
}

func (t *Transaction) CreatedAt() time.Time {
	return t.createdAt
}

func (t *Transaction) UpdatedAt() time.Time {
	return t.updatedAt
}
