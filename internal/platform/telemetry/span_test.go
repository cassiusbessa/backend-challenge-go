package telemetry

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

func TestStep_opensAChildOfTheSpanTheContextCarries(t *testing.T) {
	t.Parallel()
	spans := tracetest.NewSpanRecorder()
	ctx, entry := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test").Start(context.Background(), "entry")
	stepped, done := Step(ctx, "unit of work", attribute.String("db.system.name", "postgresql"))
	done(nil)
	entry.End()
	step := endedFirst(t, spans.Ended(), "unit of work")
	if got, want := step.Parent().SpanID(), entry.SpanContext().SpanID(); got != want {
		t.Fatalf("parent of the step = %s, want the entry %s", got, want)
	}
	if got := trace.SpanContextFromContext(stepped); !got.Equal(step.SpanContext()) {
		t.Fatalf("span the returned context carries = %s, want the step %s", got.SpanID(), step.SpanContext().SpanID())
	}
	if got := step.Attributes(); len(got) != 1 || got[0].Value.AsString() != "postgresql" {
		t.Fatalf("attributes of the step = %v, want the one it was opened with", got)
	}
}

// endedFirst answers the span that ended first, and fails unless it is the step
// named and the entry ended after it.
func endedFirst(t *testing.T, ended []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	if len(ended) != 2 || ended[0].Name() != name {
		t.Fatalf("spans ended = %d, want %q and then the entry", len(ended), name)
	}
	return ended[0]
}

// A background turn carries no span, and the step it would open leaves no trace
// of its own: the provider of an absent span is the no-op one.
func TestStep_opensNothingWithoutASpanInTheContext(t *testing.T) {
	t.Parallel()
	stepped, done := Step(context.Background(), "unit of work")
	done(fault.Wrap("commit transaction", errors.New("connection reset")))
	if span := trace.SpanFromContext(stepped); span.IsRecording() || span.SpanContext().IsValid() {
		t.Fatalf("step without a parent recording = %t, valid = %t, want neither", span.IsRecording(), span.SpanContext().IsValid())
	}
}

// Only the failure that carries a stack marks the step: a rule that refused is
// an outcome, and so is a step that went through.
func TestEnd_marksTheStepFailedOnlyForAFailureWithAStack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{name: "a step that went through", err: nil, want: codes.Unset},
		{name: "a rule that refused", err: wager.NewRejection(wager.InsufficientFunds, nil), want: codes.Unset},
		{name: "a failure of infrastructure", err: fault.Wrap("commit transaction", errors.New("connection reset")), want: codes.Error},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spans := tracetest.NewSpanRecorder()
			_, span := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test").Start(context.Background(), "step")
			end(span, tc.err)
			ended := spans.Ended()
			if len(ended) != 1 {
				t.Fatalf("spans ended by end = %d, want 1", len(ended))
			}
			if got := ended[0].Status().Code; got != tc.want {
				t.Fatalf("status of the step = %v, want %v", got, tc.want)
			}
		})
	}
}
