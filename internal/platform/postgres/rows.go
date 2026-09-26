package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// The columns of one wager transaction, in one place: the repository inside a
// transaction and the read outside it answer the same row, and only the scope
// differs.
const transactionColumns = `
SELECT id, kind, player_id, wallet_id, amount_cents, currency,
       provider_id, external_id, idempotency_key, body_hash, round_id, game_id,
       reference_external_id, status, failure_code, observed_balance_cents,
       next_attempt_at, reference_deadline_at, attempt_count, created_at, updated_at
  FROM wager_transactions`

const selectTransactionByKey = transactionColumns + `
 WHERE provider_id = $1 AND idempotency_key = $2`

// The provider is part of the query and not of a check afterwards: a transaction
// of another provider leaves by the same path as one that does not exist, so no
// branch can tell the two apart.
const selectTransactionOfProvider = transactionColumns + `
 WHERE id = $1 AND provider_id = $2`

// The cited operation, found the way the provider names it. The provider is part
// of the query for the same reason as above.
const selectTransactionByExternalID = transactionColumns + `
 WHERE provider_id = $1 AND external_id = $2`

// The row of one wait, locked so that a single replica decides it. SKIP LOCKED
// is what makes the replica that loses skip instead of queueing behind the one
// that won, and the whole row comes back because the decision uses the state
// re-read here and not the one the scan saw.
// The schedule is part of the claim and not of a check afterwards: the scan takes
// no lock, so two replicas reading the same instant both offer the same row, and
// without this predicate the second one attempts a row whose next attempt the
// first has just moved into the future.
const lockWaitRow = transactionColumns + `
 WHERE id = $1
   AND next_attempt_at <= $2
   FOR UPDATE SKIP LOCKED`

// querier is what both scopes have in common. The open transaction and the pool
// answer the same row for the same query.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// transactionRow is the row as PostgreSQL hands it over, before the domain types
// take it back. A nullable column arrives as a pointer, which is what tells an
// absent value from a zero one.
type transactionRow struct {
	id          string
	kind        string
	playerID    string
	walletID    string
	cents       int64
	currency    string
	providerID  string
	externalID  string
	key         string
	bodyHash    string
	roundID     string
	gameID      string
	reference   *string
	status      string
	failure     *string
	observed    *int64
	nextAttempt *time.Time
	deadline    *time.Time
	attempts    int64
	createdAt   time.Time
	updatedAt   time.Time
}

func scanTransaction(ctx context.Context, from querier, query string, args ...any) (transactionRow, error) {
	var row transactionRow
	err := from.QueryRow(ctx, query, args...).Scan(
		&row.id, &row.kind, &row.playerID, &row.walletID, &row.cents, &row.currency,
		&row.providerID, &row.externalID, &row.key, &row.bodyHash, &row.roundID, &row.gameID,
		&row.reference, &row.status, &row.failure, &row.observed,
		&row.nextAttempt, &row.deadline, &row.attempts, &row.createdAt, &row.updatedAt,
	)
	return row, err
}

// wait is the row as the worker claims it: the state to rehydrate from, and the
// attempts already made, which are the window of the backoff and never part of
// the aggregate.
func (r transactionRow) wait() (storage.Wait, error) {
	state, err := r.state()
	if err != nil {
		return storage.Wait{}, err
	}
	return storage.Wait{State: state, Attempts: r.attempts}, nil
}

// state is the row as the aggregate rehydrates from it.
func (r transactionRow) state() (wager.State, error) {
	var parse rowParser
	state := wager.State{
		ID:                  parse.transactionID(r.id),
		Kind:                parse.kind(r.kind),
		PlayerID:            parse.playerID(r.playerID),
		WalletID:            parse.walletID(r.walletID),
		Amount:              parse.money(r.cents, r.currency),
		ProviderID:          parse.providerID(r.providerID),
		ExternalID:          parse.externalID(r.externalID),
		IdempotencyKey:      parse.idempotencyKey(r.key),
		BodyHash:            r.bodyHash,
		RoundID:             parse.roundID(r.roundID),
		GameID:              parse.gameID(r.gameID),
		ReferenceExternalID: parse.optionalExternalID(r.reference),
		Status:              parse.status(r.status),
		FailureCode:         parse.failureCode(r.failure),
		ObservedBalance:     parse.optionalMoney(r.observed, r.currency),
		NextAttemptAt:       instantOf(r.nextAttempt),
		ReferenceDeadlineAt: instantOf(r.deadline),
		CreatedAt:           r.createdAt,
		UpdatedAt:           r.updatedAt,
	}
	if parse.err != nil {
		return wager.State{}, wrap("read transaction row", parse.err)
	}
	return state, nil
}

// view is the row as a read answers it: the recorded outcome, with no aggregate
// rehydrated and no movement replayed.
func (r transactionRow) view() (storage.TransactionView, error) {
	var parse rowParser
	view := storage.TransactionView{
		ID:              parse.transactionID(r.id),
		Kind:            parse.kind(r.kind),
		Status:          parse.status(r.status),
		ProviderID:      parse.providerID(r.providerID),
		ExternalID:      parse.externalID(r.externalID),
		PlayerID:        parse.playerID(r.playerID),
		WalletID:        parse.walletID(r.walletID),
		RoundID:         parse.roundID(r.roundID),
		GameID:          parse.gameID(r.gameID),
		Amount:          parse.money(r.cents, r.currency),
		ObservedBalance: parse.optionalMoney(r.observed, r.currency),
		FailureCode:     parse.failureCode(r.failure),
		CreatedAt:       r.createdAt,
		UpdatedAt:       r.updatedAt,
	}
	if parse.err != nil {
		return storage.TransactionView{}, wrap("read transaction row", parse.err)
	}
	return view, nil
}

// rowParser turns the text of a row back into domain types and keeps the first
// refusal.
//
// A row of twenty columns would otherwise need a branch per column, and the
// whole set is refused the same way: a column the domain cannot parse is a row
// this context did not write.
type rowParser struct {
	err error
}

func (p *rowParser) keep(err error) {
	if p.err == nil {
		p.err = err
	}
}

func (p *rowParser) transactionID(text string) identity.TransactionID {
	parsed, err := identity.ParseTransactionID(text)
	p.keep(err)
	return parsed
}

func (p *rowParser) playerID(text string) identity.PlayerID {
	parsed, err := identity.ParsePlayerID(text)
	p.keep(err)
	return parsed
}

func (p *rowParser) walletID(text string) identity.WalletID {
	parsed, err := identity.ParseWalletID(text)
	p.keep(err)
	return parsed
}

func (p *rowParser) ledgerEntryID(text string) identity.LedgerEntryID {
	parsed, err := identity.ParseLedgerEntryID(text)
	p.keep(err)
	return parsed
}

func (p *rowParser) direction(text string) ledger.Direction {
	parsed, err := ledger.ParseDirection(text)
	p.keep(err)
	return parsed
}

func (p *rowParser) providerID(text string) identity.ProviderID {
	parsed, err := identity.ParseProviderID(text)
	p.keep(err)
	return parsed
}

func (p *rowParser) externalID(text string) identity.ExternalTransactionID {
	parsed, err := identity.ParseExternalTransactionID(text)
	p.keep(err)
	return parsed
}

// optionalExternalID answers the zero identifier for a NULL column, which is
// what a transaction citing no other operation carries.
func (p *rowParser) optionalExternalID(text *string) identity.ExternalTransactionID {
	if text == nil {
		return identity.ExternalTransactionID{}
	}
	return p.externalID(*text)
}

func (p *rowParser) idempotencyKey(text string) identity.IdempotencyKey {
	parsed, err := identity.ParseIdempotencyKey(text)
	p.keep(err)
	return parsed
}

func (p *rowParser) roundID(text string) identity.RoundID {
	parsed, err := identity.ParseRoundID(text)
	p.keep(err)
	return parsed
}

func (p *rowParser) gameID(text string) identity.GameID {
	parsed, err := identity.ParseGameID(text)
	p.keep(err)
	return parsed
}

func (p *rowParser) kind(text string) wager.Kind {
	parsed, err := wager.ParseKind(text)
	p.keep(err)
	return parsed
}

func (p *rowParser) status(text string) wager.Status {
	parsed, err := wager.ParseStatus(text)
	p.keep(err)
	return parsed
}

// failureCode answers the zero code for a NULL column, which is what a
// transaction no rule refused carries.
func (p *rowParser) failureCode(text *string) wager.FailureCode {
	if text == nil {
		return wager.FailureCode(0)
	}
	parsed, err := wager.ParseFailureCode(*text)
	p.keep(err)
	return parsed
}

func (p *rowParser) money(cents int64, currency string) money.Money {
	parsed, err := balanceOf(currency, cents)
	p.keep(err)
	return parsed
}

// optionalMoney answers the zero Money for a NULL column. The zero value carries
// no currency, which is what tells an absent balance from a balance of zero.
func (p *rowParser) optionalMoney(cents *int64, currency string) money.Money {
	if cents == nil {
		return money.Money{}
	}
	return p.money(*cents, currency)
}

func instantOf(at *time.Time) time.Time {
	if at == nil {
		return time.Time{}
	}
	return *at
}
