package wallet

import (
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

var (
	ErrInsufficientFunds = errors.New("wallet: balance does not cover the debit")
	ErrCurrencyMismatch  = errors.New("wallet: operation is in a currency other than the wallet one")
	ErrNonPositiveAmount = errors.New("wallet: movement amount is not greater than zero")
)

// MoveSpec brings what the movement needs and the wallet does not mint: the
// identifiers and the clock arrive ready from the caller.
type MoveSpec struct {
	EntryID       identity.LedgerEntryID
	TransactionID identity.TransactionID
	Amount        money.Money
	At            time.Time
}

// Result is the following state as a value: the entry, the balance after and
// the version. Opening at zero carries no entry, so Entry reports whether one
// exists rather than returning a zero entry.
type Result struct {
	entry    ledger.Entry
	hasEntry bool
	balance  money.Money
	version  int64
}

func (r Result) Entry() (ledger.Entry, bool) {
	return r.entry, r.hasEntry
}

func (r Result) Balance() money.Money {
	return r.balance
}

func (r Result) Version() int64 {
	return r.version
}

// Debit takes the amount out of the balance and returns the entry. A debit
// that would take the balance below zero is refused with ErrInsufficientFunds.
func (w *Wallet) Debit(spec MoveSpec) (Result, error) {
	return w.move(ledger.Debit, spec)
}

// Credit puts the amount into the balance and returns the entry.
func (w *Wallet) Credit(spec MoveSpec) (Result, error) {
	return w.move(ledger.Credit, spec)
}

// move is the only writer of the balance: it computes the next state, builds
// the entry and assigns the fields.
//
// Every field is ready before the first assignment, so a refusal returns the
// error with the wallet untouched — no deferred rollback and no defensive copy
// of the aggregate.
func (w *Wallet) move(direction ledger.Direction, spec MoveSpec) (Result, error) {
	if err := w.checkAmount(spec.Amount); err != nil {
		return Result{}, err
	}
	next, err := w.nextBalance(direction, spec.Amount)
	if err != nil {
		return Result{}, err
	}
	entry, err := w.entryFor(direction, spec, next)
	if err != nil {
		return Result{}, err
	}
	w.balance = next
	w.version++
	w.updatedAt = spec.At
	return Result{entry: entry, hasEntry: true, balance: next, version: w.version}, nil
}

func (w *Wallet) checkAmount(amount money.Money) error {
	if !amount.IsPositive() {
		return ErrNonPositiveAmount
	}
	if amount.Currency() != w.balance.Currency() {
		return ErrCurrencyMismatch
	}
	return nil
}

// nextBalance refuses the debit that would take the balance below zero.
//
// The refusal is a condition, not a token: only the caller knows whether this
// is a bet or a reversal, and the same condition names INSUFFICIENT_FUNDS on
// one path and REVERSAL_INSUFFICIENT_FUNDS on the other.
func (w *Wallet) nextBalance(direction ledger.Direction, amount money.Money) (money.Money, error) {
	next, err := direction.Apply(w.balance, amount)
	if err != nil {
		return money.Money{}, err
	}
	if next.IsNegative() {
		return money.Money{}, ErrInsufficientFunds
	}
	return next, nil
}

// entryFor numbers the entry with the resulting wallet version.
func (w *Wallet) entryFor(direction ledger.Direction, spec MoveSpec, next money.Money) (ledger.Entry, error) {
	return ledger.NewEntry(ledger.EntrySpec{
		ID:            spec.EntryID,
		WalletID:      w.id,
		TransactionID: spec.TransactionID,
		Direction:     direction,
		Amount:        spec.Amount,
		BalanceBefore: w.balance,
		BalanceAfter:  next,
		Sequence:      w.version + 1,
		CreatedAt:     spec.At,
	})
}
