package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

const rowEntry = "55555555-5555-4555-8555-555555555555"

func TestView_takesTheEntryRowBackIntoDomainTypes(t *testing.T) {
	t.Parallel()
	view, err := entryRowOf().view()
	if err != nil {
		t.Fatalf("view of the entry row = %v, want nil", err)
	}
	assertEntryMoney(t, view)
	assertEntryPlacement(t, view)
}

func assertEntryMoney(t *testing.T, view storage.EntryView) {
	t.Helper()
	if view.Direction != ledger.Debit {
		t.Fatalf("direction = %s, want DEBIT", view.Direction)
	}
	if view.Amount.Amount() != "25.00" || view.Amount.Currency().Code() != "BRL" {
		t.Fatalf("amount = %s, want 25.00 BRL", view.Amount)
	}
	if view.BalanceBefore.Amount() != "1000.00" || view.BalanceAfter.Amount() != "975.00" {
		t.Fatalf("balances = %s and %s, want 1000.00 and 975.00", view.BalanceBefore.Amount(), view.BalanceAfter.Amount())
	}
}

func assertEntryPlacement(t *testing.T, view storage.EntryView) {
	t.Helper()
	if view.Sequence != 2 {
		t.Fatalf("sequence = %d, want 2", view.Sequence)
	}
	if view.ID.String() != rowEntry || view.TransactionID.String() != rowTransaction {
		t.Fatalf("identities = %s and %s, want the entry and the transaction of the row", view.ID, view.TransactionID)
	}
}

func TestView_refusesAnEntryRowTheDomainCannotAccept(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mutil func(*entryRow)
	}{
		{name: "an entry out of format is refused", mutil: func(r *entryRow) { r.id = "not-a-uuid" }},
		{name: "a transaction out of format is refused", mutil: func(r *entryRow) { r.transactionID = "not-a-uuid" }},
		{name: "a direction outside the two tokens is refused", mutil: func(r *entryRow) { r.direction = "TRANSFER" }},
		{name: "a currency outside ISO 4217 is refused", mutil: func(r *entryRow) { r.currency = "BRLL" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := entryRowOf()
			tc.mutil(&row)
			_, err := row.view()
			if err == nil {
				t.Fatalf("view of the entry row = nil, want an error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), "read ledger entry row") {
				t.Fatalf("refusal of %s = %q, want the operation named in the chain", tc.name, err.Error())
			}
		})
	}
}

func TestLedger_refusesWhileTheSharedPoolIsClosed(t *testing.T) {
	t.Parallel()
	reads := NewReads(NewPool(config.Config{DatabaseURL: unreachable}))
	page, err := reads.Ledger(context.Background(), walletIdentity(t), storage.EntryPosition{}, 50)
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Ledger = %v, want %v", err, ErrPoolClosed)
	}
	if page != nil {
		t.Fatalf("page beside the closed pool = %v, want none", page)
	}
}

// The wallet is asked for first, so a wallet that does not exist answers its
// absence before any page is read, and answers it as the absence of the wallet
// rather than as a ledger with nothing in it.
func TestEntriesOf_answersTheAbsenceOfTheWalletBeforeReadingThePage(t *testing.T) {
	t.Parallel()
	source := &stubQuerier{row: stubRow{err: pgx.ErrNoRows}}
	_, err := entriesOf(context.Background(), source, walletIdentity(t), storage.EntryPosition{}, 50)
	if !errors.Is(err, storage.ErrWalletNotFound) {
		t.Fatalf("entriesOf over an absent wallet = %v, want %v", err, storage.ErrWalletNotFound)
	}
	if source.queries != 0 {
		t.Fatalf("pages read for an absent wallet = %d, want 0", source.queries)
	}
}

// The page is asked with the position and the limit as they were given: the
// keyset predicate and the LIMIT are what make the page continue from the cursor
// and stop at the size the use case asked for.
func TestEntriesOf_asksThePageAfterThePositionUpToTheLimit(t *testing.T) {
	t.Parallel()
	source := &stubQuerier{row: stubRow{values: []any{rowWallet}}, rows: &entryStubRows{rows: [][]any{entryValues()}}}
	after := storage.EntryPosition{Sequence: 2, EntryID: entryIdentity(t)}
	page, err := entriesOf(context.Background(), source, walletIdentity(t), after, 3)
	if err != nil {
		t.Fatalf("entriesOf = %v, want nil", err)
	}
	if len(page) != 1 || page[0].Sequence != 2 {
		t.Fatalf("page = %+v, want the one row the scan handed over", page)
	}
	want := []any{rowWallet, int64(2), rowEntry, 3}
	if !reflect.DeepEqual(source.args, want) {
		t.Fatalf("arguments of the page = %v, want %v", source.args, want)
	}
}

// The failure of the driver on the page itself leaves as the failure of the read,
// with the operation named, and no wallet check can hide it.
func TestEntriesOf_answersTheFailureOfThePageQuery(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection reset by peer")
	source := &stubQuerier{row: stubRow{values: []any{rowWallet}}, queryErr: broken}
	_, err := entriesOf(context.Background(), source, walletIdentity(t), storage.EntryPosition{}, 50)
	if !errors.Is(err, broken) {
		t.Fatalf("entriesOf over a broken page query = %v, want %v", err, broken)
	}
	if !strings.Contains(err.Error(), "read ledger page") {
		t.Fatalf("failure of the page query = %q, want the operation named in the chain", err.Error())
	}
}

// A row that could not be scanned stops the page where it is: the rows before it
// are not answered as a shorter page.
func TestScanEntries_answersTheFailureOfOneRowAndNoPage(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection reset by peer")
	page, err := scanEntries(&entryStubRows{rows: [][]any{entryValues()}, scan: broken})
	if !errors.Is(err, broken) {
		t.Fatalf("scanEntries over a broken row = %v, want %v", err, broken)
	}
	if page != nil {
		t.Fatalf("page beside the broken row = %v, want none", page)
	}
}

// A walk that failed is not a short page: answering the rows read so far would
// hand the client a page that ends where the failure happened, with a cursor
// pointing there.
func TestScanEntries_answersTheFailureOfTheWalkAndNoPage(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection reset by peer")
	page, err := scanEntries(&entryStubRows{rows: [][]any{entryValues()}, walk: broken})
	if !errors.Is(err, broken) {
		t.Fatalf("scanEntries over a broken walk = %v, want %v", err, broken)
	}
	if page != nil {
		t.Fatalf("page beside the broken walk = %v, want none", page)
	}
}

func TestScanEntry_answersTheFailureOfTheDriver(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection reset by peer")
	_, err := scanEntry(&entryStubRows{scan: broken, at: -1})
	if !errors.Is(err, broken) {
		t.Fatalf("scanEntry over a broken row = %v, want %v", err, broken)
	}
	if !strings.Contains(err.Error(), "read ledger entry row") {
		t.Fatalf("failure of the driver = %q, want the operation named in the chain", err.Error())
	}
}

func TestSummary_refusesWhileTheSharedPoolIsClosed(t *testing.T) {
	t.Parallel()
	reads := NewReads(NewPool(config.Config{DatabaseURL: unreachable}))
	_, err := reads.Summary(context.Background(), walletIdentity(t))
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Summary = %v, want %v", err, ErrPoolClosed)
	}
}

// The wallet is part of the statement and not of a check afterwards: zero rows
// is the wallet not existing, and it leaves as that absence.
func TestSummaryOf_answersTheAbsenceOfTheWallet(t *testing.T) {
	t.Parallel()
	source := &stubQuerier{row: stubRow{err: pgx.ErrNoRows}}
	_, err := summaryOf(context.Background(), source, walletIdentity(t))
	if !errors.Is(err, storage.ErrWalletNotFound) {
		t.Fatalf("summaryOf over an absent wallet = %v, want %v", err, storage.ErrWalletNotFound)
	}
}

func TestSummaryOf_takesTheJoinedRowBackIntoTheSummary(t *testing.T) {
	t.Parallel()
	wallet := rowOf()
	source := &stubQuerier{row: stubRow{values: []any{
		wallet.id, wallet.playerID, wallet.currency, wallet.cents, wallet.version, wallet.createdAt, wallet.updatedAt,
		int64(97500), int64(3), int64(3), int64(2),
	}}}
	summary, err := summaryOf(context.Background(), source, walletIdentity(t))
	if err != nil {
		t.Fatalf("summaryOf = %v, want nil", err)
	}
	if summary.Wallet.Balance.Amount() != "1000.00" || summary.Wallet.Version != 3 {
		t.Fatalf("wallet of the summary = %s at version %d, want 1000.00 at version 3", summary.Wallet.Balance.Amount(), summary.Wallet.Version)
	}
	assertAggregates(t, summary)
}

func assertAggregates(t *testing.T, summary storage.LedgerSummary) {
	t.Helper()
	if summary.LedgerBalance != 97500 || summary.EntryCount != 3 || summary.LastSequence != 3 {
		t.Fatalf("ledger of the summary = %d cents over %d entries up to %d, want 97500 over 3 up to 3", summary.LedgerBalance, summary.EntryCount, summary.LastSequence)
	}
	if summary.FirstBreakSequence != 2 {
		t.Fatalf("first break = %d, want 2", summary.FirstBreakSequence)
	}
}

// The wallet half of the joined row is refused the same way a wallet row is: a
// summary over a wallet the domain cannot accept is not a summary.
func TestSummary_refusesAWalletHalfTheDomainCannotAccept(t *testing.T) {
	t.Parallel()
	row := summaryRow{wallet: rowOf()}
	row.wallet.currency = "BRLL"
	if _, err := row.summary(); err == nil {
		t.Fatalf("summary over a currency outside ISO 4217 = %v, want a refusal", err)
	}
}

func entryRowOf() entryRow {
	return entryRow{
		id:            rowEntry,
		transactionID: rowTransaction,
		direction:     "DEBIT",
		cents:         2500,
		currency:      "BRL",
		before:        100000,
		after:         97500,
		sequence:      2,
		createdAt:     time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC),
	}
}

// entryValues is the row of entryRowOf as the driver scans it, column by column.
func entryValues() []any {
	row := entryRowOf()
	return []any{row.id, row.transactionID, row.direction, row.cents, row.currency, row.before, row.after, row.sequence, row.createdAt}
}

func entryIdentity(t *testing.T) identity.LedgerEntryID {
	t.Helper()
	parsed, err := identity.ParseLedgerEntryID(rowEntry)
	if err != nil {
		t.Fatalf("ParseLedgerEntryID = %v, want nil", err)
	}
	return parsed
}

// stubQuerier is the pool of one case: the single row it answers, the result set
// it hands over, and what the page was asked with.
type stubQuerier struct {
	row      stubRow
	rows     pgx.Rows
	queryErr error
	queries  int
	args     []any
}

func (q *stubQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	return q.row
}

func (q *stubQuerier) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	q.queries++
	q.args = args
	if q.queryErr != nil {
		return nil, q.queryErr
	}
	return q.rows, nil
}

// stubRow is one row of the driver: the values it scans into place, or the
// failure it answers instead.
type stubRow struct {
	values []any
	err    error
}

func (r stubRow) Scan(into ...any) error {
	if r.err != nil {
		return r.err
	}
	assign(into, r.values)
	return nil
}

// entryStubRows is the result set of one page: the rows it hands over, and the
// failures the driver may answer while it is walked or scanned.
type entryStubRows struct {
	pgx.Rows
	rows [][]any
	at   int
	scan error
	walk error
}

func (r *entryStubRows) Next() bool {
	if r.at >= len(r.rows) {
		return false
	}
	r.at++
	return r.at <= len(r.rows)
}

func (r *entryStubRows) Scan(into ...any) error {
	if r.scan != nil {
		return r.scan
	}
	assign(into, r.rows[max(r.at-1, 0)])
	return nil
}

func (r *entryStubRows) Err() error {
	return r.walk
}

func (r *entryStubRows) Close() {}

// assign puts each value behind the pointer the scan was given, which is what
// the driver does column by column.
func assign(into, values []any) {
	for i, value := range values {
		reflect.ValueOf(into[i]).Elem().Set(reflect.ValueOf(value))
	}
}
