package walletapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/listledger"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

const (
	entryText       = "55555555-5555-4555-8555-555555555555"
	transactionText = "33333333-3333-4333-8333-333333333333"
	issuedCursor    = "MTExMTExMTEtMTExMS00MTExLTgxMTEtMTExMTExMTExMTExOjI6NTU1NTU1NTUtNTU1NS00NTU1LTg1NTUtNTU1NTU1NTU1NTU1"
)

func TestListLedger_answersThePageWithMoneyAsStringsAndTheCursor(t *testing.T) {
	t.Parallel()
	page := listledger.Page{Entries: []storage.EntryView{entryViewOf(t)}, NextCursor: issuedCursor}
	recorder := serve(ListLedger(&lister{page: page}, quietReporter()), ledgerRequestOf(walletText, "limit=1"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := decodeLedger(t, recorder)
	if body.WalletID != walletText || body.NextCursor != issuedCursor {
		t.Fatalf("page = %s with cursor %q, want %s with %q", body.WalletID, body.NextCursor, walletText, issuedCursor)
	}
	if len(body.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(body.Entries))
	}
	assertEntryBody(t, body.Entries[0])
}

func assertEntryBody(t *testing.T, entry externalEntry) {
	t.Helper()
	if entry.ID != entryText || entry.TransactionID != transactionText {
		t.Fatalf("identities = %s of %s, want %s of %s", entry.ID, entry.TransactionID, entryText, transactionText)
	}
	if entry.Direction != "DEBIT" || entry.Sequence != 2 {
		t.Fatalf("entry = %s at %d, want DEBIT at 2", entry.Direction, entry.Sequence)
	}
	assertEntryMoney(t, entry)
}

// assertEntryMoney checks the money of the entry as decimal strings, and the
// instant in UTC even though the view carried another zone.
func assertEntryMoney(t *testing.T, entry externalEntry) {
	t.Helper()
	if entry.Amount.Amount != "25.00" || entry.BalanceBefore.Amount != "1000.00" || entry.BalanceAfter.Amount != "975.00" {
		t.Fatalf("money = %+v from %+v to %+v, want 25.00 from 1000.00 to 975.00", entry.Amount, entry.BalanceBefore, entry.BalanceAfter)
	}
	if entry.CreatedAt != "2026-09-24T12:00:00Z" {
		t.Fatalf("createdAt = %s, want the instant in UTC", entry.CreatedAt)
	}
}

// An empty ledger is an empty array on the wire and no cursor: null would read
// as a page the client cannot walk, and a cursor would say there is more.
func TestListLedger_answersAnEmptyListAndNoCursorForAWalletWithoutMovements(t *testing.T) {
	t.Parallel()
	recorder := serve(ListLedger(&lister{}, quietReporter()), ledgerRequestOf(walletText, ""))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status of the empty ledger = %d, want 200", recorder.Code)
	}
	raw := recorder.Body.String()
	if !strings.Contains(raw, `"entries":[]`) {
		t.Fatalf("body = %s, want an empty entries array", raw)
	}
	if strings.Contains(raw, "nextCursor") {
		t.Fatalf("body = %s, want no cursor on the last page", raw)
	}
}

func TestListLedger_handsTheQueryOfTheURLToTheUseCase(t *testing.T) {
	t.Parallel()
	asked := &lister{}
	serve(ListLedger(asked, quietReporter()), ledgerRequestOf(walletText, "limit=2&cursor="+issuedCursor))
	if asked.query.WalletID != walletOf(t) || asked.query.Limit != 2 || asked.query.Cursor != issuedCursor {
		t.Fatalf("query = %+v, want the wallet of the URL with limit 2 and the cursor", asked.query)
	}
}

func TestListLedger_refusesEveryLimitOutsideTheContract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		limit   string
		refused error
		calls   int
	}{
		{limit: "0", refused: nil, calls: 0},
		{limit: "abc", refused: nil, calls: 0},
		{limit: "201", refused: listledger.ErrInvalidLimit, calls: 1},
	}
	for _, tc := range cases {
		t.Run("limit "+tc.limit+" is refused", func(t *testing.T) {
			asked := &lister{err: tc.refused}
			recorder := serve(ListLedger(asked, quietReporter()), ledgerRequestOf(walletText, "limit="+tc.limit))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status of the limit %s = %d, want 400", tc.limit, recorder.Code)
			}
			if body := decodeProblem(t, recorder); body.Detail != "limit is not valid" {
				t.Fatalf("detail of the limit %s = %q, want limit named", tc.limit, body.Detail)
			}
			if asked.calls != tc.calls {
				t.Fatalf("use case calls = %d, want %d", asked.calls, tc.calls)
			}
		})
	}
}

// The refusal names the field and never the token: what the cursor carries does
// not travel back in the error body.
func TestListLedger_refusesTheCursorWithoutEchoingIt(t *testing.T) {
	t.Parallel()
	recorder := serve(ListLedger(&lister{err: listledger.ErrInvalidCursor}, quietReporter()), ledgerRequestOf(walletText, "cursor=bm90LW91cnM"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status of the refused cursor = %d, want 400", recorder.Code)
	}
	body := decodeProblem(t, recorder)
	if body.Detail != "cursor is not valid" {
		t.Fatalf("detail of the cursor = %q, want cursor named", body.Detail)
	}
	if strings.Contains(recorder.Body.String(), "bm90LW91cnM") {
		t.Fatalf("body = %s, want it without the cursor", recorder.Body.String())
	}
	if body.FailureCode != "" {
		t.Fatalf("failureCode = %s, want empty for invalid input", body.FailureCode)
	}
}

func TestListLedger_answers404ForAWalletThatDoesNotExist(t *testing.T) {
	t.Parallel()
	recorder := serve(ListLedger(&lister{err: storage.ErrWalletNotFound}, quietReporter()), ledgerRequestOf(walletText, ""))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	if recorder.Header().Get("Content-Type") != problem.MediaType {
		t.Fatalf("content type of the absence = %s, want %s", recorder.Header().Get("Content-Type"), problem.MediaType)
	}
}

func TestListLedger_refusesAnIdentityOutOfFormatWithoutAskingTheUseCase(t *testing.T) {
	t.Parallel()
	asked := &lister{}
	recorder := serve(ListLedger(asked, quietReporter()), ledgerRequestOf("not-a-uuid", ""))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status of an identity out of format = %d, want 400", recorder.Code)
	}
	if asked.calls != 0 {
		t.Fatalf("use case calls = %d, want 0", asked.calls)
	}
}

// An empty ledger has to be an empty array and never null, and that is decided
// here, before the encoder sees a nil slice.
func TestEntriesOf_answersAnEmptyListAndNeverNil(t *testing.T) {
	t.Parallel()
	answered := entriesOf(nil)
	if answered == nil || len(answered) != 0 {
		t.Fatalf("entriesOf(nil) = %v, want an empty list that is not nil", answered)
	}
}

func TestEntriesOf_writesEachEntryWithTheInstantInUTC(t *testing.T) {
	t.Parallel()
	answered := entriesOf([]storage.EntryView{entryViewOf(t)})
	if len(answered) != 1 {
		t.Fatalf("entries answered = %d, want 1", len(answered))
	}
	if answered[0].Direction != "DEBIT" || answered[0].Sequence != 2 || answered[0].ID != entryText {
		t.Fatalf("entry = %+v, want DEBIT at 2 with the identity of the view", answered[0])
	}
	if answered[0].CreatedAt.Location() != time.UTC {
		t.Fatalf("createdAt zone = %s, want UTC", answered[0].CreatedAt.Location())
	}
}

type lister struct {
	page  listledger.Page
	err   error
	calls int
	query listledger.Query
}

func (l *lister) Page(_ context.Context, query listledger.Query) (listledger.Page, error) {
	l.calls++
	l.query = query
	if l.err != nil {
		return listledger.Page{}, l.err
	}
	return l.page, nil
}

type externalEntry struct {
	ID            string        `json:"id"`
	TransactionID string        `json:"transactionId"`
	Direction     string        `json:"direction"`
	Amount        externalMoney `json:"amount"`
	BalanceBefore externalMoney `json:"balanceBefore"`
	BalanceAfter  externalMoney `json:"balanceAfter"`
	Sequence      int64         `json:"sequenceNumber"`
	CreatedAt     string        `json:"createdAt"`
}

type externalLedger struct {
	WalletID   string          `json:"walletId"`
	Entries    []externalEntry `json:"entries"`
	NextCursor string          `json:"nextCursor"`
}

func ledgerRequestOf(walletID, rawQuery string) *http.Request {
	target := "/wallets/" + walletID + "/ledger"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	request.SetPathValue("walletId", walletID)
	return request
}

func decodeLedger(t *testing.T, recorder *httptest.ResponseRecorder) externalLedger {
	t.Helper()
	var body externalLedger
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal ledger = %v, want nil", err)
	}
	return body
}

func entryViewOf(t *testing.T) storage.EntryView {
	t.Helper()
	entryID, err := identity.ParseLedgerEntryID(entryText)
	if err != nil {
		t.Fatalf("ParseLedgerEntryID = %v, want nil", err)
	}
	transactionID, err := identity.ParseTransactionID(transactionText)
	if err != nil {
		t.Fatalf("ParseTransactionID = %v, want nil", err)
	}
	return storage.EntryView{
		ID:            entryID,
		TransactionID: transactionID,
		Direction:     ledger.Debit,
		Amount:        moneyOf(t, "25.00"),
		BalanceBefore: moneyOf(t, "1000.00"),
		BalanceAfter:  moneyOf(t, "975.00"),
		Sequence:      2,
		CreatedAt:     time.Date(2026, time.September, 24, 9, 0, 0, 0, time.FixedZone("BRT", -3*60*60)),
	}
}

func moneyOf(t *testing.T, amount string) money.Money {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	return parsed
}
