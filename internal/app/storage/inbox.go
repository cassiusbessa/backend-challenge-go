package storage

import (
	"context"
	"errors"
	"time"
)

// ErrMessageRecorded is the unicity of consumer and message refusing a second
// row: this consumer has already recorded that message.
//
// It is a refusal of the contract and not a business rejection, the same as the
// two indexes of idempotency: the closed catalog of failure codes names wager
// operations, and a redelivery is not one. The row that won is answered beside
// it, so the caller compares the hash instead of asking again.
var ErrMessageRecorded = errors.New("storage: consumer has already recorded this message")

// Message is one received message as the inbox records it: which consumer took
// it, which message it was, and the hash of the body that arrived.
//
// The hash is what tells a legitimate redelivery from the same identifier
// re-presented with another body. The zero value records nothing: every field is
// required, and the CHECK of the schema refuses an empty one.
type Message struct {
	Consumer  string
	MessageID string
	BodyHash  string
	At        time.Time
}

// Inbox records the message that caused the commit.
//
// Only the insert is here, and there is no read beside it: what the caller needs
// of a message already recorded is answered by the insert that was refused, so
// no path can ask about the inbox before deciding and take the answer of a query
// two replicas could both pass.
type Inbox interface {
	// Insert records the message in the transaction that decided the operation,
	// so the movement and the memory of the message live or die together.
	//
	// The row already there is answered together with ErrMessageRecorded when the
	// unicity refuses this one, and the caller compares its hash: the same hash
	// is the redelivery of a message already settled, and another hash is the
	// same identifier re-presented with a different body.
	Insert(ctx context.Context, message Message) (Message, error)
}
