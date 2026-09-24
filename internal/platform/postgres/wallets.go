package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

const insertWallet = `
INSERT INTO wallets (id, player_id, currency, balance_cents, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)`

// wallets writes the wallet row of one open transaction.
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
