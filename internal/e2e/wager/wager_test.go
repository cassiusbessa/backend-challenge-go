//go:build integration

// The journey of the wager routes against a real PostgreSQL and a real IdP: what
// one commit writes, what a second arrival answers, how two bets over the same
// wallet serialize, and what a provider is allowed to see.
//
// Every case opens its own wallet, so two runs of the suite never collide on the
// unique index of one wallet per player and currency.
package wager

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

func TestSubmit_settlesTheRoundAndAnswersEachTransaction(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	placed := assertBetDebits(ctx, t, at, wallet)
	closed := assertLossRecordsTheTransactionAlone(ctx, t, at, wallet)
	assertWinCredits(ctx, t, at, wallet)
	assertRead(ctx, t, at, placed, "PROCESSED", "")
	assertRead(ctx, t, at, closed, "PROCESSED", "")
}

// The bet debits the wallet and writes its entry in the same commit, and the answer
// points at the resource it created.
func assertBetDebits(ctx context.Context, t *testing.T, at suite, wallet owner) string {
	t.Helper()
	placed := bet(ctx, t, at, wallet, "25.00")
	if placed.status != http.StatusCreated {
		t.Fatalf("the bet that debits = %d, want 201: %s", placed.status, placed.body)
	}
	settled := placed.transaction(t)
	if settled.Status != "PROCESSED" || settled.Balance.Amount != "975.00" {
		t.Fatalf("bet = %s with %s observed, want PROCESSED with 975.00", settled.Status, settled.Balance.Amount)
	}
	if got := placed.header.Get("Location"); got != wagerRoute+"/"+settled.TransactionID {
		t.Fatalf("location = %s, want %s", got, wagerRoute+"/"+settled.TransactionID)
	}
	assertStatementNames(t, placed)
	assertWallet(ctx, t, wallet.id, 97500, 2)
	assertEntries(ctx, t, wallet.id, 2)
	return settled.TransactionID
}

// assertStatementNames reads the body as bytes, the way a client following the
// challenge statement reads it: that document's names, the former ones gone, and
// the replay marker written even on the first completion.
func assertStatementNames(t *testing.T, placed answer) {
	t.Helper()
	for _, want := range []string{`"transactionId":`, `"balance":`, `"idempotentReplay":false`} {
		if !bytes.Contains(placed.body, []byte(want)) {
			t.Fatalf("body of the first completion = %s, want it carrying %s", placed.body, want)
		}
	}
	for _, former := range []string{`"id":`, `"observedBalance":`} {
		if bytes.Contains(placed.body, []byte(former)) {
			t.Fatalf("body of the first completion = %s, want no former name %s", placed.body, former)
		}
	}
}

// The loss closes the round without touching the wallet: no entry, no version
// change, and the amount is zero.
func assertLossRecordsTheTransactionAlone(ctx context.Context, t *testing.T, at suite, wallet owner) string {
	t.Helper()
	lost := submitBody(ctx, t, at, wallet.loss(nil))
	if lost.status != http.StatusCreated {
		t.Fatalf("loss = %d, want 201: %s", lost.status, lost.body)
	}
	closed := lost.transaction(t)
	if closed.Status != "PROCESSED" || closed.Money.Amount != "0.00" {
		t.Fatalf("loss = %s of %s, want PROCESSED of 0.00", closed.Status, closed.Money.Amount)
	}
	assertWallet(ctx, t, wallet.id, 97500, 2)
	assertEntries(ctx, t, wallet.id, 2)
	return closed.TransactionID
}

// A win citing no operation credits right away.
func assertWinCredits(ctx context.Context, t *testing.T, at suite, wallet owner) {
	t.Helper()
	credited := submitBody(ctx, t, at, wallet.win("50.00", nil))
	if credited.status != http.StatusCreated {
		t.Fatalf("win = %d, want 201: %s", credited.status, credited.body)
	}
	assertWallet(ctx, t, wallet.id, 102500, 3)
	assertEntries(ctx, t, wallet.id, 3)
}

// The second arrival of the same operation answers the outcome recorded by the
// first, with the balance observed back then, and moves nothing.
func TestSubmit_replaysTheSameKeyAndTheSameBody(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	payload := wallet.bet("25.00", nil)
	key := "key-" + suiteenv.NewID()

	first := submit(ctx, t, at, at.provider, key, payload)
	if first.status != http.StatusCreated {
		t.Fatalf("first arrival of the same body = %d, want 201: %s", first.status, first.body)
	}
	again := submit(ctx, t, at, at.provider, key, payload)
	if again.status != http.StatusOK {
		t.Fatalf("second arrival = %d, want 200: %s", again.status, again.body)
	}
	replayed := again.transaction(t)
	if !replayed.IdempotentReplay {
		t.Fatalf("idempotentReplay = %t, want true on the second arrival", replayed.IdempotentReplay)
	}
	if replayed.TransactionID != first.transaction(t).TransactionID {
		t.Fatalf("replay answered %s, want the recorded %s", replayed.TransactionID, first.transaction(t).TransactionID)
	}
	if replayed.Balance.Amount != "975.00" {
		t.Fatalf("observed balance = %s, want the 975.00 of the original completion", replayed.Balance.Amount)
	}
	assertWallet(ctx, t, wallet.id, 97500, 2)
	assertEntries(ctx, t, wallet.id, 2)
	assertRowsForKey(ctx, t, key, 1)
}

// A rejection by rule is durable: the row stays REJECTED with its token, the wallet
// does not move, and reading it afterwards succeeds.
func TestSubmit_keepsTheRejectedRowAndMovesNothing(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	key := "key-" + suiteenv.NewID()
	payload := wallet.bet("2000.00", nil)
	assertRefusedByBalance(ctx, t, at, key, payload)
	assertWallet(ctx, t, wallet.id, 100000, 1)
	assertEntries(ctx, t, wallet.id, 1)
	assertRead(ctx, t, at, rejectedID(ctx, t, key), "REJECTED", "INSUFFICIENT_FUNDS")
	assertRefusalReplayed(ctx, t, at, key, payload)
	assertRowsForKey(ctx, t, key, 1)
}

func assertRefusedByBalance(ctx context.Context, t *testing.T, at suite, key, payload string) {
	t.Helper()
	refused := submit(ctx, t, at, at.provider, key, payload)
	if refused.status != http.StatusUnprocessableEntity {
		t.Fatalf("bet past the balance = %d, want 422: %s", refused.status, refused.body)
	}
	if got := refused.header.Get("Content-Type"); got != problem.MediaType {
		t.Fatalf("content type = %s, want %s", got, problem.MediaType)
	}
	details := refused.refusal(t)
	if details.FailureCode != "INSUFFICIENT_FUNDS" || details.IdempotentReplay {
		t.Fatalf("refusal = %+v, want INSUFFICIENT_FUNDS without the marker", details)
	}
}

// The recorded refusal is answered again with the same token, and the marker says it
// is not a new decision.
func assertRefusalReplayed(ctx context.Context, t *testing.T, at suite, key, payload string) {
	t.Helper()
	replayed := submit(ctx, t, at, at.provider, key, payload)
	if replayed.status != http.StatusUnprocessableEntity {
		t.Fatalf("replay of the refusal = %d, want 422", replayed.status)
	}
	marked := replayed.refusal(t)
	if marked.FailureCode != "INSUFFICIENT_FUNDS" || !marked.IdempotentReplay {
		t.Fatalf("replayed refusal = %+v, want the same token with the marker", marked)
	}
}

// A wallet that does not exist writes nothing at all: the row of the operation
// could not exist, because its foreign key would name a wallet that is not there.
func TestSubmit_refusesAWalletThatDoesNotExistWithoutARow(t *testing.T) {
	ctx, at := start(t)
	absent := owner{id: suiteenv.NewID(), player: suiteenv.NewID()}
	key := "key-" + suiteenv.NewID()
	refused := submit(ctx, t, at, at.provider, key, absent.bet("25.00", nil))
	if refused.status != http.StatusUnprocessableEntity {
		t.Fatalf("bet on an absent wallet = %d, want 422: %s", refused.status, refused.body)
	}
	if got := refused.refusal(t).FailureCode; got != "WALLET_NOT_FOUND" {
		t.Fatalf("failureCode = %s, want WALLET_NOT_FOUND", got)
	}
	assertRowsForKey(ctx, t, key, 0)
}

// What is still refused with no row is the operation whose row would violate an
// invariant of the table: the internal OPENING arriving from a provider, and the
// reversal that cites nothing.
func TestSubmit_refusesTheOperationsThatCanCarryNoRow(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	cases := []struct {
		name    string
		payload string
		code    string
	}{
		{name: "an opening is refused", payload: wallet.of("OPENING", "25.00", nil), code: "OPENING_NOT_ALLOWED"},
		{name: "a refund citing nothing is refused", payload: wallet.refund("25.00", nil), code: "REFERENCE_REQUIRED"},
		{name: "a rollback citing nothing is refused", payload: wallet.rollback("25.00", nil), code: "REFERENCE_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "key-" + suiteenv.NewID()
			refused := submit(ctx, t, at, at.provider, key, tc.payload)
			if refused.status != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", refused.status, refused.body)
			}
			if got := refused.refusal(t).FailureCode; got != tc.code {
				t.Fatalf("failureCode = %s, want %s", got, tc.code)
			}
			assertRowsForKey(ctx, t, key, 0)
		})
	}
	assertWallet(ctx, t, wallet.id, 100000, 1)
}

// The cited operation spelled wrong is invalid input, not a rule refusing
// anything: the answer carries no token and no row is written.
func TestSubmit_refusesACitedOperationOutOfFormatWithoutARow(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	key := "key-" + suiteenv.NewID()
	refused := submit(ctx, t, at, at.provider, key, wallet.win("25.00", map[string]any{"referenceExternalTransactionId": "  "}))
	if refused.status != http.StatusBadRequest {
		t.Fatalf("a cited operation out of format = %d, want 400: %s", refused.status, refused.body)
	}
	if got := refused.refusal(t).FailureCode; got != "" {
		t.Fatalf("failureCode = %s, want empty for invalid input", got)
	}
	assertRowsForKey(ctx, t, key, 0)
	assertWallet(ctx, t, wallet.id, 100000, 1)
}

func TestSubmit_refusesASubmissionWithoutTheKeyWithoutARow(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	refused := submit(ctx, t, at, at.provider, "", wallet.bet("25.00", nil))
	if refused.status != http.StatusBadRequest {
		t.Fatalf("submission without the key = %d, want 400: %s", refused.status, refused.body)
	}
	if got := refused.refusal(t).FailureCode; got != "" {
		t.Fatalf("failureCode = %s, want empty for invalid input", got)
	}
	assertWallet(ctx, t, wallet.id, 100000, 1)
	assertEntries(ctx, t, wallet.id, 1)
}

// Two bets over the same wallet serialize on the lock. The one that does not fit
// the balance already committed is rejected, and only one entry is written.
func TestSubmit_serializesTwoBetsAndRejectsTheOneThatDoesNotFit(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	answers := together(ctx, t, at, wallet.bet("700.00", nil), wallet.bet("700.00", nil))
	created, refused := split(t, answers)
	if created != 1 || refused != 1 {
		t.Fatalf("created = %d and refused = %d, want one of each", created, refused)
	}
	assertOneRefusalToken(t, answers, "INSUFFICIENT_FUNDS")
	assertWallet(ctx, t, wallet.id, 30000, 2)
	assertEntries(ctx, t, wallet.id, 2)
}

// When both bets fit, the two entries chain: the balance after one is the balance
// before the other, and the version rose by two.
func TestSubmit_chainsTheEntriesOfTwoBetsThatBothFit(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	answers := together(ctx, t, at, wallet.bet("400.00", nil), wallet.bet("400.00", nil))
	created, refused := split(t, answers)
	if created != 2 || refused != 0 {
		t.Fatalf("created = %d and refused = %d, want both accepted", created, refused)
	}
	assertWallet(ctx, t, wallet.id, 20000, 3)
	assertEntries(ctx, t, wallet.id, 3)
	assertChained(ctx, t, wallet.id)
}

func TestSubmit_refusesTheSameKeyWithAnotherBody(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	key := "key-" + suiteenv.NewID()
	external := map[string]any{"externalTransactionId": "external-" + suiteenv.NewID()}
	if first := submit(ctx, t, at, at.provider, key, wallet.bet("25.00", external)); first.status != http.StatusCreated {
		t.Fatalf("first arrival before another body = %d, want 201: %s", first.status, first.body)
	}
	refused := submit(ctx, t, at, at.provider, key, wallet.bet("30.00", external))
	if refused.status != http.StatusUnprocessableEntity {
		t.Fatalf("another body under the same key = %d, want 422: %s", refused.status, refused.body)
	}
	if got := refused.refusal(t).FailureCode; got != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("failureCode = %s, want IDEMPOTENCY_CONFLICT", got)
	}
	assertRowsForKey(ctx, t, key, 1)
	assertWallet(ctx, t, wallet.id, 97500, 2)
}

func TestSubmit_refusesTheSameExternalTransactionWithAnotherKey(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	external := "external-" + suiteenv.NewID()
	payload := wallet.bet("25.00", map[string]any{"externalTransactionId": external})
	if first := submit(ctx, t, at, at.provider, "key-"+suiteenv.NewID(), payload); first.status != http.StatusCreated {
		t.Fatalf("first arrival before another key = %d, want 201: %s", first.status, first.body)
	}
	refused := submit(ctx, t, at, at.provider, "key-"+suiteenv.NewID(), payload)
	if refused.status != http.StatusUnprocessableEntity {
		t.Fatalf("another key for the same external transaction = %d, want 422: %s", refused.status, refused.body)
	}
	if got := refused.refusal(t).FailureCode; got != "DUPLICATE_EXTERNAL_TRANSACTION" {
		t.Fatalf("failureCode = %s, want DUPLICATE_EXTERNAL_TRANSACTION", got)
	}
	assertRowsForExternal(ctx, t, external, 1)
	assertWallet(ctx, t, wallet.id, 97500, 2)
}

// What authorizes is the client of the token. A body speaking for another provider
// is refused before any movement.
func TestSubmit_refusesABodyThatNamesAnotherProvider(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	key := "key-" + suiteenv.NewID()
	refused := submit(ctx, t, at, at.provider, key, wallet.bet("25.00", map[string]any{"providerId": otherClient}))
	if refused.status != http.StatusForbidden {
		t.Fatalf("body of another provider = %d, want 403: %s", refused.status, refused.body)
	}
	if got := refused.refusal(t).FailureCode; got != "" {
		t.Fatalf("failureCode = %s, want empty: no rule refused the operation", got)
	}
	assertRowsForKey(ctx, t, key, 0)
	assertWallet(ctx, t, wallet.id, 100000, 1)
}

// A transaction of another provider answers exactly what a transaction that does
// not exist answers.
func TestRead_answersTheSameAbsenceForAnotherProviderAndForNothing(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	placed := bet(ctx, t, at, wallet, "25.00")
	if placed.status != http.StatusCreated {
		t.Fatalf("the bet to read back = %d, want 201: %s", placed.status, placed.body)
	}
	alien := read(ctx, t, at, at.other, placed.transaction(t).TransactionID)
	if alien.status != http.StatusNotFound {
		t.Fatalf("read of another provider = %d, want 404: %s", alien.status, alien.body)
	}
	assertRevealsNothing(t, alien)
	absent := read(ctx, t, at, at.other, suiteenv.NewID())
	if absent.status != alien.status || refusalType(t, absent) != refusalType(t, alien) {
		t.Fatalf("absent = %d and alien = %d, want the same refusal for both", absent.status, alien.status)
	}
}

func assertRevealsNothing(t *testing.T, answered answer) {
	t.Helper()
	for _, banned := range []string{"PROCESSED", "975.00", "25.00"} {
		if bytes.Contains(answered.body, []byte(banned)) {
			t.Fatalf("body = %s, want it without %q", answered.body, banned)
		}
	}
}

// A provider replays only its own operation: the same key and the same body from
// another provider is another operation, not a replay.
func TestSubmit_keepsTheKeyScopedToTheProvider(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	key := "key-" + suiteenv.NewID()
	mine := submit(ctx, t, at, at.provider, key, wallet.bet("25.00", nil))
	if mine.status != http.StatusCreated {
		t.Fatalf("first arrival = %d, want 201: %s", mine.status, mine.body)
	}
	theirs := submit(ctx, t, at, at.other, key, wallet.bet("25.00", map[string]any{"providerId": otherClient}))
	if theirs.status != http.StatusCreated {
		t.Fatalf("the other provider = %d, want 201: the same key is another operation for it: %s", theirs.status, theirs.body)
	}
	if answered := theirs.transaction(t); answered.IdempotentReplay {
		t.Fatalf("idempotentReplay = %t, want false: it is not a replay of somebody else", answered.IdempotentReplay)
	}
	assertRowsForKey(ctx, t, key, 2)
	assertWallet(ctx, t, wallet.id, 95000, 3)
}

func TestRoutes_keepTheTwoRolesApart(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	key := "key-" + suiteenv.NewID()
	internal := submit(ctx, t, at, at.internal, key, wallet.bet("25.00", nil))
	if internal.status != http.StatusForbidden {
		t.Fatalf("internal client on the wager route = %d, want 403", internal.status)
	}
	assertRowsForKey(ctx, t, key, 0)
	balance := call(ctx, t, request{method: http.MethodGet, url: at.base + "/wallets/" + wallet.id, bearer: at.provider})
	if balance.status != http.StatusForbidden {
		t.Fatalf("provider on the wallet route = %d, want 403", balance.status)
	}
	if bytes.Contains(balance.body, []byte("1000.00")) {
		t.Fatalf("body = %s, want no balance in it", balance.body)
	}
}

func bet(ctx context.Context, t *testing.T, at suite, wallet owner, amount string) answer {
	t.Helper()
	return submitBody(ctx, t, at, wallet.bet(amount, nil))
}

func submitBody(ctx context.Context, t *testing.T, at suite, payload string) answer {
	t.Helper()
	return submit(ctx, t, at, at.provider, "key-"+suiteenv.NewID(), payload)
}

// together fires the submissions at the same time, each with its own key, which is
// what makes them contend for the wallet instead of replaying one another.
func together(ctx context.Context, t *testing.T, at suite, payloads ...string) []answer {
	t.Helper()
	answers := make([]answer, len(payloads))
	var waiting sync.WaitGroup
	for index, payload := range payloads {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			answers[index] = submit(ctx, t, at, at.provider, "key-"+suiteenv.NewID(), payload)
		}()
	}
	waiting.Wait()
	return answers
}

func split(t *testing.T, answers []answer) (created, refused int) {
	t.Helper()
	for _, answered := range answers {
		switch answered.status {
		case http.StatusCreated:
			created++
		case http.StatusUnprocessableEntity:
			refused++
		default:
			t.Fatalf("status = %d, want 201 or 422: %s", answered.status, answered.body)
		}
	}
	return created, refused
}

func assertOneRefusalToken(t *testing.T, answers []answer, want string) {
	t.Helper()
	for _, answered := range answers {
		if answered.status != http.StatusUnprocessableEntity {
			continue
		}
		if got := answered.refusal(t).FailureCode; got != want {
			t.Fatalf("failureCode = %s, want %s", got, want)
		}
	}
}

func assertRead(ctx context.Context, t *testing.T, at suite, id, status, code string) {
	t.Helper()
	answered := read(ctx, t, at, at.provider, id)
	if answered.status != http.StatusOK {
		t.Fatalf("read of %s = %d, want 200: %s", id, answered.status, answered.body)
	}
	found := answered.transaction(t)
	if found.Status != status {
		t.Fatalf("status = %s, want %s", found.Status, status)
	}
	if found.FailureCode != code {
		t.Fatalf("failureCode = %s, want %q", found.FailureCode, code)
	}
}

func refusalType(t *testing.T, answered answer) string {
	t.Helper()
	return answered.refusal(t).Type
}

func assertWallet(ctx context.Context, t *testing.T, walletID string, cents, version int64) {
	t.Helper()
	var storedCents, storedVersion int64
	err := connect(ctx, t).QueryRow(ctx, "SELECT balance_cents, version FROM wallets WHERE id = $1", walletID).
		Scan(&storedCents, &storedVersion)
	if err != nil {
		t.Fatalf("read wallet = %v, want nil", err)
	}
	if storedCents != cents || storedVersion != version {
		t.Fatalf("wallet = %d cents at version %d, want %d at version %d", storedCents, storedVersion, cents, version)
	}
}

func assertEntries(ctx context.Context, t *testing.T, walletID string, want int64) {
	t.Helper()
	if got := count(ctx, t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1", walletID); got != want {
		t.Fatalf("entries = %d, want %d", got, want)
	}
}

// assertChained walks the entries in sequence: the balance after one is the balance
// before the next, which is what the deferred trigger only checks at the commit.
func assertChained(ctx context.Context, t *testing.T, walletID string) {
	t.Helper()
	rows, err := connect(ctx, t).Query(ctx,
		"SELECT balance_before_cents, balance_after_cents FROM ledger_entries WHERE wallet_id = $1 ORDER BY sequence_number", walletID)
	if err != nil {
		t.Fatalf("query of the entries = %v, want nil", err)
	}
	defer rows.Close()
	previous := int64(-1)
	for rows.Next() {
		previous = assertFollows(t, rows, previous)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("walk of the entries = %v, want nil", err)
	}
}

func assertFollows(t *testing.T, rows pgx.Rows, previous int64) int64 {
	t.Helper()
	var before, after int64
	if err := rows.Scan(&before, &after); err != nil {
		t.Fatalf("scan entry = %v, want nil", err)
	}
	if previous >= 0 && before != previous {
		t.Fatalf("entry starts at %d, want the %d the one before it left", before, previous)
	}
	return after
}

func assertRowsForKey(ctx context.Context, t *testing.T, key string, want int64) {
	t.Helper()
	got := count(ctx, t, "SELECT count(*) FROM wager_transactions WHERE idempotency_key = $1", key)
	if got != want {
		t.Fatalf("transactions for the key = %d, want %d", got, want)
	}
}

func assertRowsForExternal(ctx context.Context, t *testing.T, external string, want int64) {
	t.Helper()
	got := count(ctx, t, "SELECT count(*) FROM wager_transactions WHERE external_id = $1", external)
	if got != want {
		t.Fatalf("transactions for the external transaction = %d, want %d", got, want)
	}
}

func rejectedID(ctx context.Context, t *testing.T, key string) string {
	t.Helper()
	var id string
	err := connect(ctx, t).QueryRow(ctx, "SELECT id FROM wager_transactions WHERE idempotency_key = $1", key).Scan(&id)
	if err != nil {
		t.Fatalf("read the rejected transaction = %v, want nil", err)
	}
	return id
}

func count(ctx context.Context, t *testing.T, query, argument string) int64 {
	t.Helper()
	var total int64
	if err := connect(ctx, t).QueryRow(ctx, query, argument).Scan(&total); err != nil {
		t.Fatalf("count = %v, want nil", err)
	}
	return total
}
