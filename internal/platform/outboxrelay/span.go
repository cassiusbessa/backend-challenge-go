package outboxrelay

import (
	"context"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/junglegaming/backend-challenge-go/internal/app/relayoutbox"
)

// Sending builds the span of one send out of the tracer of the process.
//
// The span of the send is linked to the trace of the commit that wrote the row
// rather than being a child of it: that span closed when the commit ended, and
// after a restart it does not exist in this process at all. A link survives
// both, and it is what takes a published event back to the operation it came
// from.
func Sending(tracer trace.Tracer) relayoutbox.Span {
	return func(ctx context.Context, traceID, spanID string) (context.Context, func(error)) {
		ctx, span := tracer.Start(ctx, "publish wallet event", trace.WithLinks(linksTo(traceID, spanID)...))
		return ctx, func(err error) {
			mark(span, err)
			span.End()
		}
	}
}

// mark records only what failed. A row that merely goes back on the backoff
// leaves the span ok: the broker being out is expected to come back, and a span
// in error on every retry would make an outage read as a defect.
func mark(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.RecordError(err, trace.WithStackTrace(true))
	span.SetStatus(codes.Error, "outbox turn failed")
}

// linksTo rebuilds the context of the commit from the two identifiers on the
// row. A row written outside any span carries none, and the send is then a span
// with no link rather than one with a broken one.
func linksTo(traceID, spanID string) []trace.Link {
	committed, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		return nil
	}
	within, err := trace.SpanIDFromHex(spanID)
	if err != nil {
		return nil
	}
	return []trace.Link{{SpanContext: trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: committed,
		SpanID:  within,
		// The commit was sampled when it was written, and the link stands for
		// that span, so it carries the same decision.
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})}}
}
