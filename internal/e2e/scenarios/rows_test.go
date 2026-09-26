//go:build integration

package scenarios

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// store is the suite database as a case reads it back: one connection of the
// case, outside every instance, so what it reads is what the instances committed.
type store struct {
	conn *pgx.Conn
}

func connect(ctx context.Context, t *testing.T) store {
	t.Helper()
	conn, err := pgx.Connect(ctx, suiteenv.DatabaseURL())
	if err != nil {
		t.Fatalf("connect = %v, want nil: the case needs the migration applied", err)
	}
	closing := context.WithoutCancel(ctx)
	t.Cleanup(func() { _ = conn.Close(closing) })
	return store{conn: conn}
}

// stored is the state of one wallet: the balance in cents and the version.
type stored struct {
	cents   int64
	version int64
}

func (s store) wallet(ctx context.Context, t *testing.T, walletID string) stored {
	t.Helper()
	var read stored
	err := s.conn.QueryRow(ctx, "SELECT balance_cents, version FROM wallets WHERE id = $1", walletID).
		Scan(&read.cents, &read.version)
	if err != nil {
		t.Fatalf("read the wallet = %v, want nil", err)
	}
	return read
}

// entries is every ledger entry of the wallet, the credit of its opening included.
func (s store) entries(ctx context.Context, t *testing.T, walletID string) int64 {
	t.Helper()
	return s.count(ctx, t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1", walletID)
}

func (s store) debits(ctx context.Context, t *testing.T, walletID string) int64 {
	t.Helper()
	return s.count(ctx, t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'", walletID)
}

// forKey is every transaction written under that idempotency key of the provider.
func (s store) forKey(ctx context.Context, t *testing.T, key string) int64 {
	t.Helper()
	const query = "SELECT count(*) FROM wager_transactions WHERE provider_id = 'provider-a' AND idempotency_key = $1"
	return s.count(ctx, t, query, key)
}

// eventsOf answers the types of the outbox rows of one transaction, in order,
// with a type written twice appearing twice.
func (s store) eventsOf(ctx context.Context, t *testing.T, transactionID string) []string {
	t.Helper()
	const query = "SELECT event_type FROM outbox_events WHERE payload -> 'data' ->> 'transactionId' = $1"
	rows, err := s.conn.Query(ctx, query, transactionID)
	if err != nil {
		t.Fatalf("read the outbox of the transaction = %v, want nil", err)
	}
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect the outbox of the transaction = %v, want nil", err)
	}
	slices.Sort(types)
	return types
}

func (s store) count(ctx context.Context, t *testing.T, query, argument string) int64 {
	t.Helper()
	var total int64
	if err := s.conn.QueryRow(ctx, query, argument).Scan(&total); err != nil {
		t.Fatalf("count = %v, want nil", err)
	}
	return total
}
