package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

// The wallet is asked for before the page, in a round trip of its own, so an
// absent wallet and a wallet with no movements do not both come back as zero
// rows. Two round trips are safe here because nothing deletes a wallet: one that
// existed for this statement exists for the next. See ADR 0023.
const existsWallet = `
SELECT id
  FROM wallets
 WHERE id = $1`

// One page of the ledger, keyset over the pair that orders it.
//
// The identity is kept in the predicate and in the ORDER BY even though, inside
// one wallet, UNIQUE (wallet_id, sequence_number) never lets it decide: the
// order of the ledger is the pair, as go-reads and the domain collection fix
// it, and the cost of carrying it is zero. The unique index serves both the
// predicate and the order.
const selectEntriesAfter = `
SELECT id, transaction_id, direction, amount_cents, currency,
       balance_before_cents, balance_after_cents, sequence_number, created_at
  FROM ledger_entries
 WHERE wallet_id = $1 AND (sequence_number, id) > ($2, $3)
 ORDER BY sequence_number, id
 LIMIT $4`

// The wallet and its ledger aggregated in one statement, so both sides come from
// one snapshot without a transaction and without a lock. See ADR 0022.
//
// The sum is signed by the direction — credit adds, debit subtracts — and cast
// back to bigint: SUM over bigint answers numeric, and a ledger whose sum no
// longer fits int64 fails the cast, which leaves as an infrastructure failure
// rather than as a verdict. The break of the chain is the lowest sequence whose
// balance before is not what the entry before it left, with zero as what the
// first entry starts from; it is measured here because it needs the whole ledger
// and carrying every row to Go to compute one number would not be a read.
const selectLedgerSummary = `
WITH entries AS (
    SELECT direction, amount_cents, balance_before_cents, sequence_number,
           lag(balance_after_cents, 1, 0) OVER (ORDER BY sequence_number, id) AS previous_after
      FROM ledger_entries
     WHERE wallet_id = $1
), summary AS (
    SELECT coalesce(sum(CASE direction WHEN 'CREDIT' THEN amount_cents ELSE -amount_cents END), 0)::bigint AS ledger_balance,
           count(*) AS entry_count,
           coalesce(max(sequence_number), 0) AS last_sequence,
           coalesce(min(sequence_number) FILTER (WHERE balance_before_cents <> previous_after), 0) AS first_break
      FROM entries
)
SELECT w.id, w.player_id, w.currency, w.balance_cents, w.version, w.created_at, w.updated_at,
       s.ledger_balance, s.entry_count, s.last_sequence, s.first_break
  FROM wallets AS w
 CROSS JOIN summary AS s
 WHERE w.id = $1`

// pageQuerier is what the two ledger reads need from the pool: one row for the
// wallet and the summary, a result set for the page.
type pageQuerier interface {
	querier
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Ledger answers one page of the ledger of that wallet after the position, or
// ErrWalletNotFound when the identity is not a wallet of this context.
func (r *Reads) Ledger(ctx context.Context, id identity.WalletID, after storage.EntryPosition, limit int) ([]storage.EntryView, error) {
	pool, err := r.source.Querier()
	if err != nil {
		return nil, wrap("acquire pool", err)
	}
	return entriesOf(ctx, pool, id, after, limit)
}

func entriesOf(ctx context.Context, from pageQuerier, id identity.WalletID, after storage.EntryPosition, limit int) ([]storage.EntryView, error) {
	var found string
	if err := from.QueryRow(ctx, existsWallet, id.String()).Scan(&found); err != nil {
		return nil, missingWallet("read wallet", err)
	}
	rows, err := from.Query(ctx, selectEntriesAfter, id.String(), after.Sequence, after.EntryID.String(), limit)
	if err != nil {
		return nil, wrap("read ledger page", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

func scanEntries(rows pgx.Rows) ([]storage.EntryView, error) {
	var page []storage.EntryView
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		page = append(page, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("read ledger page", err)
	}
	return page, nil
}

func scanEntry(rows pgx.Rows) (storage.EntryView, error) {
	var row entryRow
	err := rows.Scan(
		&row.id, &row.transactionID, &row.direction, &row.cents, &row.currency,
		&row.before, &row.after, &row.sequence, &row.createdAt,
	)
	if err != nil {
		return storage.EntryView{}, wrap("read ledger entry row", err)
	}
	return row.view()
}

// Summary answers the stored wallet and its ledger aggregated from one
// snapshot, or ErrWalletNotFound when the identity is not a wallet of this
// context.
func (r *Reads) Summary(ctx context.Context, id identity.WalletID) (storage.LedgerSummary, error) {
	pool, err := r.source.Querier()
	if err != nil {
		return storage.LedgerSummary{}, wrap("acquire pool", err)
	}
	return summaryOf(ctx, pool, id)
}

func summaryOf(ctx context.Context, from querier, id identity.WalletID) (storage.LedgerSummary, error) {
	var row summaryRow
	err := from.QueryRow(ctx, selectLedgerSummary, id.String()).Scan(
		&row.wallet.id, &row.wallet.playerID, &row.wallet.currency,
		&row.wallet.cents, &row.wallet.version, &row.wallet.createdAt, &row.wallet.updatedAt,
		&row.ledgerBalance, &row.entryCount, &row.lastSequence, &row.firstBreak,
	)
	if err != nil {
		return storage.LedgerSummary{}, missingWallet("read ledger summary", err)
	}
	return row.summary()
}

// entryRow is the ledger row as PostgreSQL hands it over, before the domain
// types take it back.
type entryRow struct {
	id            string
	transactionID string
	direction     string
	cents         int64
	currency      string
	before        int64
	after         int64
	sequence      int64
	createdAt     time.Time
}

func (r entryRow) view() (storage.EntryView, error) {
	var parse rowParser
	view := storage.EntryView{
		ID:            parse.ledgerEntryID(r.id),
		TransactionID: parse.transactionID(r.transactionID),
		Direction:     parse.direction(r.direction),
		Amount:        parse.money(r.cents, r.currency),
		BalanceBefore: parse.money(r.before, r.currency),
		BalanceAfter:  parse.money(r.after, r.currency),
		Sequence:      r.sequence,
		CreatedAt:     r.createdAt,
	}
	if parse.err != nil {
		return storage.EntryView{}, wrap("read ledger entry row", parse.err)
	}
	return view, nil
}

// summaryRow is the joined row of the wallet and its aggregated ledger.
type summaryRow struct {
	wallet        walletRow
	ledgerBalance int64
	entryCount    int64
	lastSequence  int64
	firstBreak    int64
}

func (r summaryRow) summary() (storage.LedgerSummary, error) {
	view, err := r.wallet.view()
	if err != nil {
		return storage.LedgerSummary{}, err
	}
	return storage.LedgerSummary{
		Wallet:             view,
		LedgerBalance:      r.ledgerBalance,
		EntryCount:         r.entryCount,
		LastSequence:       r.lastSequence,
		FirstBreakSequence: r.firstBreak,
	}, nil
}
