package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
)

const insertInboxMessage = `
INSERT INTO inbox_messages (consumer, message_id, body_hash, created_at)
VALUES ($1, $2, $3, $4)`

const selectInboxMessage = `
SELECT consumer, message_id, body_hash, created_at
FROM inbox_messages
WHERE consumer = $1 AND message_id = $2`

// inbox writes the row of the message that caused one commit. There is no update
// and no delete here: the row is immutable, and the schema refuses both.
type inbox struct {
	tx pgx.Tx
}

// Insert records the message beside the operation it caused, and answers the row
// already there when the unicity refuses this one.
//
// The attempt runs inside a savepoint. A unique violation aborts the statement
// and everything after it, so without the savepoint the refusal would take the
// commit of the operation with it and the caller would have nothing left to
// decide with — the movement is rolled back either way, but which of the two
// redeliveries this is only the recorded hash can say.
//
// The decision stays with the unicity of the database rather than with a query
// taken first: two replicas can both pass a query and neither can both win the
// same index.
func (r inbox) Insert(ctx context.Context, message storage.Message) (storage.Message, error) {
	attempt, err := r.tx.Begin(ctx)
	if err != nil {
		return storage.Message{}, wrap("open inbox savepoint", err)
	}
	_, err = attempt.Exec(ctx, insertInboxMessage,
		message.Consumer,
		message.MessageID,
		message.BodyHash,
		message.At,
	)
	if err == nil {
		return message, wrap("commit inbox savepoint", attempt.Commit(ctx))
	}
	// The savepoint is rolled back before anything else is asked: the statement
	// that was refused left the transaction unable to answer until it is.
	_ = attempt.Rollback(ctx)
	return r.recorded(ctx, message, wrap("insert inbox message", err))
}

// recorded answers the row that won the unicity, together with the refusal the
// insert answered.
//
// A refusal of another kind is answered on its own: only the unicity has a
// winning row to read, and reading one for a failure of infrastructure would
// hide it behind an absence.
func (r inbox) recorded(ctx context.Context, message storage.Message, refusal error) (storage.Message, error) {
	if !errors.Is(refusal, storage.ErrMessageRecorded) {
		return storage.Message{}, refusal
	}
	row := r.tx.QueryRow(ctx, selectInboxMessage, message.Consumer, message.MessageID)
	var found storage.Message
	if err := row.Scan(&found.Consumer, &found.MessageID, &found.BodyHash, &found.At); err != nil {
		return storage.Message{}, wrap("read recorded inbox message", err)
	}
	return found, refusal
}
