package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
)

const insertEntry = `
INSERT INTO ledger_entries (
    id, wallet_id, transaction_id, direction, amount_cents, currency,
    balance_before_cents, balance_after_cents, sequence_number, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

// entries writes the ledger row of one open transaction. There is no update and
// no delete here, and the table refuses both anyway.
type entries struct {
	tx pgx.Tx
}

func (r entries) Insert(ctx context.Context, entry ledger.Entry) error {
	_, err := r.tx.Exec(ctx, insertEntry,
		entry.ID().String(),
		entry.WalletID().String(),
		entry.TransactionID().String(),
		entry.Direction().String(),
		entry.Amount().Cents(),
		entry.Amount().Currency().Code(),
		entry.BalanceBefore().Cents(),
		entry.BalanceAfter().Cents(),
		entry.Sequence(),
		entry.CreatedAt(),
	)
	return wrap("insert ledger entry", err)
}
