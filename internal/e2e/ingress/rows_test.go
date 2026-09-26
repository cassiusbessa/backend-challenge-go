//go:build integration

package ingress

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
)

// wallet is the stored state of one wallet: the balance in cents and the version.
type wallet struct {
	cents   int64
	version int64
}

func storedWallet(ctx context.Context, t *testing.T, conn *pgx.Conn, walletID string) wallet {
	t.Helper()
	const query = "SELECT balance_cents, version FROM wallets WHERE id = $1"
	var stored wallet
	if err := conn.QueryRow(ctx, query, walletID).Scan(&stored.cents, &stored.version); err != nil {
		t.Fatalf("read the wallet = %v, want nil", err)
	}
	return stored
}

func countEntries(ctx context.Context, t *testing.T, conn *pgx.Conn, walletID string) int64 {
	t.Helper()
	return count(ctx, t, conn, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1", walletID)
}

func countInbox(ctx context.Context, t *testing.T, conn *pgx.Conn, messageID string) int64 {
	t.Helper()
	return count(ctx, t, conn, "SELECT count(*) FROM inbox_messages WHERE message_id = $1", messageID)
}

// countExternal is every transaction of that wallet that came from a provider. The
// opening is left out so a case reads what the queue produced and not the birth of
// the wallet.
func countExternal(ctx context.Context, t *testing.T, conn *pgx.Conn, walletID string) int64 {
	t.Helper()
	const query = "SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind <> 'OPENING'"
	return count(ctx, t, conn, query, walletID)
}

// storedKey answers the idempotency key of the single operation of that wallet
// that came from a provider, as the row keeps it.
func storedKey(ctx context.Context, t *testing.T, conn *pgx.Conn, walletID string) string {
	t.Helper()
	const query = "SELECT idempotency_key FROM wager_transactions WHERE wallet_id = $1 AND kind <> 'OPENING'"
	var key string
	if err := conn.QueryRow(ctx, query, walletID).Scan(&key); err != nil {
		t.Fatalf("read the recorded key = %v, want nil", err)
	}
	return key
}

func count(ctx context.Context, t *testing.T, conn *pgx.Conn, query, argument string) int64 {
	t.Helper()
	var total int64
	if err := conn.QueryRow(ctx, query, argument).Scan(&total); err != nil {
		t.Fatalf("count = %v, want nil", err)
	}
	return total
}

// transaction is the recorded outcome of one operation, as a case reads it.
type transaction struct {
	id     string
	status string
}

// externalTransaction answers the single operation of that wallet that came from a
// provider, once there is one.
func externalTransaction(ctx context.Context, t *testing.T, conn *pgx.Conn, walletID string) transaction {
	t.Helper()
	const query = `
SELECT id, status FROM wager_transactions
WHERE wallet_id = $1 AND kind <> 'OPENING'`
	var found transaction
	if err := conn.QueryRow(ctx, query, walletID).Scan(&found.id, &found.status); err != nil {
		t.Fatalf("read the transaction = %v, want nil", err)
	}
	return found
}

// event is one row of the outbox, read as the payload the broker receives.
type event struct {
	eventType     string
	correlationID string
	causationID   string
}

// events answers the outbox rows of one wallet, oldest first, which is the order
// the relay publishes them in.
func events(ctx context.Context, t *testing.T, conn *pgx.Conn, walletID string) []event {
	t.Helper()
	const query = `
SELECT event_type, payload FROM outbox_events
WHERE wallet_id = $1
ORDER BY created_at, event_id`
	rows, err := conn.Query(ctx, query, walletID)
	if err != nil {
		t.Fatalf("read the outbox = %v, want nil", err)
	}
	defer rows.Close()
	var out []event
	for rows.Next() {
		var eventType string
		var payload []byte
		if err := rows.Scan(&eventType, &payload); err != nil {
			t.Fatalf("scan an outbox row = %v, want nil", err)
		}
		out = append(out, event{eventType: eventType, correlationID: fieldOf(t, payload, "correlationId"), causationID: fieldOf(t, payload, "causationId")})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate the outbox = %v, want nil", err)
	}
	return out
}

// fieldOf reads one string field of the payload, and answers the empty string for a
// field the envelope omitted — which is how an absent cause is told from one that
// was sent empty.
func fieldOf(t *testing.T, payload []byte, field string) string {
	t.Helper()
	var read map[string]json.RawMessage
	if err := json.Unmarshal(payload, &read); err != nil {
		t.Fatalf("unmarshal an outbox payload = %v, want nil", err)
	}
	raw, ok := read[field]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("unmarshal %s = %v, want nil", field, err)
	}
	return value
}

// awaitEvents waits for that many outbox rows of the wallet, which is what says the
// commit went through with its events in it.
func awaitEvents(ctx context.Context, t *testing.T, conn *pgx.Conn, walletID string, want int) []event {
	t.Helper()
	var got []event
	until(t, "the commit to leave its outbox rows", func() bool {
		got = events(ctx, t, conn, walletID)
		return len(got) >= want
	})
	return got
}
