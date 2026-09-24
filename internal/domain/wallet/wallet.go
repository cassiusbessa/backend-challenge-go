package wallet

import (
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

var (
	ErrIncompleteWallet = errors.New("wallet: wallet has no identity, version or instant")
	ErrMissingCurrency  = errors.New("wallet: balance has no currency")
	ErrNegativeBalance  = errors.New("wallet: balance is below zero")
)

// The wallet is born at version 1 already holding the initial balance, and
// every balance change raises the version by exactly one. That is why the
// entry sequence is the resulting version: no separate counter exists to drift
// out of sync with it inside a transaction.
const firstVersion int64 = 1

// Wallet owns the balance, the version and the movement. The zero value is
// invalid: Open and Rehydrate are the only ways to build one.
//
// Fields are private and there is no setter. The following state leaves only
// in the return of Open, Debit and Credit.
type Wallet struct {
	id        identity.WalletID
	playerID  identity.PlayerID
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// OpenSpec brings identity, player, initial balance and instant already
// resolved. The currency is the one of the initial balance, so the two cannot
// disagree.
//
// EntryID and TransactionID are required only when the initial balance is
// positive, which is when an entry exists at all.
type OpenSpec struct {
	ID             identity.WalletID
	PlayerID       identity.PlayerID
	InitialBalance money.Money
	EntryID        identity.LedgerEntryID
	TransactionID  identity.TransactionID
	At             time.Time
}

// State is what persistence already knows, for rehydration.
type State struct {
	ID        identity.WalletID
	PlayerID  identity.PlayerID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Open creates the wallet, born at version 1 already holding the initial
// balance. It returns the opening credit entry only when that balance is
// positive.
//
// The initial balance is refused below zero with ErrNegativeBalance, and no
// movement takes the balance below zero afterwards either.
func Open(spec OpenSpec) (*Wallet, Result, error) {
	if err := spec.validate(); err != nil {
		return nil, Result{}, err
	}
	opened := &Wallet{
		id:        spec.ID,
		playerID:  spec.PlayerID,
		balance:   spec.InitialBalance,
		version:   firstVersion,
		createdAt: spec.At,
		updatedAt: spec.At,
	}
	if !spec.InitialBalance.IsPositive() {
		return opened, Result{balance: spec.InitialBalance, version: firstVersion}, nil
	}
	entry, err := openingEntry(spec)
	if err != nil {
		return nil, Result{}, err
	}
	return opened, Result{entry: entry, hasEntry: true, balance: spec.InitialBalance, version: firstVersion}, nil
}

// openingEntry is the credit of the birth itself: it starts at zero, reaches
// the initial balance and takes sequence 1.
//
// Opening at zero and crediting afterwards would answer version 2 on the
// creation response, which is not what POST /wallets shows.
func openingEntry(spec OpenSpec) (ledger.Entry, error) {
	empty, err := money.Zero(spec.InitialBalance.Currency())
	if err != nil {
		return ledger.Entry{}, err
	}
	return ledger.NewEntry(ledger.EntrySpec{
		ID:            spec.EntryID,
		WalletID:      spec.ID,
		TransactionID: spec.TransactionID,
		Direction:     ledger.Credit,
		Amount:        spec.InitialBalance,
		BalanceBefore: empty,
		BalanceAfter:  spec.InitialBalance,
		Sequence:      firstVersion,
		CreatedAt:     spec.At,
	})
}

// Rehydrate is a separate function from Open: it replays no movement, produces
// no entry and does not touch the stored version.
func Rehydrate(state State) (*Wallet, error) {
	if err := state.validate(); err != nil {
		return nil, err
	}
	return &Wallet{
		id:        state.ID,
		playerID:  state.PlayerID,
		balance:   state.Balance,
		version:   state.Version,
		createdAt: state.CreatedAt,
		updatedAt: state.UpdatedAt,
	}, nil
}

func (s OpenSpec) validate() error {
	if s.ID.IsZero() || s.PlayerID.IsZero() || s.At.IsZero() {
		return ErrIncompleteWallet
	}
	if err := checkBalance(s.InitialBalance); err != nil {
		return err
	}
	return s.checkOpeningEntry()
}

func (s OpenSpec) checkOpeningEntry() error {
	if !s.InitialBalance.IsPositive() {
		return nil
	}
	if s.EntryID.IsZero() || s.TransactionID.IsZero() {
		return ErrIncompleteWallet
	}
	return nil
}

func (s State) validate() error {
	if s.ID.IsZero() || s.PlayerID.IsZero() {
		return ErrIncompleteWallet
	}
	if err := s.checkPlacement(); err != nil {
		return err
	}
	return checkBalance(s.Balance)
}

func (s State) checkPlacement() error {
	if s.Version < firstVersion || s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() {
		return ErrIncompleteWallet
	}
	return nil
}

func checkBalance(balance money.Money) error {
	if balance.Currency().IsZero() {
		return ErrMissingCurrency
	}
	if balance.IsNegative() {
		return ErrNegativeBalance
	}
	return nil
}

func (w *Wallet) ID() identity.WalletID {
	return w.id
}

func (w *Wallet) PlayerID() identity.PlayerID {
	return w.playerID
}

// Balance returns a copy. Money is a value type, so altering what leaves here
// does not reach the stored balance.
func (w *Wallet) Balance() money.Money {
	return w.balance
}

func (w *Wallet) Currency() money.Currency {
	return w.balance.Currency()
}

func (w *Wallet) Version() int64 {
	return w.version
}

func (w *Wallet) CreatedAt() time.Time {
	return w.createdAt
}

func (w *Wallet) UpdatedAt() time.Time {
	return w.updatedAt
}
