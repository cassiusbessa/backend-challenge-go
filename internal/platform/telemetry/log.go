package telemetry

import (
	"context"
	"log/slog"
	"strings"
)

func CorrelationID(header, traceID string) string {
	if validCorrelation(header) {
		return header
	}
	return traceID
}

func validCorrelation(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	return onlyTokenRunes(value)
}

func onlyTokenRunes(value string) bool {
	for _, r := range value {
		if !strings.ContainsRune(tokenAlphabet, r) {
			return false
		}
	}
	return true
}

const tokenAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._:-"

func Allow(next slog.Handler) slog.Handler {
	return allowHandler{next: next}
}

var allowedKeys = map[string]struct{}{
	"correlationId": {},
	"trace_id":      {},
	"span_id":       {},
	"messageId":     {},
	"eventId":       {},
	"transactionId": {},
	"walletId":      {},
	"providerId":    {},
	"kind":          {},
	"status":        {},
	"failureCode":   {},
	// The stack of an infrastructure failure, captured once. A business
	// rejection never carries one.
	"stack": {},
}

type allowHandler struct {
	next slog.Handler
}

func (h allowHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h allowHandler) Handle(ctx context.Context, rec slog.Record) error {
	filtered := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	rec.Attrs(func(attr slog.Attr) bool {
		keep(&filtered, attr)
		return true
	})
	return h.next.Handle(ctx, filtered)
}

func allowed(key string) bool {
	_, ok := allowedKeys[key]
	return ok
}

func keep(rec *slog.Record, attr slog.Attr) {
	if !allowed(attr.Key) {
		return
	}
	rec.AddAttrs(attr)
}

func (h allowHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return allowHandler{next: h.next.WithAttrs(filterAttrs(attrs))}
}

func filterAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		if allowed(attr.Key) {
			out = append(out, attr)
		}
	}
	return out
}

func (h allowHandler) WithGroup(string) slog.Handler {
	return h
}

// correlationKey is the context key the correlation of one operation travels
// under. It is a type of its own so that no other package can collide with it.
type correlationKey struct{}

// WithCorrelation carries the correlation of one operation down the call. The
// border decides it once — from the header of the request or from the trace —
// and everything the operation writes reads it back from here instead of
// deciding it again.
func WithCorrelation(ctx context.Context, correlation string) context.Context {
	return context.WithValue(ctx, correlationKey{}, correlation)
}

// Correlation answers the correlation carried by the context, or the empty
// string when nothing put one there. The absence is not a failure: work that
// no border started has no request to correlate with.
func Correlation(ctx context.Context) string {
	correlation, _ := ctx.Value(correlationKey{}).(string)
	return correlation
}
