package postgres

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// A unit of work over a pool the process never opened refuses before it opens a
// transaction, and it refuses as a failure of infrastructure: the work never runs,
// so there is no rejection of a rule to confuse it with.
func TestWithin_refusesBeforeTheWorkWhenThePoolIsNotOpen(t *testing.T) {
	t.Parallel()
	ran := false
	err := NewUnitOfWork(&Pool{}).Within(context.Background(), func(storage.Tx) error {
		ran = true
		return nil
	})
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Within over a closed pool = %v, want ErrPoolClosed in the chain", err)
	}
	if ran {
		t.Fatalf("the work ran = true, want false: a pool that is not open decides nothing")
	}
	if frames := fault.Stack(err); len(frames) == 0 {
		t.Fatalf("frames of the refusal = 0, want the stack of an infrastructure failure")
	}
}

// The unit of work is a span of its own under the operation, closed after the
// commit or the rollback, and a failure of infrastructure marks it.
func TestWithin_closesItsSpanUnderTheOperationAndMarksAFailure(t *testing.T) {
	t.Parallel()
	spans := tracetest.NewSpanRecorder()
	ctx, operation := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test").Start(context.Background(), "submit wager")
	err := NewUnitOfWork(&Pool{}).Within(ctx, func(storage.Tx) error { return nil })
	operation.End()
	ended := spans.Ended()
	if len(ended) != 2 || ended[0].Name() != "unit of work" {
		t.Fatalf("spans ended = %d, want the unit of work and then the operation", len(ended))
	}
	if got, want := ended[0].Parent().SpanID(), operation.SpanContext().SpanID(); got != want {
		t.Fatalf("parent of the unit of work = %s, want the operation %s", got, want)
	}
	if got := ended[0].Status().Code; got != codes.Error || err == nil {
		t.Fatalf("status of a unit of work over a closed pool = %v with %v, want an error", got, err)
	}
}
