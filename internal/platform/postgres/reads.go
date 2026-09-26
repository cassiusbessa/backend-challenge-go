package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

const selectWallet = `
SELECT id, player_id, currency, balance_cents, version, created_at, updated_at
  FROM wallets
 WHERE id = $1`

// The queue of the waits whose scheduled instant has come, earliest first.
//
// It takes no lock and opens no transaction: the scan only chooses candidates,
// so it never stands between a submission and the wallet that submission moves.
// The partial index of the second migration serves exactly this predicate, so
// the terminal rows — the overwhelming majority of the table — are not walked.
const selectDueWaits = `
SELECT id, wallet_id
  FROM wager_transactions
 WHERE status = 'PENDING_REFERENCE' AND next_attempt_at <= $1
 ORDER BY next_attempt_at
 LIMIT $2`

// The instant the oldest wait entered the queue. The entry is the creation of
// the row: a wait is recorded in the very commit that creates the transaction.
// The partial index of the second migration covers the predicate, so the
// terminal rows are not walked.
const selectOldestWait = `
SELECT min(created_at)
  FROM wager_transactions
 WHERE status = 'PENDING_REFERENCE'`

// The page of wallets a sweep walks, in the order of the identity. The zero
// identity is the nil UUID, which sorts before every identity the process
// mints, so the first page asks for everything after it.
const selectWalletIDsAfter = `
SELECT id
  FROM wallets
 WHERE id > $1
 ORDER BY id
 LIMIT $2`

// Reads answers read models from the pool. A read opens no transaction and
// writes nothing. The zero value is not used: NewReads is the only constructor.
type Reads struct {
	source *Pool
}

func NewReads(source *Pool) *Reads {
	return &Reads{source: source}
}

// Wallet answers the stored wallet, or ErrWalletNotFound when the identity is
// not a wallet of this context.
func (r *Reads) Wallet(ctx context.Context, id identity.WalletID) (storage.WalletView, error) {
	pool, err := r.source.Querier()
	if err != nil {
		return storage.WalletView{}, wrap("acquire pool", err)
	}
	var found walletRow
	err = pool.QueryRow(ctx, selectWallet, id.String()).Scan(
		&found.id, &found.playerID, &found.currency,
		&found.cents, &found.version, &found.createdAt, &found.updatedAt,
	)
	if err != nil {
		return storage.WalletView{}, missingWallet("read wallet", err)
	}
	return found.view()
}

// Transaction answers the recorded outcome of one transaction of that provider.
// A transaction of another provider answers ErrTransactionNotFound, the same as
// one that does not exist.
func (r *Reads) Transaction(ctx context.Context, id identity.TransactionID, provider identity.ProviderID) (storage.TransactionView, error) {
	pool, err := r.source.Querier()
	if err != nil {
		return storage.TransactionView{}, wrap("acquire pool", err)
	}
	row, err := scanTransaction(ctx, pool, selectTransactionOfProvider, id.String(), provider.String())
	if err != nil {
		return storage.TransactionView{}, missingTransaction("read transaction", err)
	}
	return row.view()
}

// TransactionByKey answers the transaction of that provider and key from outside
// any transaction, which is what the loser of the unique constraint needs: the
// violation aborts its SQL transaction, so the winning row is only readable after
// the rollback.
func (r *Reads) TransactionByKey(ctx context.Context, provider identity.ProviderID, key identity.IdempotencyKey) (wager.State, error) {
	pool, err := r.source.Querier()
	if err != nil {
		return wager.State{}, wrap("acquire pool", err)
	}
	row, err := scanTransaction(ctx, pool, selectTransactionByKey, provider.String(), key.String())
	if err != nil {
		return wager.State{}, missingTransaction("read transaction by key", err)
	}
	return row.state()
}

// DueWaits answers the waits whose scheduled instant has come, the earliest
// schedule first, up to the limit asked.
//
// The wallet travels with each candidate because the decision locks the wallet
// before the row of the wait, and that order cannot be taken from a row the
// caller has not read yet. The wallet of a transaction never changes, so reading
// it here answers the same identity the locked row carries.
func (r *Reads) DueWaits(ctx context.Context, now time.Time, limit int) ([]storage.WaitCandidate, error) {
	pool, err := r.source.Querier()
	if err != nil {
		return nil, wrap("acquire pool", err)
	}
	rows, err := pool.Query(ctx, selectDueWaits, now, limit)
	if err != nil {
		return nil, wrap("scan reference waits", err)
	}
	defer rows.Close()
	return scanCandidates(rows)
}

// OldestWait answers the age of the oldest wait at that instant, and zero when
// nothing is waiting.
func (r *Reads) OldestWait(ctx context.Context, now time.Time) (time.Duration, error) {
	pool, err := r.source.Querier()
	if err != nil {
		return 0, wrap("acquire pool", err)
	}
	var oldest *time.Time
	if err := pool.QueryRow(ctx, selectOldestWait).Scan(&oldest); err != nil {
		return 0, wrap("read the oldest reference wait", err)
	}
	return ageOf(now, oldest), nil
}

// WalletIDsAfter answers the identities of the wallets after that one, in the
// order of the identity, up to the limit asked.
func (r *Reads) WalletIDsAfter(ctx context.Context, after identity.WalletID, limit int) ([]identity.WalletID, error) {
	pool, err := r.source.Querier()
	if err != nil {
		return nil, wrap("acquire pool", err)
	}
	rows, err := pool.Query(ctx, selectWalletIDsAfter, after.String(), limit)
	if err != nil {
		return nil, wrap("page the wallets", err)
	}
	defer rows.Close()
	return scanWalletIDs(rows)
}

func scanWalletIDs(rows pgx.Rows) ([]identity.WalletID, error) {
	var page []identity.WalletID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, wrap("read a wallet identity", err)
		}
		id, err := identity.ParseWalletID(raw)
		if err != nil {
			return nil, wrap("read a wallet identity", err)
		}
		page = append(page, id)
	}
	return page, wrap("page the wallets", rows.Err())
}

// ageOf answers how long the oldest wait has been waiting, and zero when there
// is none. An entry stamped after the instant asked about — two clocks that
// disagree — reads as no age rather than a negative one.
func ageOf(now time.Time, oldest *time.Time) time.Duration {
	if oldest == nil {
		return 0
	}
	return max(now.Sub(*oldest), 0)
}

func scanCandidates(rows pgx.Rows) ([]storage.WaitCandidate, error) {
	var due []storage.WaitCandidate
	for rows.Next() {
		candidate, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		due = append(due, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("scan reference waits", err)
	}
	return due, nil
}

func scanCandidate(rows pgx.Rows) (storage.WaitCandidate, error) {
	var id, walletID string
	if err := rows.Scan(&id, &walletID); err != nil {
		return storage.WaitCandidate{}, wrap("read reference wait row", err)
	}
	var parse rowParser
	candidate := storage.WaitCandidate{
		TransactionID: parse.transactionID(id),
		WalletID:      parse.walletID(walletID),
	}
	if parse.err != nil {
		return storage.WaitCandidate{}, wrap("read reference wait row", parse.err)
	}
	return candidate, nil
}

// walletRow is the row as PostgreSQL hands it over, before the domain types
// take it back.
type walletRow struct {
	id        string
	playerID  string
	currency  string
	cents     int64
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

func (r walletRow) view() (storage.WalletView, error) {
	id, err := identity.ParseWalletID(r.id)
	if err != nil {
		return storage.WalletView{}, wrap("read wallet identity", err)
	}
	playerID, err := identity.ParsePlayerID(r.playerID)
	if err != nil {
		return storage.WalletView{}, wrap("read player identity", err)
	}
	balance, err := balanceOf(r.currency, r.cents)
	if err != nil {
		return storage.WalletView{}, wrap("read wallet balance", err)
	}
	return storage.WalletView{
		ID:        id,
		PlayerID:  playerID,
		Balance:   balance,
		Version:   r.version,
		CreatedAt: r.createdAt,
		UpdatedAt: r.updatedAt,
	}, nil
}
