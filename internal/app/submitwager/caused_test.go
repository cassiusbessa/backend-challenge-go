package submitwager

import (
	"context"
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// The message that caused the operation, as the consumer hands it over. The hash
// is of the body that arrived, which is what tells a redelivery of the same bytes
// from the same identifier re-presented with another content.
func caused() Caused {
	return Caused{Consumer: "wager-ingress", MessageID: "message-1", BodyHash: "hash-of-the-body"}
}

func TestSubmitCaused_recordsTheMessageInTheCommitOfTheMovement(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	result, err := service(t, book).SubmitCaused(context.Background(), commandOf(t, wager.KindBet, "25.00"), caused())
	if err != nil {
		t.Fatalf("SubmitCaused = %v, want nil", err)
	}
	if result.Status != wager.Processed {
		t.Fatalf("status = %s, want PROCESSED", result.Status)
	}
	assertRedelivered(t, result, false)
	assertRows(t, book, 1, 1)
	if len(book.messages) != 1 {
		t.Fatalf("inbox rows = %d, want 1", len(book.messages))
	}
	if book.messages[0].BodyHash != caused().BodyHash {
		t.Fatalf("recorded hash = %q, want %q", book.messages[0].BodyHash, caused().BodyHash)
	}
	// The row takes the instant of the operation it caused: one commit, one
	// instant.
	if !book.messages[0].At.Equal(frozen) {
		t.Fatalf("recorded instant = %s, want the instant of the operation %s", book.messages[0].At, frozen)
	}
}

// A message this consumer already recorded with the same body is not applied
// again. What answers it is the transaction under the idempotency key, which the
// first delivery committed.
func TestSubmitCaused_answersTheRecordedOutcomeOfARedeliveryAndAppliesNothing(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	cmd := commandOf(t, wager.KindBet, "25.00")
	first, err := service(t, book).SubmitCaused(context.Background(), cmd, caused())
	if err != nil {
		t.Fatalf("first delivery = %v, want nil", err)
	}
	book.recorded = map[string]storage.Message{
		"wager-ingress|message-1": {Consumer: "wager-ingress", MessageID: "message-1", BodyHash: caused().BodyHash},
	}
	again, err := service(t, book).SubmitCaused(context.Background(), cmd, caused())
	if err != nil {
		t.Fatalf("redelivery = %v, want nil", err)
	}
	if !again.IdempotentReplay {
		t.Fatalf("replay = %t, want true on a redelivery", again.IdempotentReplay)
	}
	assertRedelivered(t, again, true)
	if again.TransactionID != first.TransactionID {
		t.Fatalf("transaction = %s, want the one of the first delivery %s", again.TransactionID, first.TransactionID)
	}
	assertRows(t, book, 1, 1)
	if len(book.messages) != 1 {
		t.Fatalf("inbox rows = %d, want the single one of the first delivery", len(book.messages))
	}
	assertBalance(t, book, 97500, 2)
}

// assertRedelivered reads the marker of the inbox on the result: true only when
// the inbox already held the message that caused the arrival.
func assertRedelivered(t *testing.T, result Result, want bool) {
	t.Helper()
	if result.Redelivered != want {
		t.Fatalf("redelivered = %t, want %t", result.Redelivered, want)
	}
}

// The same identifier with another body is not a redelivery of anything: it is
// refused, nothing is written, and the same bytes would be refused the same way,
// so whoever received it takes it out of the queue.
func TestSubmitCaused_refusesARecordedIdentifierThatArrivesWithAnotherBody(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	book.recorded = map[string]storage.Message{
		"wager-ingress|message-1": {Consumer: "wager-ingress", MessageID: "message-1", BodyHash: "hash-of-another-body"},
	}
	_, err := service(t, book).SubmitCaused(context.Background(), commandOf(t, wager.KindBet, "25.00"), caused())
	if !errors.Is(err, ErrMessageBodyDiffers) {
		t.Fatalf("SubmitCaused = %v, want %v", err, ErrMessageBodyDiffers)
	}
	assertNothingWritten(t, book)
	if book.rollbacks != 1 {
		t.Fatalf("rollbacks = %d, want 1", book.rollbacks)
	}
}

// An operation that came over HTTP records nothing: there is no message to
// remember, and the inbox is not reached at all.
func TestSubmit_recordsNoMessageForAnOperationThatCameOverHTTP(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	submit(t, book, commandOf(t, wager.KindBet, "25.00"))
	if len(book.messages) != 0 {
		t.Fatalf("inbox rows = %d, want 0 for an arrival over HTTP", len(book.messages))
	}
}

// The refusal of the inbox takes the movement down with it, which is the whole
// point of the row sharing the commit.
func TestSubmitCaused_takesTheMovementDownWithAFailureOfTheInbox(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	broken := errors.New("insert inbox message: interrupted")
	book.messageErr = broken
	_, err := service(t, book).SubmitCaused(context.Background(), commandOf(t, wager.KindBet, "25.00"), caused())
	if !errors.Is(err, broken) {
		t.Fatalf("SubmitCaused over a refusing inbox = %v, want %v", err, broken)
	}
	assertNothingWritten(t, book)
}
