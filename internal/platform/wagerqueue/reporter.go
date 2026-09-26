package wagerqueue

import (
	"context"
	"errors"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/authz"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

// Reporter is the only place in this package that logs, marks the span and moves
// a counter.
//
// Whoever decides the outcome reports it, and reports it once. No line here
// carries a body, an amount, a balance, an idempotency key, a token or a
// credential: what it says is which message it was, what happened to it, and why.
type Reporter struct {
	log       *slog.Logger
	tracer    trace.Tracer
	abandoned *prometheus.CounterVec
	depth     prometheus.Gauge
}

func NewReporter(log *slog.Logger, tracer trace.Tracer, metrics *Metrics) *Reporter {
	return &Reporter{log: log, tracer: tracer, abandoned: metrics.Abandoned, depth: metrics.DeadLetterDepth}
}

// Metrics is what the ingress adds to the registry of the process.
//
// Neither series carries a wallet or a provider in a label: go-observability keeps
// both out, and the reason of an abandonment is a closed set of tokens, so the
// cardinality is bounded by the code and not by the traffic.
type Metrics struct {
	Abandoned       *prometheus.CounterVec
	DeadLetterDepth prometheus.Gauge
}

// NewMetrics registers the two series of the ingress on the registry of the
// process.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	abandoned := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wager_ingress_messages_abandoned_total",
		Help: "Messages copied to the dead-letter queue, by reason.",
	}, []string{"reason"})
	depth := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "wager_ingress_dead_letter_depth",
		Help: "Messages waiting in the dead-letter queue.",
	})
	reg.MustRegister(abandoned, depth)
	return &Metrics{Abandoned: abandoned, DeadLetterDepth: depth}
}

// propagator reads the trace the sender put in the attributes of the message. It
// is the W3C format the HTTP border already uses: the channel changes and the
// shape of the propagation does not.
var propagator = propagation.TraceContext{}

// Receiving opens the span of one message and carries its identity down the call.
//
// The correlation is the one the envelope brought, and a message that brought none
// is correlated by its own identity: an operation of the queue always has a
// correlation, and the identity of the message is the one value that is always
// there. That identity also travels as the cause, so every event the commit writes
// names the message that caused it.
//
// The span continues the trace the sender propagated when there is one, rather than
// opening a new one cut off from the origin.
func (rep *Reporter) Receiving(ctx context.Context, delivery Delivery, decoded Message) (context.Context, func(error)) {
	message := identityOf(delivery, decoded)
	ctx = telemetry.WithCorrelation(ctx, correlationOf(decoded, message))
	ctx = telemetry.WithCausation(ctx, message)
	ctx = propagator.Extract(ctx, propagation.MapCarrier(delivery.Trace))
	ctx, span := rep.tracer.Start(ctx, "receive wager message", trace.WithSpanKind(trace.SpanKindConsumer))
	span.SetAttributes(attribute.String("messaging.message.id", message))
	return ctx, func(err error) {
		rep.mark(span, err)
		span.End()
	}
}

// identityOf answers the identity of the message: the one of the envelope, and the
// deduplication the broker registered for a body this border could not read.
//
// go-sqs-ingress fixes the deduplication as the identity of the envelope, so the
// fallback names the same message the envelope would have — which is what lets a
// body nobody could decode still be reported by name.
func identityOf(delivery Delivery, decoded Message) string {
	if decoded.MessageID != "" {
		return decoded.MessageID
	}
	return delivery.Deduplication
}

func correlationOf(decoded Message, message string) string {
	if decoded.CorrelationID != "" {
		return decoded.CorrelationID
	}
	return message
}

// mark records only what failed. A rule refusing the operation leaves the span ok:
// go-observability reserves the error of a span for infrastructure, a version
// conflict and the dead-letter queue, and a rejection is none of the three.
func (rep *Reporter) mark(span trace.Span, err error) {
	if _, refused := rejectionOf(err); err == nil || refused {
		return
	}
	span.RecordError(err, trace.WithStackTrace(true))
	span.SetStatus(codes.Error, "message of the ingress queue failed")
}

// rejectionOf answers the rule that refused the operation, and reports whether one
// did. A refusal is an outcome and not a defect, and both the span and the line
// need the token, so the chain is read once and the value travels.
func rejectionOf(err error) (wager.Rejection, bool) {
	var refusal wager.Rejection
	found := errors.As(err, &refusal)
	return refusal, found
}

// Settled records the outcome of a message that reached a decision, including a
// redelivery that reapplied nothing.
func (rep *Reporter) Settled(ctx context.Context, result submitwager.Result) {
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.String("wager.transaction.id", result.TransactionID.String()),
		attribute.String("wager.status", result.Status.String()),
	)
	rep.log.LogAttrs(ctx, slog.LevelInfo, "message of the ingress queue settled",
		append(rep.named(ctx),
			slog.String("transactionId", result.TransactionID.String()),
			slog.String("kind", result.Kind.String()),
			slog.String("status", result.Status.String()),
		)...)
}

// Rejected records a message a rule refused, which is a settled outcome with a
// durable row of its own and not a failure of this consumer.
//
// go-observability asks every rejection to log, and the token is what it logs:
// the line the border would answer over HTTP carries the same failureCode, so the
// two origins of one rule read alike. The span stays ok, because a rejection is
// none of the three cases that mark one.
func (rep *Reporter) Rejected(ctx context.Context, result submitwager.Result, refusal wager.Rejection) {
	code := refusal.Code().String()
	transaction := result.TransactionID.String()
	trace.SpanFromContext(ctx).SetAttributes(
		attribute.String("wager.transaction.id", transaction),
		attribute.String("wager.status", wager.Rejected.String()),
		attribute.String("wager.failure_code", code),
	)
	rep.log.LogAttrs(ctx, slog.LevelWarn, "message of the ingress queue rejected",
		append(rep.named(ctx),
			slog.String("transactionId", transaction),
			slog.String("kind", result.Kind.String()),
			slog.String("status", wager.Rejected.String()),
			slog.String("failureCode", code),
		)...)
}

// Abandoned records a message on its way to the dead-letter queue, whatever the
// reason, and counts it by that reason.
//
// A refusal by sender carries the observed identity, because that value is what
// corrects the configuration: a map naming the wrong one sends every legitimate
// message here, and the line is the only place the true value can be read from.
func (rep *Reporter) Abandoned(ctx context.Context, delivery Delivery, reason string, err error) {
	rep.abandoned.WithLabelValues(reason).Inc()
	// Every one of the four doors to the dead-letter queue names a refusal of its
	// own, the delivery limit included, so the chain is always there to log.
	attrs := append(rep.named(ctx),
		slog.String("reason", reason),
		slog.String("error", err.Error()),
	)
	if errors.Is(err, authz.ErrUnmappedSender) || errors.Is(err, authz.ErrProviderNotAllowed) {
		attrs = append(attrs, slog.String("sender", delivery.Sender))
	}
	rep.log.LogAttrs(ctx, slog.LevelWarn, "message abandoned to the dead-letter queue", attrs...)
}

// Failed records a failure of this consumer: the chain that names where it came
// from, and the frames of where it was first seen.
//
// A turn the shutdown cut is not a failure. It is read off the error and not off
// the context, because once the deadline has cancelled the work a context test
// would drop every failure that merely raced it, and that is the last one the
// process gets to report.
func (rep *Reporter) Failed(ctx context.Context, message string, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	rep.log.LogAttrs(ctx, slog.LevelError, message,
		append(rep.named(ctx),
			slog.String("error", err.Error()),
			slog.Any("stack", stackOf(message, err)),
		)...)
}

// Depth records how many messages the dead-letter queue is holding.
func (rep *Reporter) Depth(messages int64) {
	rep.depth.Set(float64(messages))
}

// named is what every line of one operation carries: the identity of the message,
// the correlation the operation was given, and the trace the line belongs to.
//
// A line outside any message — the poll itself failing — carries none of the
// three: there is no operation to name, and an empty attribute would read as one
// whose value was lost.
func (rep *Reporter) named(ctx context.Context) []slog.Attr {
	message := telemetry.Causation(ctx)
	if message == "" {
		return nil
	}
	sc := trace.SpanContextFromContext(ctx)
	return []slog.Attr{
		slog.String("messageId", message),
		slog.String("correlationId", telemetry.Correlation(ctx)),
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
	}
}

// stackOf answers the frames of the failure, capturing them here when the chain
// carries none.
func stackOf(op string, err error) []string {
	if frames := fault.Stack(err); len(frames) > 0 {
		return frames
	}
	return fault.Stack(fault.Wrap(op, err))
}
