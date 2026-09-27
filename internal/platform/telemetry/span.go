package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// Step opens a span under the one ctx carries, and answers the context that
// carries it and the function that ends it with the outcome of the step.
//
// The span comes from the provider of its parent, so no tracer is handed to the
// use cases or to the unit of work, and a ctx with no span opens nothing: the
// provider of an absent span is the no-op one. A step belongs to an operation
// that has an entry — a request, a message — and a turn of a background
// component that found nothing to do is not one.
func Step(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(error)) {
	provider := trace.SpanFromContext(ctx).TracerProvider()
	ctx, span := provider.Tracer("wager").Start(ctx, name, trace.WithAttributes(attrs...))
	return ctx, func(err error) { end(span, err) }
}

// end closes the step, and marks it failed only for a failure that carries a
// stack: infrastructure and the version conflict. A rule that refused is an
// outcome and leaves it ok. The stack itself stays on the span of the entry,
// where the border records it: one failure, one stack.
func end(span trace.Span, err error) {
	if len(fault.Stack(err)) > 0 {
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}
