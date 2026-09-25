//go:build integration

// The migration of the wait queue, against a database the first migration already
// shaped. What is under test is that the migration is additive: applying it again
// changes nothing, and undoing it takes away the index and the constraint it added
// and nothing else.
package schema

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// waitQueueIndex is the index the second migration creates, spelled out here
// instead of read from the file: a case built from the file would pass at any
// name, and the adapter and the query plan both depend on this one.
const waitQueueIndex = "wager_transactions_wait_queue"

// waitDeadlineConstraint is what the migration adds beside the index: the deadline
// of a wait becomes load-bearing here, and a row in the wait without one expires
// on its first attempt. Spelled out for the same reason as the index.
const waitDeadlineConstraint = "wager_transactions_waiting_has_deadline"

// The two files of the migration, spelled whole so the read takes a constant
// path: a name built at the call site is a file this suite did not choose.
const (
	upMigration   = "../../../deploy/migrations/000002_reference_wait_queue.up.sql"
	downMigration = "../../../deploy/migrations/000002_reference_wait_queue.down.sql"
)

// The queue is a fraction of the table, so the index is restricted to the status
// of the wait: a terminal row neither enters it nor maintains it.
func TestWaitQueueIndex_coversOnlyTheRowsInTheWait(t *testing.T) {
	ctx, conn := connect(t)
	definition := indexDefinition(ctx, t, conn, waitQueueIndex)
	if definition == "" {
		t.Fatalf("index %s is absent, want it created by the migration", waitQueueIndex)
	}
	for _, part := range []string{"next_attempt_at", "WHERE (status = 'PENDING_REFERENCE'"} {
		if !strings.Contains(definition, part) {
			t.Fatalf("definition = %q, want %q in it", definition, part)
		}
	}
}

// The migration runs against a database that is already migrated, so applying it
// a second time has to be a no-op rather than a failure.
func TestWaitQueueMigration_appliesTwiceWithoutFailing(t *testing.T) {
	ctx, conn := connect(t)
	statements, err := os.ReadFile(upMigration)
	if err != nil {
		t.Fatalf("read the migration = %v, want nil", err)
	}
	if _, err := conn.Exec(ctx, string(statements)); err != nil {
		t.Fatalf("second application of the migration = %v, want nil", err)
	}
	if indexDefinition(ctx, t, conn, waitQueueIndex) == "" {
		t.Fatalf("index %s is absent after applying twice, want it there", waitQueueIndex)
	}
}

// Undoing the migration leaves out the index and the constraint it added, and the
// rest of the schema as it was. The case runs inside a transaction it rolls back,
// because the database under it is shared with every other suite.
func TestWaitQueueMigration_undoesWhatItAddedAndNothingElse(t *testing.T) {
	ctx, conn := connect(t)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin = %v, want nil", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	before := schemaObjects(ctx, t, tx)
	statements, err := os.ReadFile(downMigration)
	if err != nil {
		t.Fatalf("read the undo of the migration = %v, want nil", err)
	}
	if _, err := tx.Exec(ctx, string(statements)); err != nil {
		t.Fatalf("undo of the migration = %v, want nil", err)
	}
	if definition := indexDefinition(ctx, t, tx, waitQueueIndex); definition != "" {
		t.Fatalf("index after the undo = %q, want it gone", definition)
	}
	after := schemaObjects(ctx, t, tx)
	if after["constraint:"+waitDeadlineConstraint] {
		t.Fatalf("constraint %s is there after the undo, want it gone", waitDeadlineConstraint)
	}
	assertOnlyThisMigrationUndone(t, before, after)
}

// added is what this migration brings, and the only thing its undo may take.
var added = map[string]bool{
	"index:" + waitQueueIndex:              true,
	"constraint:" + waitDeadlineConstraint: true,
}

func assertOnlyThisMigrationUndone(t *testing.T, before, after map[string]bool) {
	t.Helper()
	for object := range before {
		if added[object] {
			continue
		}
		if !after[object] {
			t.Fatalf("%s is gone after the undo, want only what the migration added removed", object)
		}
	}
	for object := range after {
		if !before[object] {
			t.Fatalf("%s appeared after the undo, want nothing created", object)
		}
	}
}

// querier of one query row, which both the connection and the open transaction
// answer: the case reads the same catalog either way.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// indexDefinition answers the CREATE INDEX the catalog holds, or the empty
// string when there is no such index.
func indexDefinition(ctx context.Context, t *testing.T, from rowQuerier, name string) string {
	t.Helper()
	var definition *string
	err := from.QueryRow(ctx,
		"SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND indexname = $1", name).Scan(&definition)
	if errors.Is(err, pgx.ErrNoRows) || definition == nil {
		return ""
	}
	if err != nil {
		t.Fatalf("read index definition = %v, want nil", err)
	}
	return *definition
}

// The catalog of every index, constraint and grant of the three financial
// tables, which is what an additive migration has to leave untouched.
const schemaCatalog = `
SELECT 'index:' || indexname FROM pg_indexes
 WHERE schemaname = 'public' AND tablename IN ('wallets', 'wager_transactions', 'ledger_entries')
UNION ALL
SELECT 'constraint:' || conname FROM pg_constraint
 WHERE connamespace = 'public'::regnamespace
UNION ALL
SELECT 'grant:' || grantee || ':' || table_name || ':' || privilege_type FROM information_schema.table_privileges
 WHERE table_schema = 'public'`

func schemaObjects(ctx context.Context, t *testing.T, from rowQuerier) map[string]bool {
	t.Helper()
	rows, err := from.Query(ctx, schemaCatalog)
	if err != nil {
		t.Fatalf("read the schema catalog = %v, want nil", err)
	}
	defer rows.Close()
	found := map[string]bool{}
	for rows.Next() {
		var object string
		if err := rows.Scan(&object); err != nil {
			t.Fatalf("scan the schema catalog = %v, want nil", err)
		}
		found[object] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("walk the schema catalog = %v, want nil", err)
	}
	return found
}
