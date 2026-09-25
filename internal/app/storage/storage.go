// Package storage declares the persistence ports the use cases speak to.
//
// The adapter in internal/platform/postgres implements them, so a use case
// never names pgx and a unit test never needs a database.
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

var (
	// ErrWalletExists is the unique violation of one wallet per player and
	// currency. It is a refusal of the contract, not a business rejection: the
	// closed catalog of failure codes names wager operations only, and an
	// opening is not one.
	ErrWalletExists = errors.New("storage: player already holds a wallet in this currency")

	// ErrNotFound is the family of every absence: the row the caller named does
	// not exist. Each entity keeps the sentinel below, and every one of them
	// unwraps to this family, so errors.Is answers the question about the entity
	// and the question about the absence for the same error.
	ErrNotFound = errors.New("storage: row does not exist")

	// ErrWalletNotFound is the absence of the wallet the caller named.
	ErrWalletNotFound = NotFoundError{Entity: "wallet"}

	// ErrTransactionNotFound is the absence of the transaction the caller named.
	// A transaction of another provider answers this same absence, so neither the
	// body nor the status tells the two apart.
	ErrTransactionNotFound = NotFoundError{Entity: "wager transaction"}

	// ErrLostWrite is a balance update that affected no row: the version read
	// under the lock is no longer the stored one, so some write went past it.
	//
	// It is a transient infrastructure failure and carries no failureCode: the
	// SQL transaction rolls back whole and the operation is tried again.
	ErrLostWrite = errors.New("storage: balance write affected no row")
)

// NotFoundError is the absence of one named entity. It unwraps to ErrNotFound,
// which is what lets a border classify every absence in a single case while a
// caller that has to tell a wallet from a transaction still compares the
// sentinel.
//
// The zero value names no entity and is not an absence.
type NotFoundError struct {
	Entity string
}

// Error names the entity that does not exist and never the identity that was
// looked up: an absence is answered without echoing back what was asked for.
func (e NotFoundError) Error() string {
	return "storage: " + e.Entity + " does not exist"
}

// Unwrap answers the family, which is what makes errors.Is match ErrNotFound for
// every entity without this package listing them anywhere.
func (e NotFoundError) Unwrap() error {
	return ErrNotFound
}

// UnitOfWork is the transactional boundary: one use case commits once.
type UnitOfWork interface {
	Within(ctx context.Context, work func(Tx) error) error
}

// Tx hands out the repositories of one open transaction. They are reachable
// only from here, so a write outside the commit is not expressible.
type Tx interface {
	Wallets() Wallets
	Transactions() Transactions
	Entries() Entries
}

// Wallets reads the wallet row for writing and writes it.
type Wallets interface {
	Insert(ctx context.Context, opened *wallet.Wallet) error

	// GetForUpdate locks the wallet row for writing and answers the state to
	// rehydrate from. Every operation that may move the balance goes through it,
	// because the decision must read a balance nobody else can change until the
	// commit. An absent wallet answers ErrWalletNotFound.
	GetForUpdate(ctx context.Context, id identity.WalletID) (wallet.State, error)

	// UpdateBalance writes the moved balance conditioned on the version that was
	// read under the lock, and answers ErrLostWrite when no row matched.
	//
	// An operation that moves nothing does not come through here at all: the
	// version rises only when the balance changes.
	UpdateBalance(ctx context.Context, moved *wallet.Wallet, readVersion int64) error
}

// Transactions reads and writes the wager transaction row.
type Transactions interface {
	Insert(ctx context.Context, recorded *wager.Transaction) error

	// ByKey answers the transaction of that provider and idempotency key, or
	// ErrTransactionNotFound. The pair is the scope of the key: the same key from
	// another provider is another operation.
	ByKey(ctx context.Context, provider identity.ProviderID, key identity.IdempotencyKey) (wager.State, error)
}

// Entries writes the ledger row, which is only ever inserted.
type Entries interface {
	Insert(ctx context.Context, entry ledger.Entry) error
}

// WalletView is the read model of a wallet. A query answers it without
// rehydrating the aggregate, because a read moves no money.
type WalletView struct {
	ID        identity.WalletID
	PlayerID  identity.PlayerID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TransactionView is the read model of a wager transaction: the outcome as it
// was recorded.
//
// ObservedBalance keeps its zero value when the row carries none, which is what
// tells an absent balance from a balance of zero. FailureCode is set only on a
// transaction closed by a rule.
type TransactionView struct {
	ID              identity.TransactionID
	Kind            wager.Kind
	Status          wager.Status
	ProviderID      identity.ProviderID
	ExternalID      identity.ExternalTransactionID
	PlayerID        identity.PlayerID
	WalletID        identity.WalletID
	RoundID         identity.RoundID
	GameID          identity.GameID
	Amount          money.Money
	ObservedBalance money.Money
	FailureCode     wager.FailureCode
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Reads answers read models outside any transaction.
type Reads interface {
	Wallet(ctx context.Context, id identity.WalletID) (WalletView, error)

	// Transaction answers the recorded outcome of one transaction of that
	// provider. The provider is part of the query and not of a check afterwards,
	// so a transaction of another provider and one that does not exist leave by
	// the same path.
	Transaction(ctx context.Context, id identity.TransactionID, provider identity.ProviderID) (TransactionView, error)

	// TransactionByKey answers the transaction of that provider and key from
	// outside a transaction, which is what the loser of the unique constraint
	// needs: the violation aborts its SQL transaction, so the winning row can
	// only be read after the rollback.
	TransactionByKey(ctx context.Context, provider identity.ProviderID, key identity.IdempotencyKey) (wager.State, error)
}
