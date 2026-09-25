package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
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

// The reversal that took the place is the one that reached PROCESSED, which is
// exactly what the partial unique index of the schema covers: the same predicate
// here is served by that index instead of a scan.
const selectProcessedReversal = `
SELECT EXISTS (
    SELECT 1 FROM wager_transactions
     WHERE provider_id = $1
       AND reference_external_id = $2
       AND status = 'PROCESSED'
       AND kind IN ('REFUND', 'ROLLBACK')
)`

// What the worker writes over a row whose wait ended.
//
// reference_deadline_at and next_attempt_at are absent from the SET list on
// purpose: the deadline is written once, on entry, and the schedule of a wait
// that is over says nothing. The two COALESCE keep whatever the row already held
// for a field the destination status does not carry — a wait that ends PROCESSED
// carries a balance and no token, and one that ends REJECTED carries a token and
// no balance.
//
// The attempt is counted here because one call is one attempt: the count is a
// metric, and go-reference-wait is explicit that what ends the wait is the
// deadline and never the count.
const endWaitRow = `
UPDATE wager_transactions
   SET status                 = $1,
       failure_code           = COALESCE($2, failure_code),
       observed_balance_cents = COALESCE($3, observed_balance_cents),
       attempt_count          = attempt_count + 1,
       updated_at             = $4
 WHERE id = $5 AND status = 'PENDING_REFERENCE'`

// What the worker writes over a wait this attempt did not end: the schedule and
// nothing else. The status stays where it is, and the deadline is not written
// again.
const rescheduleWaitRow = `
UPDATE wager_transactions
   SET next_attempt_at = $1,
       attempt_count   = attempt_count + 1,
       updated_at      = $2
 WHERE id = $3 AND status = 'PENDING_REFERENCE'`

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

// ByExternalID answers the operation that provider recorded under that external
// identifier, or ErrTransactionNotFound.
//
// The provider is part of the query, so an operation of somebody else leaves by
// the same path as one that was never sent: a citing operation cannot learn that
// a stranger holds the identifier it named.
func (r transactions) ByExternalID(ctx context.Context, provider identity.ProviderID, external identity.ExternalTransactionID) (wager.State, error) {
	row, err := scanTransaction(ctx, r.tx, selectTransactionByExternalID, provider.String(), external.String())
	if err != nil {
		return wager.State{}, missingTransaction("read cited transaction", err)
	}
	return row.state()
}

// HasProcessedReversal reports whether the cited operation already carries a
// reversal that reached PROCESSED.
//
// Unlike the two indexes of idempotency, this question decides on its own: it is
// asked after the wallet is locked, so a second reversal only reaches it once the
// first has committed and is therefore visible. The unique index behind it stays
// as an invariant of the database and not as the arbiter.
func (r transactions) HasProcessedReversal(ctx context.Context, provider identity.ProviderID, cited identity.ExternalTransactionID) (bool, error) {
	var exists bool
	err := r.tx.QueryRow(ctx, selectProcessedReversal, provider.String(), cited.String()).Scan(&exists)
	if err != nil {
		return false, wrap("read processed reversal", err)
	}
	return exists, nil
}

// ClaimWait locks the row of one wait and answers it re-read under that lock.
//
// A row another replica holds, one that is gone, and one whose next attempt is no
// longer due all answer ErrTransactionNotFound: each means this replica is not
// the one deciding this wait now, and the next scan offers it again.
func (r transactions) ClaimWait(ctx context.Context, id identity.TransactionID, due time.Time) (storage.Wait, error) {
	row, err := scanTransaction(ctx, r.tx, lockWaitRow, id.String(), due)
	if err != nil {
		return storage.Wait{}, missingTransaction("claim reference wait", err)
	}
	return row.wait()
}

// EndWait writes the terminal decision over a row that was waiting, and answers
// ErrTransactionNotFound when the row is no longer in the wait.
func (r transactions) EndWait(ctx context.Context, decided *wager.Transaction) error {
	return r.writeWait(ctx, "end reference wait", endWaitRow,
		decided.Status().String(),
		absent(decided.FailureCode().String()),
		cents(decided.ObservedBalance()),
		decided.UpdatedAt(),
		decided.ID().String(),
	)
}

// RescheduleWait moves the next attempt of a wait this attempt did not end, and
// answers ErrTransactionNotFound when the row is no longer in the wait.
func (r transactions) RescheduleWait(ctx context.Context, id identity.TransactionID, nextAttemptAt, at time.Time) error {
	return r.writeWait(ctx, "reschedule reference wait", rescheduleWaitRow, nextAttemptAt, at, id.String())
}

// writeWait runs the update and reads no row affected as the row having left the
// wait: the claim holds the lock, so the only way to miss is a row that is not
// what the caller decided over.
func (r transactions) writeWait(ctx context.Context, op, statement string, args ...any) error {
	tag, err := r.tx.Exec(ctx, statement, args...)
	if err != nil {
		return wrap(op, err)
	}
	if tag.RowsAffected() == 0 {
		return missingTransaction(op, pgx.ErrNoRows)
	}
	return nil
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
