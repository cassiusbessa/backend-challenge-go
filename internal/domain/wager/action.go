package wager

import (
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// Movement carries what an action needs and the domain does not mint: the
// identity of the entry about to be written and the clock.
type Movement struct {
	EntryID identity.LedgerEntryID
	At      time.Time
}

// Reference is the cited operation as the use case loaded it.
//
// Cited is nil when the operation has not arrived through the other channel
// yet, which is what produces the decision to wait. AlreadyReversed says a
// PROCESSED reversal of that operation already exists, which only a query can
// know — the cited transaction does not carry it.
type Reference struct {
	Cited           *Transaction
	AlreadyReversed bool
}

// Decision is what an action decided over objects already loaded. No action
// reaches a repository, an SQL transaction or an event, and none of them
// computes a balance: the movement comes from the wallet.
type Decision struct {
	waiting  bool
	entry    ledger.Entry
	hasEntry bool
	balance  money.Money
	version  int64
}

// IsWaiting reports whether the action asked to wait for the cited operation,
// the decision that corresponds to PENDING_REFERENCE. The deadline, the
// schedule and the worker of that wait live outside this package.
func (d Decision) IsWaiting() bool {
	return d.waiting
}

// Entry answers the entry the movement produced and reports whether there was
// one. A LOSS moves nothing, so it has none.
func (d Decision) Entry() (ledger.Entry, bool) {
	return d.entry, d.hasEntry
}

func (d Decision) Balance() money.Money {
	return d.balance
}

func (d Decision) Version() int64 {
	return d.version
}

func moved(result wallet.Result) Decision {
	entry, ok := result.Entry()
	return Decision{
		entry:    entry,
		hasEntry: ok,
		balance:  result.Balance(),
		version:  result.Version(),
	}
}

func unchanged(w *wallet.Wallet) Decision {
	return Decision{balance: w.Balance(), version: w.Version()}
}

func waitForReference() Decision {
	return Decision{waiting: true}
}

// guard refuses before any movement: an absent wallet, an operation from
// another player, or an amount in a currency the wallet does not hold.
func guard(w *wallet.Wallet, op *Transaction) error {
	if w == nil {
		return NewRejection(WalletNotFound, nil)
	}
	if op.playerID != w.PlayerID() {
		return NewRejection(PlayerWalletMismatch, nil)
	}
	if op.amount.Currency() != w.Currency() {
		return NewRejection(CurrencyMismatch, nil)
	}
	return nil
}

// Bet debits the wallet.
//
// The wallet answers a condition and this function names the token, because
// the very same condition is INSUFFICIENT_FUNDS here and
// REVERSAL_INSUFFICIENT_FUNDS on a reversal.
func Bet(w *wallet.Wallet, op *Transaction, move Movement) (Decision, error) {
	if err := guard(w, op); err != nil {
		return Decision{}, err
	}
	return debit(w, op, move, InsufficientFunds)
}

// Loss closes the round without touching the wallet: no entry, no version
// change, and the balance stays where it was.
func Loss(w *wallet.Wallet, op *Transaction) (Decision, error) {
	if err := guard(w, op); err != nil {
		return Decision{}, err
	}
	return unchanged(w), nil
}

func debit(w *wallet.Wallet, op *Transaction, move Movement, insufficient FailureCode) (Decision, error) {
	result, err := w.Debit(op.spec(move))
	if err != nil {
		return Decision{}, translate(err, insufficient)
	}
	return moved(result), nil
}

func credit(w *wallet.Wallet, op *Transaction, move Movement) (Decision, error) {
	result, err := w.Credit(op.spec(move))
	if err != nil {
		return Decision{}, translate(err, InsufficientFunds)
	}
	return moved(result), nil
}

func (t *Transaction) spec(move Movement) wallet.MoveSpec {
	return wallet.MoveSpec{
		EntryID:       move.EntryID,
		TransactionID: t.id,
		Amount:        t.amount,
		At:            move.At,
	}
}

// translate names the catalog token for a wallet condition, and leaves
// anything else alone: an entry the ledger refused is a defect, not a
// business rejection, and it must not reach the provider as a failureCode.
func translate(err error, insufficient FailureCode) error {
	if errors.Is(err, wallet.ErrInsufficientFunds) {
		return NewRejection(insufficient, err)
	}
	if errors.Is(err, wallet.ErrCurrencyMismatch) {
		return NewRejection(CurrencyMismatch, err)
	}
	return err
}
