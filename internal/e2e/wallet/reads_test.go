//go:build integration

package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// The support of the two wallet reads: the wagers that make a ledger grow, the
// two routes as the client calls them, and the write that produces the state
// the reconciliation exists to detect.

// externalEntry is one ledger entry as the client reads it.
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

type externalReconciliation struct {
	WalletID           string        `json:"walletId"`
	StoredBalance      externalMoney `json:"storedBalance"`
	LedgerBalance      externalMoney `json:"ledgerBalance"`
	Version            int64         `json:"version"`
	EntryCount         int64         `json:"entryCount"`
	LastSequence       int64         `json:"lastSequence"`
	Consistent         bool          `json:"consistent"`
	Divergences        []string      `json:"divergences"`
	FirstBreakSequence int64         `json:"firstBreakSequence"`
}

type externalTransaction struct {
	TransactionID string `json:"transactionId"`
	Status        string `json:"status"`
}

// funded is a wallet opened with a thousand and the provider token that moves
// it: what every case about a ledger with movements starts from.
type funded struct {
	wallet   externalWallet
	provider string
}

// openFunded opens a wallet with a thousand, which covers the wagers below and
// keeps the arithmetic of each balance readable.
func openFunded(ctx context.Context, t *testing.T, base, internal string) funded {
	t.Helper()
	opened, status := open(ctx, t, base, internal, body(suiteenv.NewID(), "1000.00", "BRL"))
	if status != http.StatusCreated {
		t.Fatalf("opening before the read = %d, want 201", status)
	}
	return funded{wallet: opened, provider: tokenFor(ctx, t, providerClient, providerSecret)}
}

// bet debits the wallet through the wager route, with the provider token, and
// answers the transaction that was recorded.
func (f funded) bet(ctx context.Context, t *testing.T, base, amount string) externalTransaction {
	t.Helper()
	return f.submit(ctx, t, base, "BET", amount)
}

// win credits the wallet the same way.
func (f funded) win(ctx context.Context, t *testing.T, base, amount string) externalTransaction {
	t.Helper()
	return f.submit(ctx, t, base, "WIN", amount)
}

func (f funded) submit(ctx context.Context, t *testing.T, base, kind, amount string) externalTransaction {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"providerId":            providerClient,
		"externalTransactionId": "external-" + suiteenv.NewID(),
		"roundId":               "round-" + suiteenv.NewID(),
		"gameId":                "game-1",
		"playerId":              f.wallet.PlayerID,
		"walletId":              f.wallet.ID,
		"kind":                  kind,
		"money":                 map[string]string{"amount": amount, "currency": "BRL"},
	})
	if err != nil {
		t.Fatalf("marshal %s = %v, want nil", kind, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/wagering/transactions", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("request of the %s = %v, want nil", kind, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+f.provider)
	req.Header.Set("Idempotency-Key", suiteenv.NewID())
	var answered externalTransaction
	if status := call(t, req, &answered); status != http.StatusCreated {
		t.Fatalf("%s of %s = %d, want 201", kind, amount, status)
	}
	if answered.Status != "PROCESSED" {
		t.Fatalf("%s of %s = %s, want PROCESSED", kind, amount, answered.Status)
	}
	return answered
}

func ledgerURL(base, walletID, rawQuery string) string {
	target := base + "/wallets/" + walletID + "/ledger"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	return target
}

func listLedger(ctx context.Context, t *testing.T, base, bearer, walletID, rawQuery string) (externalLedger, int) {
	t.Helper()
	var answered externalLedger
	status := request(ctx, t, http.MethodGet, ledgerURL(base, walletID, rawQuery), bearer, "", &answered)
	return answered, status
}

// refusalOf answers the status, the media type and the raw body of a refused
// read, so a case can compare two refusals byte for byte.
func refusalOf(ctx context.Context, t *testing.T, rawURL, bearer string) (int, string, []byte) {
	t.Helper()
	res := send(ctx, t, http.MethodGet, rawURL, bearer, "")
	defer func() { _ = res.Body.Close() }()
	answered, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s = %v, want nil", rawURL, err)
	}
	return res.StatusCode, res.Header.Get("Content-Type"), answered
}

func problemOf(t *testing.T, raw []byte) problem.Details {
	t.Helper()
	var refusal problem.Details
	if err := json.Unmarshal(raw, &refusal); err != nil {
		t.Fatalf("unmarshal problem = %v, want nil: %s", err, raw)
	}
	return refusal
}

func reconciliationURL(base, walletID string) string {
	return base + "/wallets/" + walletID + "/reconciliation"
}

func reconcile(ctx context.Context, t *testing.T, base, bearer, walletID string) (externalReconciliation, int) {
	t.Helper()
	var answered externalReconciliation
	status := request(ctx, t, http.MethodGet, reconciliationURL(base, walletID), bearer, "", &answered)
	return answered, status
}

// divergeBalance writes a stored balance the ledger does not sum to, which is one
// of the three states the reconciliation exists to detect and one the application
// role cannot produce.
func divergeBalance(ctx context.Context, t *testing.T, walletID string, cents int64) {
	t.Helper()
	withoutTriggers(ctx, t, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, updateStoredBalance, walletID, cents); err != nil {
			t.Fatalf("write the balance past the ledger = %v, want nil", err)
		}
	})
}

// brokenEntry is a ledger row the application could not write: the case spells the
// sequence, the credit and the balance the row starts from, and the helper closes
// the movement the way the per-row CHECK demands. The identity comes from
// lowEntryID, for the reason given there.
type brokenEntry struct {
	entryID       string
	sequence      int64
	amountCents   int64
	balanceBefore int64
}

// insertEntryPastTheApplication writes that row and moves the stored balance onto
// the signed sum of the ledger, so the report names the divergence the case is
// about and not a balance mismatch on top of it. It is the only way to reach the
// aggregate and window arms of the summary statement.
func insertEntryPastTheApplication(ctx context.Context, t *testing.T, walletID string, entry brokenEntry, storedCents int64) {
	t.Helper()
	withoutTriggers(ctx, t, func(tx pgx.Tx) {
		after := entry.balanceBefore + entry.amountCents
		args := []any{entry.entryID, walletID, suiteenv.NewID(), entry.amountCents, entry.balanceBefore, after, entry.sequence}
		if _, err := tx.Exec(ctx, insertLedgerEntry, args...); err != nil {
			t.Fatalf("write the entry past the application = %v, want nil", err)
		}
		if _, err := tx.Exec(ctx, updateStoredBalance, walletID, storedCents); err != nil {
			t.Fatalf("move the stored balance onto the sum of the ledger = %v, want nil", err)
		}
	})
}

// The application role holds only SELECT and INSERT on the ledger, and the
// currency is the one openFunded opens the wallet with.
const insertLedgerEntry = `
INSERT INTO ledger_entries (id, wallet_id, transaction_id, direction, amount_cents, currency,
                            balance_before_cents, balance_after_cents, sequence_number, created_at)
VALUES ($1, $2, $3, 'CREDIT', $4, 'BRL', $5, $6, $7, now())`

const updateStoredBalance = `UPDATE wallets SET balance_cents = $2 WHERE id = $1`

// lowEntryID mints an identity whose first four bytes are zero, which sorts below
// every identity the application mints, and whose tail is random, which keeps the
// primary key free across cases and across runs of the suite.
func lowEntryID() string {
	return "00000000-0000-4000-8000-" + suiteenv.NewID()[24:]
}

// withoutTriggers runs the write with the triggers off: the deferred balance
// trigger and the foreign key of the transaction refuse every row these cases
// need, while the per-row CHECK stays on. Only a superuser may set
// session_replication_role, and a suite pointed at another user fails by name.
func withoutTriggers(ctx context.Context, t *testing.T, write func(tx pgx.Tx)) {
	t.Helper()
	conn := connect(ctx, t)
	requireSuperuser(ctx, t, conn)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the divergence = %v, want nil", err)
	}
	if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		t.Fatalf("switch the triggers off = %v, want nil: the divergence needs a superuser", err)
	}
	write(tx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit the divergence = %v, want nil: the triggers were expected to be off", err)
	}
}

func requireSuperuser(ctx context.Context, t *testing.T, conn *pgx.Conn) {
	t.Helper()
	var user string
	var superuser bool
	err := conn.QueryRow(ctx, "SELECT current_user, rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&user, &superuser)
	if err != nil {
		t.Fatalf("read the role of the suite = %v, want nil", err)
	}
	if !superuser {
		t.Fatalf("the suite connects as %s, which is not a superuser: only a superuser sets session_replication_role, and the divergence needs it", user)
	}
}

func storedBalance(ctx context.Context, t *testing.T, walletID string) (int64, int64) {
	t.Helper()
	var cents, version int64
	err := connect(ctx, t).QueryRow(ctx, "SELECT balance_cents, version FROM wallets WHERE id = $1", walletID).Scan(&cents, &version)
	if err != nil {
		t.Fatalf("read wallet = %v, want nil", err)
	}
	return cents, version
}

func openingTransaction(ctx context.Context, t *testing.T, walletID string) string {
	t.Helper()
	var id string
	err := connect(ctx, t).QueryRow(ctx, "SELECT id FROM wager_transactions WHERE wallet_id = $1 AND kind = 'OPENING'", walletID).Scan(&id)
	if err != nil {
		t.Fatalf("read the opening = %v, want nil", err)
	}
	return id
}
