package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

const insertWallet = `
INSERT INTO wallets (id, player_id, currency, balance_cents, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`

// The lock is taken on the wallet row before the balance is read, so the
// decision that follows reads a balance nobody else can change until the commit.
const lockWallet = `
SELECT player_id, currency, balance_cents, version, created_at, updated_at
  FROM wallets
 WHERE id = $1
   FOR UPDATE`

// The version that was read is the condition of the write. It is the assertion
// that nothing went past the lock, and it is what makes zero rows affected mean
// something.
const updateWalletBalance = `
UPDATE wallets
   SET balance_cents = $1, version = $2, updated_at = $3
 WHERE id = $4 AND version = $5`

// wallets reads and writes the wallet row of one open transaction.
type wallets struct {
	tx pgx.Tx
}

// Insert writes the opened wallet. The duplicate of a player and currency is
// decided here, by the unique index, and not by a query that two replicas could
// both pass at the same time.
func (r wallets) Insert(ctx context.Context, opened *wallet.Wallet) error {
	_, err := r.tx.Exec(ctx, insertWallet,
		opened.ID().String(),
		opened.PlayerID().String(),
		opened.Currency().Code(),
		opened.Balance().Cents(),
		opened.Version(),
		opened.CreatedAt(),
		opened.UpdatedAt(),
	)
	return wrap("insert wallet", err)
}

// GetForUpdate locks the wallet row and answers the state to rehydrate from. A
// wallet that does not exist answers ErrWalletNotFound, before any row of the
// operation exists.
func (r wallets) GetForUpdate(ctx context.Context, id identity.WalletID) (wallet.State, error) {
	var found lockedWallet
	err := r.tx.QueryRow(ctx, lockWallet, id.String()).Scan(
		&found.playerID, &found.currency, &found.cents,
		&found.version, &found.createdAt, &found.updatedAt,
	)
	if err != nil {
		return wallet.State{}, missingWallet("lock wallet", err)
	}
	return found.state(id)
}

// UpdateBalance writes the moved balance conditioned on the version read under
// the lock.
//
// Zero rows affected is a write that went past the lock: it answers ErrLostWrite,
// which undoes the SQL transaction and carries no failureCode, because no
// business rule refused anything here.
func (r wallets) UpdateBalance(ctx context.Context, moved *wallet.Wallet, readVersion int64) error {
	tag, err := r.tx.Exec(ctx, updateWalletBalance,
		moved.Balance().Cents(),
		moved.Version(),
		moved.UpdatedAt(),
		moved.ID().String(),
		readVersion,
	)
	if err != nil {
		return wrap("update wallet balance", err)
	}
	if tag.RowsAffected() == 0 {
		return wrap("update wallet balance", storage.ErrLostWrite)
	}
	return nil
}

// lockedWallet is the locked row as PostgreSQL hands it over. The identity is
// the one the caller asked for, so the column is not read back.
type lockedWallet struct {
	playerID  string
	currency  string
	cents     int64
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

func (r lockedWallet) state(id identity.WalletID) (wallet.State, error) {
	playerID, err := identity.ParsePlayerID(r.playerID)
	if err != nil {
		return wallet.State{}, wrap("read player identity", err)
	}
	balance, err := balanceOf(r.currency, r.cents)
	if err != nil {
		return wallet.State{}, wrap("read wallet balance", err)
	}
	return wallet.State{
		ID:        id,
		PlayerID:  playerID,
		Balance:   balance,
		Version:   r.version,
		CreatedAt: r.createdAt,
		UpdatedAt: r.updatedAt,
	}, nil
}
