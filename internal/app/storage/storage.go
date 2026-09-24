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

	// ErrWalletNotFound is the absence of the wallet the caller named.
	ErrWalletNotFound = errors.New("storage: wallet does not exist")
)

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

// Wallets writes the wallet row.
type Wallets interface {
	Insert(ctx context.Context, opened *wallet.Wallet) error
}

// Transactions writes the wager transaction row.
type Transactions interface {
	Insert(ctx context.Context, recorded *wager.Transaction) error
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

// Reads answers read models outside any transaction.
type Reads interface {
	Wallet(ctx context.Context, id identity.WalletID) (WalletView, error)
}
