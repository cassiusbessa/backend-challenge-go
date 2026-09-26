package listledger

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

const (
	walletText      = "11111111-1111-4111-8111-111111111111"
	otherWalletText = "33333333-3333-4333-8333-333333333333"
	entryText       = "55555555-5555-4555-8555-555555555555"
)

func TestPage_asksForTheDefaultPlusOneWhenNoLimitIsGiven(t *testing.T) {
	t.Parallel()
	asked := &rows{}
	if _, err := New(asked).Page(context.Background(), Query{WalletID: walletOf(t, walletText)}); err != nil {
		t.Fatalf("Page without a limit = %v, want nil", err)
	}
	if asked.limit != DefaultLimit+1 {
		t.Fatalf("limit asked of the port = %d, want the default plus the row past the page, %d", asked.limit, DefaultLimit+1)
	}
	if asked.after != (storage.EntryPosition{}) {
		t.Fatalf("position asked of the port = %+v, want the one before the first entry", asked.after)
	}
}

func TestPage_refusesALimitOutsideTheRangeWithoutAskingTheLedger(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{-1, MaxLimit + 1} {
		t.Run("the limit "+strconv.Itoa(limit)+" is refused", func(t *testing.T) {
			asked := &rows{}
			_, err := New(asked).Page(context.Background(), Query{WalletID: walletOf(t, walletText), Limit: limit})
			if !errors.Is(err, ErrInvalidLimit) {
				t.Fatalf("Page with the limit %d = %v, want %v", limit, err, ErrInvalidLimit)
			}
			if asked.calls != 0 {
				t.Fatalf("ledger reads for a refused limit = %d, want 0", asked.calls)
			}
		})
	}
}

func TestPage_acceptsTheTwoEndsOfTheRange(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{1, MaxLimit} {
		t.Run("the limit "+strconv.Itoa(limit)+" is accepted", func(t *testing.T) {
			asked := &rows{}
			if _, err := New(asked).Page(context.Background(), Query{WalletID: walletOf(t, walletText), Limit: limit}); err != nil {
				t.Fatalf("Page with the limit %d = %v, want nil", limit, err)
			}
			if asked.limit != limit+1 {
				t.Fatalf("limit asked of the port = %d, want %d", asked.limit, limit+1)
			}
		})
	}
}

func TestPage_refusesTheCursorWithoutAskingTheLedger(t *testing.T) {
	t.Parallel()
	asked := &rows{}
	_, err := New(asked).Page(context.Background(), Query{WalletID: walletOf(t, walletText), Cursor: "not base64!"})
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("Page with a cursor the route did not issue = %v, want %v", err, ErrInvalidCursor)
	}
	if asked.calls != 0 {
		t.Fatalf("ledger reads for a refused cursor = %d, want 0", asked.calls)
	}
}

func TestPage_continuesFromTheCursorItIssued(t *testing.T) {
	t.Parallel()
	asked := &rows{entries: entriesOf(t, 1, 2, 3)}
	first, err := New(asked).Page(context.Background(), Query{WalletID: walletOf(t, walletText), Limit: 2})
	if err != nil {
		t.Fatalf("first page = %v, want nil", err)
	}
	if _, err := New(asked).Page(context.Background(), Query{WalletID: walletOf(t, walletText), Limit: 2, Cursor: first.NextCursor}); err != nil {
		t.Fatalf("second page = %v, want nil", err)
	}
	if asked.after.Sequence != 2 || asked.after.EntryID != first.Entries[1].ID {
		t.Fatalf("position asked for the second page = %+v, want the last entry of the first", asked.after)
	}
}

func TestPage_cutsTheRowPastThePageAndIssuesTheCursorOfTheLastOne(t *testing.T) {
	t.Parallel()
	page, err := New(&rows{entries: entriesOf(t, 1, 2, 3)}).Page(context.Background(), Query{WalletID: walletOf(t, walletText), Limit: 2})
	if err != nil {
		t.Fatalf("Page over three entries = %v, want nil", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("entries = %d, want the 2 of the limit", len(page.Entries))
	}
	assertCursorPointsAt(t, page.NextCursor, page.Entries[1])
}

// assertCursorPointsAt decodes the issued cursor and checks it names the entry
// the next page has to continue from.
func assertCursorPointsAt(t *testing.T, cursor string, last storage.EntryView) {
	t.Helper()
	if cursor == "" {
		t.Fatalf("next cursor = %q, want one for the row past the page", cursor)
	}
	position, err := decodeCursor(walletOf(t, walletText), cursor)
	if err != nil {
		t.Fatalf("decodeCursor of the issued cursor = %v, want nil", err)
	}
	if position.Sequence != last.Sequence || position.EntryID != last.ID {
		t.Fatalf("cursor points at %+v, want the last entry answered, %d", position, last.Sequence)
	}
}

func TestPage_answersTheLastPageWithoutACursor(t *testing.T) {
	t.Parallel()
	page, err := New(&rows{entries: entriesOf(t, 1, 2)}).Page(context.Background(), Query{WalletID: walletOf(t, walletText), Limit: 2})
	if err != nil {
		t.Fatalf("Page over two entries = %v, want nil", err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(page.Entries))
	}
	if page.NextCursor != "" {
		t.Fatalf("next cursor = %q, want none on the last page", page.NextCursor)
	}
}

func TestPage_answersAnEmptyPageForAWalletWithoutMovements(t *testing.T) {
	t.Parallel()
	page, err := New(&rows{}).Page(context.Background(), Query{WalletID: walletOf(t, walletText)})
	if err != nil {
		t.Fatalf("Page over no entries = %v, want nil", err)
	}
	if len(page.Entries) != 0 || page.NextCursor != "" {
		t.Fatalf("page = %+v, want no entry and no cursor", page)
	}
}

func TestPage_passesTheAbsenceThrough(t *testing.T) {
	t.Parallel()
	_, err := New(&rows{err: storage.ErrWalletNotFound}).Page(context.Background(), Query{WalletID: walletOf(t, walletText)})
	if !errors.Is(err, storage.ErrWalletNotFound) {
		t.Fatalf("Page = %v, want %v", err, storage.ErrWalletNotFound)
	}
}

// The range of a page, at its ends: zero is the absence of a limit and takes
// the default, one and the ceiling are accepted, and past either side is refused.
func TestLimitOf_appliesTheDefaultAndRefusesOutsideTheRange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		asked int
		want  int
		err   error
	}{
		{asked: 0, want: DefaultLimit},
		{asked: 1, want: 1},
		{asked: MaxLimit, want: MaxLimit},
		{asked: -1, err: ErrInvalidLimit},
		{asked: MaxLimit + 1, err: ErrInvalidLimit},
	}
	for _, tc := range cases {
		t.Run("the limit "+strconv.Itoa(tc.asked), func(t *testing.T) {
			got, err := limitOf(tc.asked)
			if !errors.Is(err, tc.err) {
				t.Fatalf("limitOf(%d) error = %v, want %v", tc.asked, err, tc.err)
			}
			if got != tc.want {
				t.Fatalf("limitOf(%d) = %d, want %d", tc.asked, got, tc.want)
			}
		})
	}
}

// The absence of a cursor is not a case of its own for the port: it is the zero
// position, the one before the first entry. A cursor present is whatever
// decodeCursor reads back from it.
func TestPositionOfQuery_answersTheZeroPositionWithoutACursorAndTheDecodedOneWithIt(t *testing.T) {
	t.Parallel()
	first, err := positionOfQuery(Query{WalletID: walletOf(t, walletText)})
	if err != nil {
		t.Fatalf("positionOfQuery without a cursor = %v, want nil", err)
	}
	if first != (storage.EntryPosition{}) {
		t.Fatalf("position without a cursor = %+v, want the one before the first entry", first)
	}
	issued := storage.EntryPosition{Sequence: 7, EntryID: entryOf(t, entryText)}
	next, err := positionOfQuery(Query{WalletID: walletOf(t, walletText), Cursor: encodeCursor(walletOf(t, walletText), issued)})
	if err != nil {
		t.Fatalf("positionOfQuery with the cursor it issued = %v, want nil", err)
	}
	if next != issued {
		t.Fatalf("position with a cursor = %+v, want the one the token names, %+v", next, issued)
	}
}

// The row past the page is the only thing that says there is a next one: with
// it the page is cut and a cursor is issued, without it the page is whole and
// carries none.
func TestPageOf_issuesACursorOnlyWhenTheRowPastThePageExisted(t *testing.T) {
	t.Parallel()
	cut := pageOf(walletOf(t, walletText), entriesOf(t, 1, 2, 3), 2)
	if len(cut.Entries) != 2 || cut.NextCursor == "" {
		t.Fatalf("pageOf over three entries with limit 2 = %d entries and cursor %q, want 2 and a cursor", len(cut.Entries), cut.NextCursor)
	}
	whole := pageOf(walletOf(t, walletText), entriesOf(t, 1, 2), 2)
	if len(whole.Entries) != 2 || whole.NextCursor != "" {
		t.Fatalf("pageOf over two entries with limit 2 = %d entries and cursor %q, want 2 and none", len(whole.Entries), whole.NextCursor)
	}
}

// rows is the read port in memory. Only the page belongs to this use case: the
// other reads are part of the same port and are never reached from here.
type rows struct {
	entries []storage.EntryView
	err     error
	calls   int
	after   storage.EntryPosition
	limit   int
}

func (r *rows) Ledger(_ context.Context, _ identity.WalletID, after storage.EntryPosition, limit int) ([]storage.EntryView, error) {
	r.calls++
	r.after = after
	r.limit = limit
	if r.err != nil {
		return nil, r.err
	}
	var page []storage.EntryView
	for _, entry := range r.entries {
		if entry.Sequence > after.Sequence && len(page) < limit {
			page = append(page, entry)
		}
	}
	return page, nil
}

func (r *rows) Wallet(context.Context, identity.WalletID) (storage.WalletView, error) {
	return storage.WalletView{}, storage.ErrWalletNotFound
}

func (r *rows) Summary(context.Context, identity.WalletID) (storage.LedgerSummary, error) {
	return storage.LedgerSummary{}, storage.ErrWalletNotFound
}

func (r *rows) Transaction(context.Context, identity.TransactionID, identity.ProviderID) (storage.TransactionView, error) {
	return storage.TransactionView{}, storage.ErrTransactionNotFound
}

func (r *rows) TransactionByKey(context.Context, identity.ProviderID, identity.IdempotencyKey) (wager.State, error) {
	return wager.State{}, storage.ErrTransactionNotFound
}

func (r *rows) DueWaits(context.Context, time.Time, int) ([]storage.WaitCandidate, error) {
	return nil, nil
}

func (r *rows) OldestWait(context.Context, time.Time) (time.Duration, error) {
	return 0, nil
}

func (r *rows) WalletIDsAfter(context.Context, identity.WalletID, int) ([]identity.WalletID, error) {
	return nil, nil
}

// entriesOf builds one credit per sequence, each with an identity of its own,
// so a cursor can be checked against the entry it points at.
func entriesOf(t *testing.T, sequences ...int64) []storage.EntryView {
	t.Helper()
	amount, err := money.Parse("25.00", "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	entries := make([]storage.EntryView, 0, len(sequences))
	for _, sequence := range sequences {
		entries = append(entries, storage.EntryView{
			ID:        entryOf(t, "5555555"+strconv.FormatInt(sequence, 10)+"-5555-4555-8555-555555555555"),
			Direction: ledger.Credit,
			Amount:    amount,
			Sequence:  sequence,
			CreatedAt: time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC),
		})
	}
	return entries
}

func walletOf(t *testing.T, text string) identity.WalletID {
	t.Helper()
	id, err := identity.ParseWalletID(text)
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	return id
}

func entryOf(t *testing.T, text string) identity.LedgerEntryID {
	t.Helper()
	id, err := identity.ParseLedgerEntryID(text)
	if err != nil {
		t.Fatalf("ParseLedgerEntryID = %v, want nil", err)
	}
	return id
}
