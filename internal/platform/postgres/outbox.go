package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/trace"

	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

const insertOutboxEvent = `
INSERT INTO outbox_events (
    event_id, event_type, wallet_id, payload, correlation_id, trace_id, span_id,
    created_at, next_attempt_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())`

// outbox writes the event rows of one open transaction. There is no update and
// no delete here: what the relay writes afterwards belongs to ports of its own,
// in transactions of their own.
type outbox struct {
	tx pgx.Tx
}

// Insert records the event beside the balance it came from, with the origin of
// the operation and the trace of the commit that wrote it. The row is publishable
// from the instant it is committed, so the first attempt is due at once.
//
// The origin is read off the context and not off the event: the correlation and
// the causing message are decided at the border, and the settlement knows
// neither. A commit no message caused omits the cause rather than carrying an
// empty one.
//
// The instant of that first attempt is the clock of the database and not the
// one of the process: the scan compares it against now(), and two clocks in one
// comparison make a row that is due here and not there.
//
// A commit outside any span still writes the row: the trace of an event is what
// links it back, not what makes it valid.
func (r outbox) Insert(ctx context.Context, envelope event.Envelope) error {
	origin := event.Origin{
		CorrelationID: telemetry.Correlation(ctx),
		CausationID:   telemetry.Causation(ctx),
	}
	payload, err := envelope.Marshal(origin)
	if err != nil {
		return wrap("marshal outbox event", err)
	}
	traceID, spanID := traceOf(ctx)
	_, err = r.tx.Exec(ctx, insertOutboxEvent,
		envelope.ID().String(),
		string(envelope.Type()),
		envelope.AggregateID().String(),
		payload,
		nullable(origin.CorrelationID),
		traceID,
		spanID,
		envelope.OccurredAt(),
	)
	return wrap("insert outbox event", err)
}

// traceOf answers the trace and the span of the commit, or two absent values
// when nothing is recording. The publisher links its own span to them, which it
// can only do for a commit that had one.
func traceOf(ctx context.Context) (*string, *string) {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return nil, nil
	}
	traceID, spanID := sc.TraceID().String(), sc.SpanID().String()
	return &traceID, &spanID
}

// nullable keeps an absent value out of the column as NULL rather than as the
// empty string, so a query can tell "nothing was carried" from "an empty token
// was carried".
func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
