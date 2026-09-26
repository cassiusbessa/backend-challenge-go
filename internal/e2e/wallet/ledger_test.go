//go:build integration

package wallet

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

func TestListLedger_answersTheThreeMovementsInSequenceWithTheChainClosed(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	bet := wallet.bet(ctx, t, base, "25.00")
	win := wallet.win(ctx, t, base, "50.00")
	page, status := listLedger(ctx, t, base, internal, wallet.wallet.ID, "")
	if status != http.StatusOK {
		t.Fatalf("status of the three movements = %d, want 200", status)
	}
	if len(page.Entries) != 3 || page.NextCursor != "" {
		t.Fatalf("page = %d entries with cursor %q, want 3 and no cursor", len(page.Entries), page.NextCursor)
	}
	assertMovement(t, page.Entries[0], 1, "CREDIT", "1000.00", "0.00", "1000.00", openingTransaction(ctx, t, wallet.wallet.ID))
	assertMovement(t, page.Entries[1], 2, "DEBIT", "25.00", "1000.00", "975.00", bet.TransactionID)
	assertMovement(t, page.Entries[2], 3, "CREDIT", "50.00", "975.00", "1025.00", win.TransactionID)
}

// assertMovement checks one entry of the page: where it sits, what it moved and
// the transaction it names.
func assertMovement(t *testing.T, entry externalEntry, sequence int64, direction, amount, before, after, transaction string) {
	t.Helper()
	if entry.Sequence != sequence || entry.Direction != direction {
		t.Fatalf("entry = %s at %d, want %s at %d", entry.Direction, entry.Sequence, direction, sequence)
	}
	if entry.TransactionID != transaction {
		t.Fatalf("transaction of the entry at %d = %s, want %s", sequence, entry.TransactionID, transaction)
	}
	assertMoved(t, entry, amount, before, after)
}

func assertMoved(t *testing.T, entry externalEntry, amount, before, after string) {
	t.Helper()
	if entry.Amount.Amount != amount || entry.Amount.Currency != "BRL" {
		t.Fatalf("amount of the entry at %d = %+v, want %s BRL", entry.Sequence, entry.Amount, amount)
	}
	if entry.BalanceBefore.Amount != before || entry.BalanceAfter.Amount != after {
		t.Fatalf("balances of the entry at %d = %s to %s, want %s to %s", entry.Sequence, entry.BalanceBefore.Amount, entry.BalanceAfter.Amount, before, after)
	}
	if !strings.HasSuffix(entry.CreatedAt, "Z") {
		t.Fatalf("createdAt of the entry at %d = %s, want an instant in UTC", entry.Sequence, entry.CreatedAt)
	}
}

func TestListLedger_answersAnEmptyLedgerAndNoCursorForAWalletAtZero(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	opened, status := open(ctx, t, base, internal, body(suiteenv.NewID(), "0.00", "BRL"))
	if status != http.StatusCreated {
		t.Fatalf("opening at zero = %d, want 201", status)
	}
	page, status := listLedger(ctx, t, base, internal, opened.ID, "")
	if status != http.StatusOK {
		t.Fatalf("status of the empty ledger = %d, want 200", status)
	}
	if page.Entries == nil || len(page.Entries) != 0 {
		t.Fatalf("entries = %v, want an empty list", page.Entries)
	}
	if page.NextCursor != "" {
		t.Fatalf("cursor = %q, want none for an empty ledger", page.NextCursor)
	}
}

func TestListLedger_pagesThreeEntriesInTwoWithLimitTwo(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	wallet.bet(ctx, t, base, "25.00")
	wallet.win(ctx, t, base, "50.00")
	first, status := listLedger(ctx, t, base, internal, wallet.wallet.ID, "limit=2")
	assertPage(t, first, status, "1,2", true)
	second, status := listLedger(ctx, t, base, internal, wallet.wallet.ID, "limit=2&cursor="+first.NextCursor)
	assertPage(t, second, status, "3", false)
}

// assertPage checks the sequences a page carries and whether it says there is a
// next one.
func assertPage(t *testing.T, page externalLedger, status int, want string, continues bool) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("page = %d, want 200", status)
	}
	if got := sequences(page); got != want {
		t.Fatalf("page = %s, want %s", got, want)
	}
	if (page.NextCursor != "") != continues {
		t.Fatalf("cursor of the page %s = %q, want one present = %t", want, page.NextCursor, continues)
	}
}

// A movement settled between two pages appears on the next one, after the
// entries that already existed: the cursor is a position in the ledger and not
// an offset, so nothing is repeated and nothing is skipped.
func TestListLedger_showsAMovementSettledBetweenTwoPagesOnTheNextOne(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	wallet.bet(ctx, t, base, "25.00")
	wallet.win(ctx, t, base, "50.00")
	first, _ := listLedger(ctx, t, base, internal, wallet.wallet.ID, "limit=2")
	if sequences(first) != "1,2" {
		t.Fatalf("first page = %s, want 1,2", sequences(first))
	}
	late := wallet.bet(ctx, t, base, "10.00")
	second, status := listLedger(ctx, t, base, internal, wallet.wallet.ID, "limit=2&cursor="+first.NextCursor)
	assertPage(t, second, status, "3,4", false)
	if second.Entries[1].TransactionID != late.TransactionID {
		t.Fatalf("last entry names %s, want the bet settled between the pages, %s", second.Entries[1].TransactionID, late.TransactionID)
	}
}

func TestListLedger_answers404ForAWalletThatDoesNotExist(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	asked := suiteenv.NewID()
	status, mediaType, _ := refusalOf(ctx, t, ledgerURL(base, asked, ""), internal)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if mediaType != problem.MediaType {
		t.Fatalf("content type of the absence = %s, want %s", mediaType, problem.MediaType)
	}
	if got := count(ctx, t, "SELECT count(*) FROM wallets WHERE id = $1", asked); got != 0 {
		t.Fatalf("wallets for the identity listed = %d, want 0: a read writes nothing", got)
	}
}

func TestListLedger_refusesEveryLimitOutsideTheContractNamingTheField(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	for _, limit := range []string{"0", "201", "abc"} {
		t.Run("limit "+limit+" is refused", func(t *testing.T) {
			status, mediaType, raw := refusalOf(ctx, t, ledgerURL(base, wallet.wallet.ID, "limit="+limit), internal)
			assertInvalidField(t, status, mediaType, raw, "limit")
		})
	}
}

// assertInvalidField checks a refusal of the contract: 400 in problem details,
// the field named in the detail, and no entry in the body.
func assertInvalidField(t *testing.T, status int, mediaType string, raw []byte, field string) {
	t.Helper()
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	if mediaType != problem.MediaType {
		t.Fatalf("content type of the refusal = %s, want %s", mediaType, problem.MediaType)
	}
	if refusal := problemOf(t, raw); refusal.Detail != field+" is not valid" {
		t.Fatalf("detail = %q, want the field %s named", refusal.Detail, field)
	}
	if bytes.Contains(raw, []byte("entries")) {
		t.Fatalf("body = %s, want no entry in a refusal", raw)
	}
}

func TestListLedger_refusesACursorTheRouteDidNotIssue(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	status, mediaType, raw := refusalOf(ctx, t, ledgerURL(base, wallet.wallet.ID, "cursor=bm90LW91cnM"), internal)
	assertInvalidField(t, status, mediaType, raw, "cursor")
	if bytes.Contains(raw, []byte("bm90LW91cnM")) {
		t.Fatalf("body = %s, want it without the cursor", raw)
	}
}

// The cursor of wallet A presented on wallet B is refused with the very body of
// a malformed one: the refusal says nothing about A, and B answers no entry.
func TestListLedger_refusesTheCursorOfAnotherWalletTheSameWayAsAMalformedOne(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	first := openFunded(ctx, t, base, internal)
	first.bet(ctx, t, base, "25.00")
	first.win(ctx, t, base, "10.00")
	issued, _ := listLedger(ctx, t, base, internal, first.wallet.ID, "limit=2")
	if issued.NextCursor == "" {
		t.Fatalf("cursor of wallet A = %q, want one to present on wallet B", issued.NextCursor)
	}
	second := openFunded(ctx, t, base, internal)
	status, _, otherWallet := refusalOf(ctx, t, ledgerURL(base, second.wallet.ID, "cursor="+issued.NextCursor), internal)
	if status != http.StatusBadRequest {
		t.Fatalf("status of the cursor of another wallet = %d, want 400", status)
	}
	_, _, malformed := refusalOf(ctx, t, ledgerURL(base, second.wallet.ID, "cursor=bm90LW91cnM"), internal)
	if !bytes.Equal(otherWallet, malformed) {
		t.Fatalf("refusal of the cursor of another wallet = %s, want the same body as a malformed one, %s", otherWallet, malformed)
	}
	if bytes.Contains(otherWallet, []byte("entries")) {
		t.Fatalf("body = %s, want no entry of wallet B in it", otherWallet)
	}
}

func TestReadRoutes_refuseAProviderByPermissionWithoutALedgerOrABalance(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	for name, rawURL := range map[string]string{
		"the ledger":         ledgerURL(base, wallet.wallet.ID, ""),
		"the reconciliation": reconciliationURL(base, wallet.wallet.ID),
	} {
		t.Run(name+" refuses the provider", func(t *testing.T) {
			status, mediaType, raw := refusalOf(ctx, t, rawURL, wallet.provider)
			assertRefusedByPermission(t, status, mediaType, raw)
		})
	}
}

// assertRefusedByPermission checks the 403 of a provider on a read: problem
// details, and nothing of the ledger or the balance in the body.
func assertRefusedByPermission(t *testing.T, status int, mediaType string, raw []byte) {
	t.Helper()
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: a provider reads neither the ledger nor the reconciliation", status)
	}
	if mediaType != problem.MediaType {
		t.Fatalf("content type of the permission refusal = %s, want %s", mediaType, problem.MediaType)
	}
	for _, banned := range []string{"entries", "1000.00", "Balance", "consistent", "nextCursor"} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Fatalf("body = %s, want it without %q", raw, banned)
		}
	}
}

func TestReadRoutes_refuseAnAbsentCredential(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	wallet := openFunded(ctx, t, base, internal)
	for name, rawURL := range map[string]string{
		"the ledger":         ledgerURL(base, wallet.wallet.ID, ""),
		"the reconciliation": reconciliationURL(base, wallet.wallet.ID),
	} {
		t.Run(name+" refuses the absent credential", func(t *testing.T) {
			status, _, _ := refusalOf(ctx, t, rawURL, "")
			if status != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", status)
			}
		})
	}
}

// sequences writes the sequences of a page as one token, so a case states the
// whole page in one comparison and the failure prints what came back.
func sequences(page externalLedger) string {
	var written strings.Builder
	for i, entry := range page.Entries {
		if i > 0 {
			written.WriteString(",")
		}
		written.WriteString(strconv.FormatInt(entry.Sequence, 10))
	}
	return written.String()
}
