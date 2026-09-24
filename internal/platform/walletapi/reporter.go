package walletapi

import (
	"log/slog"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

// Reporter is the only place in this package that logs and marks the span.
//
// Whoever decides the outcome logs it, and logs it once: a handler that both
// logged and returned would turn one refusal into several lines, and the search
// would then count it several times.
type Reporter struct {
	log *slog.Logger
}

func NewReporter(log *slog.Logger) *Reporter {
	return &Reporter{log: log}
}

// Opened records the success. A successful line carries identifiers only — no
// amount and no balance.
func (rep *Reporter) Opened(r *http.Request, id identity.WalletID) {
	trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("wallet.id", id.String()))
	rep.log.LogAttrs(r.Context(), slog.LevelInfo, "wallet opened",
		slog.String("walletId", id.String()),
	)
}

// Refuse classifies the failure, answers problem details and records the
// outcome.
func (rep *Reporter) Refuse(w http.ResponseWriter, r *http.Request, err error) {
	details := problem.From(err)
	details.Detail = detailOf(err)
	rep.record(r, err, details)
	problem.Write(w, r, details)
}

// record marks the span and logs once.
//
// A business rejection and a refusal of the contract leave the span ok: the rule
// answered, and nothing is broken. Only an infrastructure failure marks the span
// and carries the stack.
func (rep *Reporter) record(r *http.Request, err error, details problem.Details) {
	span := trace.SpanFromContext(r.Context())
	span.SetAttributes(attribute.Int("http.response.status_code", details.Status))
	if details.Status < http.StatusInternalServerError {
		rep.refused(r, details, nil)
		return
	}
	span.RecordError(err, trace.WithStackTrace(true))
	span.SetStatus(codes.Error, "unavailable")
	rep.refused(r, details, fault.Stack(err))
}

func (rep *Reporter) refused(r *http.Request, details problem.Details, stack []string) {
	attrs := []slog.Attr{slog.String("status", strconv.Itoa(details.Status))}
	if details.FailureCode != "" {
		attrs = append(attrs, slog.String("failureCode", details.FailureCode))
	}
	if len(stack) > 0 {
		attrs = append(attrs, slog.Any("stack", stack))
	}
	rep.log.LogAttrs(r.Context(), slog.LevelWarn, "wallet request refused", attrs...)
}
