package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

const insertTransaction = `
INSERT INTO wager_transactions (
    id, kind, player_id, wallet_id, amount_cents, currency,
    provider_id, external_id, idempotency_key, body_hash, round_id, game_id,
    reference_external_id, status, failure_code, observed_balance_cents,
    next_attempt_at, reference_deadline_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10, $11, $12,
    $13, $14, $15, $16,
    $17, $18, $19, $20
)`

// transactions reads and writes the wager transaction row of one open
// transaction.
type transactions struct {
	tx pgx.Tx
}

// ByKey answers the transaction of that provider and key, or
// ErrTransactionNotFound.
//
// The read takes no lock, so it does not invert the order of wallet before
// transaction. It is the fast path of a replay and never the arbiter: two
// replicas can both pass it, and what decides the duplicate is the unique index.
func (r transactions) ByKey(ctx context.Context, provider identity.ProviderID, key identity.IdempotencyKey) (wager.State, error) {
	row, err := scanTransaction(ctx, r.tx, selectTransactionByKey, provider.String(), key.String())
	if err != nil {
		return wager.State{}, missingTransaction("read transaction by key", err)
	}
	return row.state()
}

// Insert writes the transaction as it stands. PENDING is not a writable status,
// so what reaches here is a terminal row or a recorded reference wait.
func (r transactions) Insert(ctx context.Context, recorded *wager.Transaction) error {
	reference, _ := recorded.ReferenceExternalID()
	_, err := r.tx.Exec(ctx, insertTransaction,
		recorded.ID().String(),
		recorded.Kind().String(),
		recorded.PlayerID().String(),
		recorded.WalletID().String(),
		recorded.Amount().Cents(),
		recorded.Amount().Currency().Code(),
		absent(recorded.ProviderID().String()),
		absent(recorded.ExternalID().String()),
		absent(recorded.IdempotencyKey().String()),
		absent(recorded.BodyHash()),
		absent(recorded.RoundID().String()),
		absent(recorded.GameID().String()),
		absent(reference.String()),
		recorded.Status().String(),
		absent(recorded.FailureCode().String()),
		cents(recorded.ObservedBalance()),
		instant(recorded.NextAttemptAt()),
		instant(recorded.ReferenceDeadlineAt()),
		recorded.CreatedAt(),
		recorded.UpdatedAt(),
	)
	return wrap("insert transaction", err)
}

// absent answers NULL for a field the row does not carry, because an internal
// OPENING keeps every provider column NULL and the CHECK enforces it.
func absent(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// cents answers NULL for money that was never set. The zero value of Money has
// no currency, which is what tells it apart from a balance of zero.
func cents(amount money.Money) any {
	if amount.Currency().IsZero() {
		return nil
	}
	return amount.Cents()
}

func instant(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return at
}
