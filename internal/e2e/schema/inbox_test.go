//go:build integration

package schema

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// The consumer of the ingress queue, as the row names it. Two names are used
// below because the unicity is the pair and not the identifier alone.
const (
	ingressConsumer = "wager-ingress"
	otherConsumer   = "another-consumer"
)

func TestInboxMessages_acceptTheSameConsumerAndMessageOnlyOnce(t *testing.T) {
	ctx, conn := connect(t)
	message := suiteenv.NewID()
	if err := insertInbox(ctx, conn, inbox{consumer: ingressConsumer, message: message, hash: "hash-1"}); err != nil {
		t.Fatalf("first inbox row = %v, want nil", err)
	}
	second := insertInbox(ctx, conn, inbox{consumer: ingressConsumer, message: message, hash: "hash-2"})
	assertRefused(t, second, "inbox_messages_one_per_consumer_and_message")
	if got := storedHash(ctx, t, conn, ingressConsumer, message); got != "hash-1" {
		t.Fatalf("recorded hash = %q, want the original hash-1", got)
	}
}

func TestInboxMessages_acceptTheSameMessageFromTwoConsumers(t *testing.T) {
	ctx, conn := connect(t)
	message := suiteenv.NewID()
	if err := insertInbox(ctx, conn, inbox{consumer: ingressConsumer, message: message, hash: "hash-1"}); err != nil {
		t.Fatalf("row of the first consumer = %v, want nil", err)
	}
	if err := insertInbox(ctx, conn, inbox{consumer: otherConsumer, message: message, hash: "hash-1"}); err != nil {
		t.Fatalf("row of the second consumer = %v, want nil", err)
	}
}

// The hash is what tells a legitimate redelivery from the same identifier
// re-presented with another body, so a row that could be rewritten would turn
// the second into the first.
func TestInboxMessages_refuseAnUpdateOverTheRecordedHash(t *testing.T) {
	ctx, conn := connect(t)
	message := suiteenv.NewID()
	if err := insertInbox(ctx, conn, inbox{consumer: ingressConsumer, message: message, hash: "hash-1"}); err != nil {
		t.Fatalf("inbox row = %v, want nil", err)
	}
	const update = "UPDATE inbox_messages SET body_hash = $1 WHERE consumer = $2 AND message_id = $3"
	if _, err := conn.Exec(ctx, update, "hash-2", ingressConsumer, message); err == nil {
		t.Fatalf("update over the hash = nil, want a refusal")
	}
	if got := storedHash(ctx, t, conn, ingressConsumer, message); got != "hash-1" {
		t.Fatalf("recorded hash = %q, want the untouched hash-1", got)
	}
}

// The application inserts the row in the commit and re-reads it when the unicity
// refuses. It never updates and never deletes, so the role holds neither
// privilege.
func TestApplicationRole_holdsOnlySelectAndInsertOnTheInbox(t *testing.T) {
	ctx, conn := connect(t)
	message := suiteenv.NewID()
	if err := insertInbox(ctx, conn, inbox{consumer: ingressConsumer, message: message, hash: "hash-1"}); err != nil {
		t.Fatalf("inbox row of the role case = %v, want nil", err)
	}
	if _, err := conn.Exec(ctx, "SET ROLE wager_app"); err != nil {
		t.Fatalf("set role = %v, want nil", err)
	}
	const read = "SELECT 1 FROM inbox_messages WHERE consumer = $1 AND message_id = $2"
	if _, err := conn.Exec(ctx, read, ingressConsumer, message); err != nil {
		t.Fatalf("select as the application role = %v, want nil", err)
	}
	const update = "UPDATE inbox_messages SET body_hash = $1 WHERE consumer = $2 AND message_id = $3"
	if _, err := conn.Exec(ctx, update, "hash-2", ingressConsumer, message); err == nil {
		t.Fatalf("update as the application role = nil, want a refusal")
	}
	const remove = "DELETE FROM inbox_messages WHERE consumer = $1 AND message_id = $2"
	if _, err := conn.Exec(ctx, remove, ingressConsumer, message); err == nil {
		t.Fatalf("delete as the application role = nil, want a refusal")
	}
}

// The migration is additive: the inbox stands beside the financial tables and
// points at none of them, and none of them gained anything that names it. A
// foreign key to the wallet would be the first thing to make the two move
// together.
func TestInboxMessages_standBesideTheFinancialTables(t *testing.T) {
	ctx, conn := connect(t)
	const foreignKeys = `
SELECT count(*) FROM information_schema.table_constraints
WHERE table_name = 'inbox_messages' AND constraint_type = 'FOREIGN KEY'`
	var pointing int64
	if err := conn.QueryRow(ctx, foreignKeys).Scan(&pointing); err != nil {
		t.Fatalf("read the foreign keys of the inbox = %v, want nil", err)
	}
	if pointing != 0 {
		t.Fatalf("foreign keys of the inbox = %d, want 0", pointing)
	}
	const naming = `
SELECT count(*) FROM information_schema.columns
WHERE table_name IN ('wallets', 'wager_transactions', 'ledger_entries', 'outbox_events')
  AND column_name LIKE '%inbox%'`
	var columns int64
	if err := conn.QueryRow(ctx, naming).Scan(&columns); err != nil {
		t.Fatalf("read the columns of the financial tables = %v, want nil", err)
	}
	if columns != 0 {
		t.Fatalf("columns naming the inbox = %d, want 0", columns)
	}
}

// inbox is one row of the inbox as a case writes it.
type inbox struct {
	consumer string
	message  string
	hash     string
}

const insertInboxSQL = `
INSERT INTO inbox_messages (consumer, message_id, body_hash, created_at)
VALUES ($1, $2, $3, now())`

func insertInbox(ctx context.Context, conn querier, row inbox) error {
	_, err := conn.Exec(ctx, insertInboxSQL, row.consumer, row.message, row.hash)
	return err
}

func storedHash(ctx context.Context, t *testing.T, conn *pgx.Conn, consumer, message string) string {
	t.Helper()
	const read = "SELECT body_hash FROM inbox_messages WHERE consumer = $1 AND message_id = $2"
	var hash string
	if err := conn.QueryRow(ctx, read, consumer, message).Scan(&hash); err != nil {
		t.Fatalf("read the recorded hash = %v, want nil", err)
	}
	return hash
}
