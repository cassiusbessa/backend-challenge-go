package outboxrelay

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
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
	cases := map[string][2]string{
		"a row with no trace at all":       {"", ""},
		"a row whose span is not readable": {committedTrace, "not-a-span"},
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			if recorded := send(t, ids[0], ids[1], nil); len(recorded.Links()) != 0 {
				t.Fatalf("links of %s = %d, want none", name, len(recorded.Links()))
			}
		})
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
	if got := len(failed.Events()); got == 0 {
		t.Fatalf("events on the span of a failed turn = %d, want the failure recorded on it", got)
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

// The status the relay reports maps to the series: a row sent back is a retry
// under the reason the broker gave, a row given up on is a dead event, and a
// send that went through or a lease that moved on move nothing.
func TestCounting_mapsEachStatusOfTheRelayToItsSeries(t *testing.T) {
	t.Parallel()
	moved := metrics.New(prometheus.NewRegistry())
	count := Counting(moved)
	for _, status := range []string{"published", "retried", "retried", "refused", "dead", "lost"} {
		count(status)
	}
	if got := testutil.ToFloat64(moved.Retries.WithLabelValues("outbox", "transient")); got != 2 {
		t.Fatalf("retries{outbox,transient} = %v, want the 2 rows sent back on the backoff", got)
	}
	if got := testutil.ToFloat64(moved.Retries.WithLabelValues("outbox", "refused")); got != 1 {
		t.Fatalf("retries{outbox,refused} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(moved.OutboxDead); got != 1 {
		t.Fatalf("dead events = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(moved.Retries); got != 2 {
		t.Fatalf("retry series = %d, want only the two reasons: published and lost move nothing", got)
	}
}
