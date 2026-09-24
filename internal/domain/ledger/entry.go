package ledger

import (
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

var (
	ErrIncompleteEntry   = errors.New("ledger: entry has no identity, sequence or instant")
	ErrNonPositiveAmount = errors.New("ledger: entry amount is not greater than zero")
	ErrCurrencyMismatch  = errors.New("ledger: amount and balances are in different currencies")
	ErrBalanceMismatch   = errors.New("ledger: balance after does not match the direction")
)

// EntrySpec carries what the wallet movement already computed. It exists so
// the constructor does not take nine positional parameters.
type EntrySpec struct {
	ID            identity.LedgerEntryID
	WalletID      identity.WalletID
	TransactionID identity.TransactionID
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	Sequence      int64
	CreatedAt     time.Time
}

// Entry is the immutable record of one movement. It is born in the wallet
// movement, is only ever inserted, and has no setter. The zero value is
// invalid.
type Entry struct {
	id            identity.LedgerEntryID
	walletID      identity.WalletID
	transactionID identity.TransactionID
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	sequence      int64
	createdAt     time.Time
}

// NewEntry checks what the wallet computed and refuses an entry that does not
// hold together.
//
// The balance after is verified against the direction rather than recomputed:
// the wallet movement is the only place allowed to decide a balance, and
// go-db-invariants guards the same relation as a CHECK.
func NewEntry(spec EntrySpec) (Entry, error) {
	if err := spec.validate(); err != nil {
		return Entry{}, err
	}
	return Entry{
		id:            spec.ID,
		walletID:      spec.WalletID,
		transactionID: spec.TransactionID,
		direction:     spec.Direction,
		amount:        spec.Amount,
		balanceBefore: spec.BalanceBefore,
		balanceAfter:  spec.BalanceAfter,
		sequence:      spec.Sequence,
		createdAt:     spec.CreatedAt,
	}, nil
}

func (s EntrySpec) validate() error {
	if err := s.checkIdentity(); err != nil {
		return err
	}
	if err := s.checkAmount(); err != nil {
		return err
	}
	return s.checkBalances()
}

func (s EntrySpec) checkIdentity() error {
	if s.ID.IsZero() || s.WalletID.IsZero() || s.TransactionID.IsZero() {
		return ErrIncompleteEntry
	}
	if s.Sequence < 1 || s.CreatedAt.IsZero() {
		return ErrIncompleteEntry
	}
	return nil
}

func (s EntrySpec) checkAmount() error {
	if !s.Amount.IsPositive() {
		return ErrNonPositiveAmount
	}
	if s.Amount.Currency() != s.BalanceBefore.Currency() {
		return ErrCurrencyMismatch
	}
	if s.Amount.Currency() != s.BalanceAfter.Currency() {
		return ErrCurrencyMismatch
	}
	return nil
}

func (s EntrySpec) checkBalances() error {
	expected, err := s.Direction.Apply(s.BalanceBefore, s.Amount)
	if err != nil {
		return err
	}
	if !expected.Equal(s.BalanceAfter) {
		return ErrBalanceMismatch
	}
	return nil
}

func (e Entry) ID() identity.LedgerEntryID {
	return e.id
}

func (e Entry) WalletID() identity.WalletID {
	return e.walletID
}

func (e Entry) TransactionID() identity.TransactionID {
	return e.transactionID
}

func (e Entry) Direction() Direction {
	return e.direction
}

func (e Entry) Amount() money.Money {
	return e.amount
}

func (e Entry) BalanceBefore() money.Money {
	return e.balanceBefore
}

func (e Entry) BalanceAfter() money.Money {
	return e.balanceAfter
}

func (e Entry) Sequence() int64 {
	return e.sequence
}

func (e Entry) CreatedAt() time.Time {
	return e.createdAt
}

func (e Entry) IsZero() bool {
	return e.id.IsZero()
}
