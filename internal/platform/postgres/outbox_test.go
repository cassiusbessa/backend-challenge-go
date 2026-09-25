package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

func TestInsert_writesTheRowWithNoTraceWhenNothingIsRecording(t *testing.T) {
	t.Parallel()
	recorder := &recordingTx{}
	if err := (outbox{tx: recorder}).Insert(context.Background(), processedEvent(t)); err != nil {
		t.Fatalf("Insert outside a span = %v, want nil", err)
	}
	if len(recorder.args) != 8 {
		t.Fatalf("arguments = %d, want 8", len(recorder.args))
	}
	// The arguments are typed pointers, so the absence is read through the type
	// and not by comparing the interface to nil.
	if traceID, spanID := recorder.args[5].(*string), recorder.args[6].(*string); traceID != nil || spanID != nil {
		t.Fatalf("trace and span = %v and %v, want both absent", traceID, spanID)
	}
}

func TestInsert_carriesTheCorrelationOfTheRequestIntoTheRowAndThePayload(t *testing.T) {
	t.Parallel()
	recorder := &recordingTx{}
	ctx := telemetry.WithCorrelation(context.Background(), "corr-1")
	if err := (outbox{tx: recorder}).Insert(ctx, processedEvent(t)); err != nil {
		t.Fatalf("Insert = %v, want nil", err)
	}
	column, ok := recorder.args[4].(*string)
	if !ok || *column != "corr-1" {
		t.Fatalf("correlation column = %v, want corr-1", recorder.args[4])
	}
	if got := correlationInPayload(t, recorder.args[3]); got != "corr-1" {
		t.Fatalf("correlation in the payload = %q, want corr-1", got)
	}
}

func correlationInPayload(t *testing.T, payload any) string {
	t.Helper()
	raw, ok := payload.([]byte)
	if !ok {
		t.Fatalf("payload = %T, want the marshalled bytes", payload)
	}
	var out struct {
		CorrelationID string `json:"correlationId"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal = %v, want nil", err)
	}
	return out.CorrelationID
}

// recordingTx keeps the arguments of the single Exec this repository makes, so a
// case reads what would have reached the driver.
type recordingTx struct {
	pgx.Tx
	args []any
}

func (r *recordingTx) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	r.args = args
	return pgconn.CommandTag{}, nil
}

func processedEvent(t *testing.T) event.Envelope {
	t.Helper()
	at := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	built, err := event.New(event.Spec{
		ID:          eventIdentity(t),
		AggregateID: walletIdentity(t),
		At:          at,
	}, event.BalanceChangedData{Direction: "CREDIT"})
	if err != nil {
		t.Fatalf("event.New = %v, want nil", err)
	}
	return built
}

func eventIdentity(t *testing.T) identity.EventID {
	t.Helper()
	parsed, err := identity.ParseEventID("019974a4-0000-7000-8000-00000000e001")
	if err != nil {
		t.Fatalf("ParseEventID = %v, want nil", err)
	}
	return parsed
}

// An envelope with nothing in it is a defect rather than a row: the insert
// refuses it with the operation that could not render it named in the chain.
func TestInsert_refusesAnEnvelopeThatCannotBeRendered(t *testing.T) {
	t.Parallel()
	err := (outbox{tx: &recordingTx{}}).Insert(context.Background(), event.Envelope{})
	if !errors.Is(err, event.ErrIncompleteEnvelope) {
		t.Fatalf("Insert of an empty envelope = %v, want ErrIncompleteEnvelope", err)
	}
	if !strings.Contains(err.Error(), "marshal outbox event") {
		t.Fatalf("failure = %v, want the operation that could not render it named in the chain", err)
	}
}
