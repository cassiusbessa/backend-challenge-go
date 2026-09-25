package wagerapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

// Reporter is the only place in this package that logs and marks the span.
//
// Whoever decides the outcome logs it, and logs it once. No line here carries an
// amount, a balance, a body, a token or the idempotency key: the log of a
// settlement is identifiers and the outcome.
type Reporter struct {
	log *slog.Logger
}

func NewReporter(log *slog.Logger) *Reporter {
	return &Reporter{log: log}
}

// Settled records the outcome of a submission that reached a decision, including a
// replay of one already recorded.
func (rep *Reporter) Settled(r *http.Request, settled submitwager.Result) {
	trace.SpanFromContext(r.Context()).SetAttributes(
		attribute.String("wager.transaction.id", settled.TransactionID.String()),
		attribute.String("wager.kind", settled.Kind.String()),
		attribute.String("wager.status", settled.Status.String()),
	)
	rep.log.LogAttrs(r.Context(), slog.LevelInfo, "wager settled",
		slog.String("transactionId", settled.TransactionID.String()),
		slog.String("kind", settled.Kind.String()),
		slog.String("status", settled.Status.String()),
	)
}

// Refuse classifies the failure, answers problem details and records the outcome.
func (rep *Reporter) Refuse(w http.ResponseWriter, r *http.Request, err error) {
	details := problem.From(err)
	details.Detail = detailOf(err)
	rep.record(r, err, details)
	problem.Write(w, r, details)
}

// record marks the span and logs once.
//
// What decides it is the class and not the number: a rule that answered, a refusal
// of the contract and a request that can be retried shortly all leave the span ok,
// and a retryable answer shares its number with an outage, so the number alone
// cannot tell them apart.
func (rep *Reporter) record(r *http.Request, err error, details problem.Details) {
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(attribute.Int("http.response.status_code", details.Status))
	if !details.Broken() {
		rep.refused(r, details, nil)
		return
	}
	span.RecordError(err, trace.WithStackTrace(true))
	span.SetStatus(codes.Error, details.Title)
	rep.refused(r, details, stackOf(err))
}

// stackOf answers the frames of the failure, capturing them here when the chain
// carries none.
//
// A defect that never crossed an I/O boundary has no fault in its chain, and this
// border is where it is first seen as a failure: go-errors puts the capture where
// the failure is first seen, and one failure keeps one stack.
func stackOf(err error) []string {
	if frames := fault.Stack(err); len(frames) > 0 {
		return frames
	}
	return fault.Stack(fault.Wrap("answer wager request", err))
}

func (rep *Reporter) refused(r *http.Request, details problem.Details, stack []string) {
	attrs := []slog.Attr{slog.String("status", strconv.Itoa(details.Status))}
	if details.FailureCode != "" {
		attrs = append(attrs, slog.String("failureCode", details.FailureCode))
	}
	if len(stack) > 0 {
		attrs = append(attrs, slog.Any("stack", stack))
	}
	rep.log.LogAttrs(r.Context(), slog.LevelWarn, "wager request refused", attrs...)
}
