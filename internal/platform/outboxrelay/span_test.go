package outboxrelay

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const (
	committedTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	committedSpan  = "00f067aa0ba902b7"
)

func TestSending_linksTheSendToTheTraceOfTheCommit(t *testing.T) {
	t.Parallel()
	recorded := send(t, committedTrace, committedSpan, nil)
	if len(recorded.Links()) != 1 {
		t.Fatalf("links = %d, want the one of the commit", len(recorded.Links()))
	}
	if got := recorded.Links()[0].SpanContext.TraceID().String(); got != committedTrace {
		t.Fatalf("linked trace = %s, want %s", got, committedTrace)
	}
	if recorded.Parent().IsValid() {
		t.Fatalf("the send is a child of %s, want a link and no parent", recorded.Parent().TraceID())
	}
}

// A commit written outside any span carries no trace, and the send is then a
// span with no link rather than one with a broken one.
func TestSending_opensASpanWithNoLinkForARowThatCarriesNoTrace(t *testing.T) {
	t.Parallel()
	if recorded := send(t, "", "", nil); len(recorded.Links()) != 0 {
		t.Fatalf("links = %d, want none for a row with no trace", len(recorded.Links()))
	}
}

func TestSending_marksTheSpanOnlyForTheTurnThatFailed(t *testing.T) {
	t.Parallel()
	if got := send(t, committedTrace, committedSpan, nil).Status().Code; got != codes.Unset {
		t.Fatalf("status of a turn that went through = %v, want unset", got)
	}
	failed := send(t, committedTrace, committedSpan, errors.New("reschedule outbox row: connection reset by peer"))
	if failed.Status().Code != codes.Error {
		t.Fatalf("status of a turn that failed = %v, want an error", failed.Status().Code)
	}
	if len(failed.Events()) == 0 {
		t.Fatalf("the failed turn recorded no event on the span, want the failure on it")
	}
}

// send takes one turn through the span and answers what the exporter recorded.
func send(t *testing.T, traceID, spanID string, outcome error) sdktrace.ReadOnlySpan {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	_, end := Sending(provider.Tracer("test"))(context.Background(), traceID, spanID)
	end(outcome)
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("spans recorded = %d, want the one of the send", len(ended))
	}
	return ended[0]
}
