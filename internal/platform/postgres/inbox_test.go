package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// recordedAt is the instant the row of the message is stamped with. It is fixed
// so a case reads an instant and not a range.
var recordedAt = time.Date(2026, time.September, 25, 12, 0, 0, 0, time.UTC)

func arrived() storage.Message {
	return storage.Message{Consumer: "wager-ingress", MessageID: "message-1", BodyHash: "hash-1", At: recordedAt}
}

func TestInsert_recordsTheMessageAndAnswersItBack(t *testing.T) {
	t.Parallel()
	savepoint := &inboxSavepoint{}
	recorded, err := inbox{tx: &inboxTx{savepoint: savepoint}}.Insert(context.Background(), arrived())
	if err != nil {
		t.Fatalf("Insert = %v, want nil", err)
	}
	if recorded != arrived() {
		t.Fatalf("recorded message = %+v, want %+v", recorded, arrived())
	}
	if !savepoint.committed {
		t.Fatalf("savepoint committed = false, want true")
	}
	if len(savepoint.args) != 4 {
		t.Fatalf("arguments = %d, want 4", len(savepoint.args))
	}
}

// The refusal is the unicity of consumer and message, and the row that won it is
// what tells a redelivery of the same body from the same identifier re-presented
// with another one.
func TestInsert_answersTheRecordedRowWhenTheUnicityRefuses(t *testing.T) {
	t.Parallel()
	refused := &pgconn.PgError{Code: uniqueViolation, ConstraintName: inboxUniqueConstraint}
	stored := storage.Message{Consumer: "wager-ingress", MessageID: "message-1", BodyHash: "hash-of-the-first", At: recordedAt}
	tx := &inboxTx{savepoint: &inboxSavepoint{refuse: refused}, stored: &stored}
	recorded, err := inbox{tx: tx}.Insert(context.Background(), arrived())
	if !errors.Is(err, storage.ErrMessageRecorded) {
		t.Fatalf("Insert over a recorded message = %v, want %v", err, storage.ErrMessageRecorded)
	}
	if recorded.BodyHash != "hash-of-the-first" {
		t.Fatalf("hash answered = %q, want the recorded hash-of-the-first", recorded.BodyHash)
	}
	if frames := fault.Stack(err); len(frames) != 0 {
		t.Fatalf("frames = %d, want 0: a refusal of the contract carries no stack", len(frames))
	}
}

// The savepoint is rolled back before the row is read: the statement that was
// refused leaves the transaction unable to answer anything until it is.
func TestInsert_rollsTheSavepointBackBeforeReadingTheRecordedRow(t *testing.T) {
	t.Parallel()
	refused := &pgconn.PgError{Code: uniqueViolation, ConstraintName: inboxUniqueConstraint}
	stored := arrived()
	tx := &inboxTx{savepoint: &inboxSavepoint{refuse: refused}, stored: &stored}
	if _, err := (inbox{tx: tx}).Insert(context.Background(), arrived()); err == nil {
		t.Fatalf("Insert over a recorded message = nil, want a refusal")
	}
	if !tx.savepoint.rolledBack {
		t.Fatalf("savepoint rolled back = false, want true")
	}
	if !tx.readAfterRollback {
		t.Fatalf("read taken after the rollback = false, want true")
	}
}

// A failure of infrastructure has no winning row to answer, so it leaves on its
// own with the stack of where it was seen.
func TestInsert_answersTheFailureOfInfrastructureWithNoRecordedRow(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection reset")
	tx := &inboxTx{savepoint: &inboxSavepoint{refuse: broken}}
	recorded, err := inbox{tx: tx}.Insert(context.Background(), arrived())
	if !errors.Is(err, broken) {
		t.Fatalf("Insert over a broken connection = %v, want %v", err, broken)
	}
	if recorded != (storage.Message{}) {
		t.Fatalf("recorded message = %+v, want the zero value", recorded)
	}
	if frames := fault.Stack(err); len(frames) == 0 {
		t.Fatalf("frames = 0, want the stack of an infrastructure failure")
	}
	if tx.reads != 0 {
		t.Fatalf("reads of the recorded row = %d, want 0", tx.reads)
	}
}

func TestInsert_answersTheFailureOfOpeningTheSavepoint(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection reset")
	tx := &inboxTx{beginErr: broken}
	if _, err := (inbox{tx: tx}).Insert(context.Background(), arrived()); !errors.Is(err, broken) {
		t.Fatalf("Insert with no savepoint = %v, want %v", err, broken)
	}
}

// inboxTx is the open transaction as this repository uses it: it hands out one
// savepoint and answers the recorded row.
type inboxTx struct {
	pgx.Tx
	savepoint *inboxSavepoint
	beginErr  error
	// stored is the row the read answers, and nil is a row that is not there.
	stored            *storage.Message
	reads             int
	readAfterRollback bool
}

func (t *inboxTx) Begin(context.Context) (pgx.Tx, error) {
	if t.beginErr != nil {
		return nil, t.beginErr
	}
	return t.savepoint, nil
}

func (t *inboxTx) QueryRow(context.Context, string, ...any) pgx.Row {
	t.reads++
	t.readAfterRollback = t.savepoint.rolledBack
	if t.stored == nil {
		return missingRow{}
	}
	return storedRow{message: *t.stored}
}

// inboxSavepoint is the nested transaction the attempt runs in.
type inboxSavepoint struct {
	pgx.Tx
	refuse     error
	args       []any
	committed  bool
	rolledBack bool
}

func (s *inboxSavepoint) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	s.args = args
	return pgconn.CommandTag{}, s.refuse
}

func (s *inboxSavepoint) Commit(context.Context) error {
	s.committed = true
	return nil
}

func (s *inboxSavepoint) Rollback(context.Context) error {
	s.rolledBack = true
	return nil
}

type storedRow struct {
	message storage.Message
}

func (r storedRow) Scan(dest ...any) error {
	*dest[0].(*string) = r.message.Consumer
	*dest[1].(*string) = r.message.MessageID
	*dest[2].(*string) = r.message.BodyHash
	*dest[3].(*time.Time) = r.message.At
	return nil
}

type missingRow struct{}

func (missingRow) Scan(...any) error {
	return pgx.ErrNoRows
}
