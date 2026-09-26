//go:build integration

// The suite walks the invariants of go-db-invariants against a real PostgreSQL.
// Every case asks the database for a write the aggregate would never produce,
// because the point is what the database refuses on its own.
package schema

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestWallets_refuseABalanceBelowZero(t *testing.T) {
	ctx, conn := connect(t)
	err := insertWallet(ctx, conn, wallet{id: newID(), player: newID(), currency: "BRL", cents: -1, version: 1})
	assertRefused(t, err, "wallets_balance_is_not_negative")
}

func TestWallets_refuseAVersionBelowOne(t *testing.T) {
	ctx, conn := connect(t)
	err := insertWallet(ctx, conn, wallet{id: newID(), player: newID(), currency: "BRL", cents: 0, version: 0})
	assertRefused(t, err, "wallets_version_starts_at_one")
}

func TestWallets_refuseTheSamePlayerAndCurrencyTwiceAndKeepTheFirstIntact(t *testing.T) {
	ctx, conn := connect(t)
	player := newID()
	first := wallet{id: newID(), player: player, currency: "BRL", cents: 0, version: 1}
	if err := insertWallet(ctx, conn, first); err != nil {
		t.Fatalf("first wallet = %v, want nil", err)
	}
	err := insertWallet(ctx, conn, wallet{id: newID(), player: player, currency: "BRL", cents: 0, version: 1})
	assertRefused(t, err, "wallets_one_per_player_and_currency")
	if got := storedBalance(ctx, t, conn, first.id); got != 0 {
		t.Fatalf("balance of the first wallet = %d, want 0", got)
	}
}

func TestWallets_acceptTheSamePlayerInAnotherCurrency(t *testing.T) {
	ctx, conn := connect(t)
	player := newID()
	if err := insertWallet(ctx, conn, wallet{id: newID(), player: player, currency: "BRL", cents: 0, version: 1}); err != nil {
		t.Fatalf("BRL wallet = %v, want nil", err)
	}
	if err := insertWallet(ctx, conn, wallet{id: newID(), player: player, currency: "USD", cents: 0, version: 1}); err != nil {
		t.Fatalf("USD wallet = %v, want nil", err)
	}
}

func TestWagerTransactions_refuseTheStatusesAndFieldsTheRuleForbids(t *testing.T) {
	ctx, conn := connect(t)
	host := emptyWallet(ctx, t, conn)
	cases := []struct {
		name       string
		row        transaction
		constraint string
	}{
		{
			name:       "a pending row cannot be written",
			row:        opening(host, 100).with(func(r *transaction) { r.status = "PENDING" }),
			constraint: "wager_transactions_status_is_writable",
		},
		{
			name:       "an opening with provider fields is refused",
			row:        opening(host, 100).with(func(r *transaction) { r.provider, r.external = text("provider-a"), text("x-1") }),
			constraint: "wager_transactions_opening_is_internal",
		},
		{
			name:       "an external row without round and game is refused",
			row:        external(host, "BET", 100).with(func(r *transaction) { r.round, r.game = nil, nil }),
			constraint: "wager_transactions_external_carries_provider_fields",
		},
		{
			name:       "a loss with a positive amount is refused",
			row:        external(host, "LOSS", 100),
			constraint: "wager_transactions_amount_matches_kind",
		},
		{
			name:       "a bet with a zero amount is refused",
			row:        external(host, "BET", 0),
			constraint: "wager_transactions_amount_matches_kind",
		},
		{
			name:       "a refund without the cited operation is refused",
			row:        external(host, "REFUND", 100),
			constraint: "wager_transactions_reversal_cites_reference",
		},
		{
			name:       "a rollback without the cited operation is refused",
			row:        external(host, "ROLLBACK", 100),
			constraint: "wager_transactions_reversal_cites_reference",
		},
		{
			name:       "a rejected row without a token is refused",
			row:        external(host, "BET", 100).with(func(r *transaction) { r.status, r.observed = "REJECTED", nil }),
			constraint: "wager_transactions_closed_by_rule_names_failure",
		},
		{
			name:       "a failed row without a token is refused",
			row:        external(host, "BET", 100).with(func(r *transaction) { r.status, r.observed = "FAILED", nil }),
			constraint: "wager_transactions_closed_by_rule_names_failure",
		},
		{
			name:       "a processed row without the observed balance is refused",
			row:        external(host, "BET", 100).with(func(r *transaction) { r.observed = nil }),
			constraint: "wager_transactions_processed_records_balance",
		},
		{
			// The deadline is carried so that the row breaks the schedule and
			// nothing else: a row missing both would leave which constraint
			// answered up to the database.
			name: "a waiting row without the next attempt is refused",
			row: external(host, "REFUND", 100).with(func(r *transaction) {
				r.reference, r.status, r.observed = text("bet-1"), "PENDING_REFERENCE", nil
				r.deadline = instant(waitEntered.Add(15 * time.Minute))
			}),
			constraint: "wager_transactions_waiting_has_next_attempt",
		},
		{
			// A wait with no deadline expires on its first attempt: the worker
			// reads the zero instant, finds it past and closes the wait one tick
			// after it was recorded instead of fifteen minutes after.
			name: "a waiting row without the deadline is refused",
			row: external(host, "REFUND", 100).with(func(r *transaction) {
				r.reference, r.status, r.observed = text("bet-1"), "PENDING_REFERENCE", nil
				r.next = instant(waitEntered)
			}),
			constraint: "wager_transactions_waiting_has_deadline",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, insertTransaction(ctx, conn, tc.row), tc.constraint)
		})
	}
}

func TestWagerTransactions_acceptOnlyOneOpeningPerWallet(t *testing.T) {
	ctx, conn := connect(t)
	host := emptyWallet(ctx, t, conn)
	if err := insertTransaction(ctx, conn, opening(host, 100)); err != nil {
		t.Fatalf("first opening = %v, want nil", err)
	}
	err := insertTransaction(ctx, conn, opening(host, 100))
	assertRefused(t, err, "wager_transactions_one_opening_per_wallet")
}

func TestWagerTransactions_acceptOneProcessedReversalPerCitedOperation(t *testing.T) {
	ctx, conn := connect(t)
	host := emptyWallet(ctx, t, conn)
	cited := external(host, "BET", 100)
	if err := insertTransaction(ctx, conn, cited); err != nil {
		t.Fatalf("cited bet = %v, want nil", err)
	}
	rejected := external(host, "REFUND", 100).with(func(r *transaction) {
		r.reference, r.status, r.failure, r.observed = cited.external, "REJECTED", text("ALREADY_REVERSED"), nil
	})
	if err := insertTransaction(ctx, conn, rejected); err != nil {
		t.Fatalf("rejected reversal = %v, want nil: it does not take the slot", err)
	}
	first := external(host, "REFUND", 100).with(func(r *transaction) { r.reference = cited.external })
	if err := insertTransaction(ctx, conn, first); err != nil {
		t.Fatalf("first processed reversal = %v, want nil", err)
	}
	second := external(host, "ROLLBACK", 100).with(func(r *transaction) { r.reference = cited.external })
	assertRefused(t, insertTransaction(ctx, conn, second), "wager_transactions_one_processed_reversal_per_reference")
	if got := storedStatus(ctx, t, conn, first.id); got != "PROCESSED" {
		t.Fatalf("status of the first reversal = %s, want PROCESSED", got)
	}
}

func TestWagerTransactions_acceptOneRowPerProviderOperationAndKey(t *testing.T) {
	ctx, conn := connect(t)
	host := emptyWallet(ctx, t, conn)
	first := external(host, "BET", 100)
	if err := insertTransaction(ctx, conn, first); err != nil {
		t.Fatalf("first bet = %v, want nil", err)
	}
	sameOperation := external(host, "WIN", 100).with(func(r *transaction) { r.external = first.external })
	assertRefused(t, insertTransaction(ctx, conn, sameOperation), "wager_transactions_one_per_provider_and_external_id")
	sameKey := external(host, "WIN", 100).with(func(r *transaction) { r.key = first.key })
	assertRefused(t, insertTransaction(ctx, conn, sameKey), "wager_transactions_one_per_provider_and_key")
}

func TestLedgerEntries_refuseAnUpdateAndKeepTheOriginalValues(t *testing.T) {
	ctx, conn := connect(t)
	host, movement := creditedWallet(ctx, t, conn, 100)
	_, err := conn.Exec(ctx, "UPDATE ledger_entries SET amount_cents = 1 WHERE id = $1", movement.id)
	if err == nil {
		t.Fatalf("update on a ledger row = nil, want a refusal")
	}
	amount, after := storedEntry(ctx, t, conn, movement.id)
	if amount != 100 || after != 100 {
		t.Fatalf("entry = %d and %d, want the original 100 and 100", amount, after)
	}
	if got := storedBalance(ctx, t, conn, host); got != 100 {
		t.Fatalf("balance = %d, want the original 100", got)
	}
}

func TestLedgerEntries_refuseADeleteAndATruncate(t *testing.T) {
	ctx, conn := connect(t)
	_, movement := creditedWallet(ctx, t, conn, 100)
	if _, err := conn.Exec(ctx, "DELETE FROM ledger_entries WHERE id = $1", movement.id); err == nil {
		t.Fatalf("delete on a ledger row = nil, want a refusal")
	}
	if _, err := conn.Exec(ctx, "TRUNCATE ledger_entries"); err == nil {
		t.Fatalf("truncate on the ledger = nil, want a refusal")
	}
	if amount, _ := storedEntry(ctx, t, conn, movement.id); amount != 100 {
		t.Fatalf("entry amount = %d, want the original 100", amount)
	}
}

func TestLedgerEntries_refuseAnEntryThatDoesNotMatchItsTransaction(t *testing.T) {
	ctx, conn := connect(t)
	host := emptyWallet(ctx, t, conn)
	other := emptyWallet(ctx, t, conn)
	recorded := external(host, "WIN", 700)
	if err := insertTransaction(ctx, conn, recorded); err != nil {
		t.Fatalf("transaction = %v, want nil", err)
	}
	cases := []struct {
		name string
		row  entry
	}{
		{
			name: "another amount is refused",
			row:  credit(host, recorded.id, 600, 0),
		},
		{
			name: "another wallet is refused",
			row:  credit(other, recorded.id, 700, 0),
		},
		{
			name: "another currency is refused",
			row:  credit(host, recorded.id, 700, 0).withCurrency("USD"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, insertEntry(ctx, conn, tc.row), "ledger_entries_match_their_transaction")
		})
	}
}

func TestLedgerEntries_acceptOnlyOnePerTransaction(t *testing.T) {
	ctx, conn := connect(t)
	host, movement := creditedWallet(ctx, t, conn, 100)
	second := credit(host, movement.transaction, 100, 100).withSequence(2)
	assertRefused(t, insertEntry(ctx, conn, second), "ledger_entries_one_per_transaction")
}

func TestLedgerEntries_refuseANonPositiveAmountAndADirectionThatDoesNotAddUp(t *testing.T) {
	ctx, conn := connect(t)
	host := emptyWallet(ctx, t, conn)
	zero := external(host, "LOSS", 0)
	if err := insertTransaction(ctx, conn, zero); err != nil {
		t.Fatalf("loss transaction = %v, want nil", err)
	}
	assertRefused(t, insertEntry(ctx, conn, credit(host, zero.id, 0, 0)), "ledger_entries_amount_is_positive")

	moved := external(host, "WIN", 500)
	if err := insertTransaction(ctx, conn, moved); err != nil {
		t.Fatalf("win transaction = %v, want nil", err)
	}
	drifting := credit(host, moved.id, 500, 0).withAfter(400)
	assertRefused(t, insertEntry(ctx, conn, drifting), "ledger_entries_direction_moves_the_balance")
}

// The wallet balance and the last entry may disagree in the middle of the SQL
// transaction, and the deferred trigger is what closes that gap at commit.
func TestWalletBalance_mustMatchTheLastEntryAtCommit(t *testing.T) {
	ctx, conn := connect(t)
	host := emptyWallet(ctx, t, conn)
	recorded := external(host, "WIN", 500)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin = %v, want nil", err)
	}
	if err := insertTransaction(ctx, tx, recorded); err != nil {
		t.Fatalf("transaction = %v, want nil", err)
	}
	if err := insertEntry(ctx, tx, credit(host, recorded.id, 500, 0)); err != nil {
		t.Fatalf("entry = %v, want nil: the two may disagree mid transaction", err)
	}
	if err := tx.Commit(ctx); err == nil {
		t.Fatalf("commit with a wallet left behind = nil, want a refusal")
	}
	if got := storedBalance(ctx, t, conn, host); got != 0 {
		t.Fatalf("balance = %d, want the untouched 0", got)
	}
}

func TestWalletBalance_commitsWhenTheWalletAndTheEntryAgree(t *testing.T) {
	ctx, conn := connect(t)
	host, movement := creditedWallet(ctx, t, conn, 250)
	if got := storedBalance(ctx, t, conn, host); got != 250 {
		t.Fatalf("balance = %d, want 250", got)
	}
	_, after := storedEntry(ctx, t, conn, movement.id)
	if after != 250 {
		t.Fatalf("balance after the entry = %d, want 250", after)
	}
}

// The application operates as the role the migration created, so what the
// process may write is what this role may write.
func TestApplicationRole_holdsOnlySelectAndInsertOnTheLedger(t *testing.T) {
	ctx, conn := connect(t)
	_, movement := creditedWallet(ctx, t, conn, 100)
	if _, err := conn.Exec(ctx, "SET ROLE wager_app"); err != nil {
		t.Fatalf("set role = %v, want nil", err)
	}
	if _, err := conn.Exec(ctx, "SELECT 1 FROM ledger_entries WHERE id = $1", movement.id); err != nil {
		t.Fatalf("select as the application role = %v, want nil", err)
	}
	if _, err := conn.Exec(ctx, "UPDATE ledger_entries SET amount_cents = 1 WHERE id = $1", movement.id); err == nil {
		t.Fatalf("update as the application role = nil, want a refusal")
	}
	if _, err := conn.Exec(ctx, "DELETE FROM ledger_entries WHERE id = $1", movement.id); err == nil {
		t.Fatalf("delete as the application role = nil, want a refusal")
	}
}

// querier is what both a connection and an open transaction answer, so a case
// can build its rows either way.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type wallet struct {
	id       string
	player   string
	currency string
	cents    int64
	version  int64
}

type transaction struct {
	id        string
	kind      string
	player    string
	walletID  string
	cents     int64
	currency  string
	provider  *string
	external  *string
	key       *string
	hash      *string
	round     *string
	game      *string
	reference *string
	status    string
	failure   *string
	observed  *int64
	next      *time.Time
	deadline  *time.Time
}

type entry struct {
	id          string
	walletID    string
	transaction string
	direction   string
	cents       int64
	currency    string
	before      int64
	after       int64
	sequence    int64
}

func (r transaction) with(change func(*transaction)) transaction {
	change(&r)
	return r
}

func (r entry) withCurrency(code string) entry {
	r.currency = code
	return r
}

func (r entry) withAfter(cents int64) entry {
	r.after = cents
	return r
}

func (r entry) withSequence(number int64) entry {
	r.sequence = number
	return r
}

func opening(walletID string, cents int64) transaction {
	observed := cents
	return transaction{
		id:       newID(),
		kind:     "OPENING",
		player:   newID(),
		walletID: walletID,
		cents:    cents,
		currency: "BRL",
		status:   "PROCESSED",
		observed: &observed,
	}
}

func external(walletID, kind string, cents int64) transaction {
	observed := cents
	id := newID()
	return transaction{
		id:       id,
		kind:     kind,
		player:   newID(),
		walletID: walletID,
		cents:    cents,
		currency: "BRL",
		provider: text("provider-a"),
		external: text("ext-" + id),
		key:      text("key-" + id),
		hash:     text("hash"),
		round:    text("round-1"),
		game:     text("game-1"),
		status:   "PROCESSED",
		observed: &observed,
	}
}

func credit(walletID, transactionID string, cents, before int64) entry {
	return entry{
		id:          newID(),
		walletID:    walletID,
		transaction: transactionID,
		direction:   "CREDIT",
		cents:       cents,
		currency:    "BRL",
		before:      before,
		after:       before + cents,
		sequence:    1,
	}
}

const insertWalletSQL = `
INSERT INTO wallets (id, player_id, currency, balance_cents, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, now(), now())`

func insertWallet(ctx context.Context, conn querier, row wallet) error {
	_, err := conn.Exec(ctx, insertWalletSQL, row.id, row.player, row.currency, row.cents, row.version)
	return err
}

const insertTransactionSQL = `
INSERT INTO wager_transactions (
    id, kind, player_id, wallet_id, amount_cents, currency,
    provider_id, external_id, idempotency_key, body_hash, round_id, game_id,
    reference_external_id, status, failure_code, observed_balance_cents,
    next_attempt_at, reference_deadline_at, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18, now(), now())`

func insertTransaction(ctx context.Context, conn querier, row transaction) error {
	_, err := conn.Exec(ctx, insertTransactionSQL,
		row.id, row.kind, row.player, row.walletID, row.cents, row.currency,
		row.provider, row.external, row.key, row.hash, row.round, row.game,
		row.reference, row.status, row.failure, row.observed, row.next, row.deadline,
	)
	return err
}

const insertEntrySQL = `
INSERT INTO ledger_entries (
    id, wallet_id, transaction_id, direction, amount_cents, currency,
    balance_before_cents, balance_after_cents, sequence_number, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now())`

func insertEntry(ctx context.Context, conn querier, row entry) error {
	_, err := conn.Exec(ctx, insertEntrySQL,
		row.id, row.walletID, row.transaction, row.direction, row.cents, row.currency,
		row.before, row.after, row.sequence,
	)
	return err
}

// emptyWallet is the host of a case that only needs a wallet to point at.
func emptyWallet(ctx context.Context, t *testing.T, conn querier) string {
	t.Helper()
	row := wallet{id: newID(), player: newID(), currency: "BRL", cents: 0, version: 1}
	if err := insertWallet(ctx, conn, row); err != nil {
		t.Fatalf("host wallet = %v, want nil", err)
	}
	return row.id
}

// creditedWallet builds a wallet that already holds a committed movement, with
// the balance and the entry in agreement.
func creditedWallet(ctx context.Context, t *testing.T, conn *pgx.Conn, cents int64) (string, entry) {
	t.Helper()
	host := wallet{id: newID(), player: newID(), currency: "BRL", cents: cents, version: 1}
	recorded := opening(host.id, cents)
	recorded.player = host.player
	movement := credit(host.id, recorded.id, cents, 0)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin = %v, want nil", err)
	}
	if err := insertWallet(ctx, tx, host); err != nil {
		t.Fatalf("wallet = %v, want nil", err)
	}
	if err := insertTransaction(ctx, tx, recorded); err != nil {
		t.Fatalf("transaction = %v, want nil", err)
	}
	if err := insertEntry(ctx, tx, movement); err != nil {
		t.Fatalf("entry = %v, want nil", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit = %v, want nil", err)
	}
	return host.id, movement
}

func storedBalance(ctx context.Context, t *testing.T, conn *pgx.Conn, walletID string) int64 {
	t.Helper()
	var cents int64
	if err := conn.QueryRow(ctx, "SELECT balance_cents FROM wallets WHERE id = $1", walletID).Scan(&cents); err != nil {
		t.Fatalf("read balance = %v, want nil", err)
	}
	return cents
}

func storedStatus(ctx context.Context, t *testing.T, conn *pgx.Conn, transactionID string) string {
	t.Helper()
	var status string
	if err := conn.QueryRow(ctx, "SELECT status FROM wager_transactions WHERE id = $1", transactionID).Scan(&status); err != nil {
		t.Fatalf("read status = %v, want nil", err)
	}
	return status
}

func storedEntry(ctx context.Context, t *testing.T, conn *pgx.Conn, entryID string) (int64, int64) {
	t.Helper()
	var amount, after int64
	err := conn.QueryRow(ctx, "SELECT amount_cents, balance_after_cents FROM ledger_entries WHERE id = $1", entryID).Scan(&amount, &after)
	if err != nil {
		t.Fatalf("read entry = %v, want nil", err)
	}
	return amount, after
}

// assertRefused names the constraint that must have refused the write, so a row
// that fails for another reason does not pass as the case under test.
func assertRefused(t *testing.T, err error, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("write = nil, want a refusal by %s", constraint)
	}
	if !refusedBy(err, constraint) {
		t.Fatalf("write = %v, want a refusal by %s", err, constraint)
	}
}

// refusedBy names the constraint the database used, so a row refused for another
// reason does not pass as the case under test.
func refusedBy(err error, constraint string) bool {
	var refusal *pgconn.PgError
	if !errors.As(err, &refusal) {
		return false
	}
	return refusal.ConstraintName == constraint
}

func connect(t *testing.T) (context.Context, *pgx.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	conn, err := pgx.Connect(ctx, databaseURL())
	if err != nil {
		t.Fatalf("connect = %v, want nil: the suite needs the migration applied", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return ctx, conn
}

// suiteDatabaseURL is where this suite lands when DATABASE_URL is unset. It is
// never the database the running application uses: the outbox relay of that
// process scans the whole table every second and publishes the row a case here
// expects to see dead.
const suiteDatabaseURL = "postgres://junglegaming:junglegaming@localhost:5432/junglegaming_test?sslmode=disable"

func databaseURL() string {
	if value := os.Getenv("DATABASE_URL"); value != "" {
		return value
	}
	return suiteDatabaseURL
}

func newID() string {
	return uuid.NewV7().String()
}

func text(value string) *string {
	return &value
}

// waitEntered is the instant the rows of a wait are recorded at. The cases about
// the wait read instants and not a range, so the entry is fixed rather than now.
var waitEntered = time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)

func instant(value time.Time) *time.Time {
	return &value
}
