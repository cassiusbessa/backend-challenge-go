package wagerqueue

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
)

// A rule refusing the operation leaves the span ok: go-observability reserves the
// error of a span for infrastructure, a version conflict and the dead-letter queue,
// and a rejection is none of the three.
func TestReceiving_leavesTheSpanOkForARuleThatRefusedTheOperation(t *testing.T) {
	t.Parallel()
	reporter, spans := reporterOver(t)
	_, closeSpan := reporter.Receiving(context.Background(), arrived(1), decodedOf(t))
	closeSpan(wager.NewRejection(wager.InsufficientFunds, nil))
	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("spans = %d, want 1", len(ended))
	}
	if got := ended[0].Status().Code; got != codes.Unset {
		t.Fatalf("span status = %v, want it unset", got)
	}
}

func TestReceiving_marksTheSpanOfAFailureOfInfrastructure(t *testing.T) {
	t.Parallel()
	reporter, spans := reporterOver(t)
	_, closeSpan := reporter.Receiving(context.Background(), arrived(1), decodedOf(t))
	closeSpan(fault.Wrap("acquire connection", errors.New("connection refused")))
	ended := spans.Ended()
	if got := ended[0].Status().Code; got != codes.Error {
		t.Fatalf("span status = %v, want an error", got)
	}
	if len(ended[0].Events()) == 0 {
		t.Fatalf("span events = 0, want the failure recorded on it")
	}
}

// A turn the shutdown cut is not a failure, and it is read off the error rather
// than off the context: once the deadline has cancelled the work, a context test
// would drop every failure that merely raced it.
func TestFailed_writesNoLineForATurnTheShutdownCut(t *testing.T) {
	t.Parallel()
	reporter, logs := loggingReporter(t)
	reporter.Failed(context.Background(), "settle a message of the ingress queue", context.Canceled)
	reporter.Failed(context.Background(), "settle a message of the ingress queue", nil)
	if logs.Len() != 0 {
		t.Fatalf("lines written = %q, want none", logs.String())
	}
}

// The frames come from the chain when it already carries them, so one failure keeps
// one stack rather than gaining a second capture at this border.
func TestFailed_keepsTheFramesTheChainAlreadyCarries(t *testing.T) {
	t.Parallel()
	reporter, logs := loggingReporter(t)
	captured := fault.Wrap("acquire connection", errors.New("connection refused"))
	reporter.Failed(context.Background(), "settle a message of the ingress queue", captured)
	line := lineWith(t, logs, "settle a message")
	frames, carried := line["stack"].([]any)
	if !carried || len(frames) == 0 {
		t.Fatalf("stack = %v, want the frames of the chain", line["stack"])
	}
	if len(frames) != len(fault.Stack(captured)) {
		t.Fatalf("frames = %d, want the %d of the chain", len(frames), len(fault.Stack(captured)))
	}
}

// A failure that crossed no boundary has no frames in its chain, and this border is
// where it is first seen as one.
func TestFailed_capturesTheFramesOfAFailureThatCarriesNone(t *testing.T) {
	t.Parallel()
	reporter, logs := loggingReporter(t)
	reporter.Failed(context.Background(), "release a message of the ingress queue", errors.New("connection reset"))
	line := lineWith(t, logs, "release a message")
	if frames, carried := line["stack"].([]any); !carried || len(frames) == 0 {
		t.Fatalf("stack = %v, want frames captured here", line["stack"])
	}
}

// A line outside any message carries no identity: the poll failing is not about an
// operation, and an empty attribute would read as one whose value was lost.
func TestFailed_namesNoMessageForALineOutsideAnyOperation(t *testing.T) {
	t.Parallel()
	reporter, logs := loggingReporter(t)
	reporter.Failed(context.Background(), "receive from the ingress queue", errors.New("connection refused"))
	line := lineWith(t, logs, "receive from the ingress queue")
	if _, named := line["messageId"]; named {
		t.Fatalf("line carries a messageId, want none: %v", line)
	}
}

// The identity of a body nobody could decode is the deduplication the broker
// registered, which go-sqs-ingress fixes as the identity of the envelope.
func TestIdentityOf_fallsBackToTheDeduplicationOfTheBroker(t *testing.T) {
	t.Parallel()
	if got := identityOf(arrived(1), Message{}); got != deduplicationID {
		t.Fatalf("identity of an undecoded body = %q, want %q", got, deduplicationID)
	}
	if got := identityOf(arrived(1), Message{MessageID: "message-of-the-envelope"}); got != "message-of-the-envelope" {
		t.Fatalf("identity = %q, want the one of the envelope", got)
	}
}

func decodedOf(t *testing.T) Message {
	t.Helper()
	decoded, err := Decode(message(nil))
	if err != nil {
		t.Fatalf("Decode = %v, want nil", err)
	}
	return decoded
}

// reporterOver builds a reporter whose spans are kept, for the cases about the
// span of one message.
func reporterOver(t *testing.T) (*Reporter, *tracetest.SpanRecorder) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test")
	return NewReporter(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), tracer, metrics.New(prometheus.NewRegistry())), spans
}

// loggingReporter builds a reporter whose lines are kept, for the cases about what
// is written.
func loggingReporter(t *testing.T) (*Reporter, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	reporter := NewReporter(slog.New(slog.NewJSONHandler(logs, nil)), quietTracer(), metrics.New(prometheus.NewRegistry()))
	return reporter, logs
}
