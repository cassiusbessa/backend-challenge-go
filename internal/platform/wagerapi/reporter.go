package wagerapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
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
//
// The refusals that reach it wrote no row: a body this border did not take, a
// client speaking for another provider, a transaction of someone else. A rule that
// refused the operation did write one, and Rejected is what names it.
func (rep *Reporter) Refuse(w http.ResponseWriter, r *http.Request, err error) {
	rep.answer(w, r, identity.TransactionID{}, err)
}

// Rejected answers a rule that refused the operation, naming the transaction the
// commit wrote for it.
//
// go-observability asks the rejection to log its row, and only the commit knows
// which one it is: the token says which rule refused, and the identifier is what
// joins the line to the transaction the provider can read back.
func (rep *Reporter) Rejected(w http.ResponseWriter, r *http.Request, id identity.TransactionID, err error) {
	rep.answer(w, r, id, err)
}

func (rep *Reporter) answer(w http.ResponseWriter, r *http.Request, id identity.TransactionID, err error) {
	details := problem.From(err)
	details.Detail = detailOf(err)
	rep.record(r, id, err, details)
	problem.Write(w, r, details)
}

// record marks the span and logs once.
//
// What decides it is the class and not the number: a rule that answered, a refusal
// of the contract and a request that can be retried shortly all leave the span ok,
// and a retryable answer shares its number with an outage, so the number alone
// cannot tell them apart.
func (rep *Reporter) record(r *http.Request, id identity.TransactionID, err error, details problem.Details) {
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(attribute.Int("http.response.status_code", details.Status))
	if !details.Broken() {
		rep.refused(r, id, details, nil)
		return
	}
	span.RecordError(err, trace.WithStackTrace(true))
	span.SetStatus(codes.Error, details.Title)
	rep.refused(r, id, details, stackOf(err))
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

func (rep *Reporter) refused(r *http.Request, id identity.TransactionID, details problem.Details, stack []string) {
	attrs := []slog.Attr{slog.String("status", strconv.Itoa(details.Status))}
	// A refusal that wrote no row has no transaction to name, and an empty
	// attribute would read as one whose value was lost.
	if !id.IsZero() {
		attrs = append(attrs, slog.String("transactionId", id.String()))
		span := trace.SpanFromContext(r.Context())
		span.SetAttributes(attribute.String("wager.transaction.id", id.String()))
	}
	if details.FailureCode != "" {
		attrs = append(attrs, slog.String("failureCode", details.FailureCode))
	}
	if len(stack) > 0 {
		attrs = append(attrs, slog.Any("stack", stack))
	}
	rep.log.LogAttrs(r.Context(), slog.LevelWarn, "wager request refused", attrs...)
}
