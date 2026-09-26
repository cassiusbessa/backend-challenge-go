package divergencewatch

import (
	"context"
	"errors"
	"log/slog"

	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
)

// Reporter is the only place in this package that logs and moves a counter.
//
// Whoever decides the outcome reports it, and reports it once. The line of a
// divergence carries the wallet and the tokens, never a balance: it is the
// same line the route writes for the same verdict, from another origin.
type Reporter struct {
	log     *slog.Logger
	metrics *metrics.Settlement
}

func NewReporter(log *slog.Logger, series *metrics.Settlement) *Reporter {
	return &Reporter{log: log, metrics: series}
}

// Checked records one verdict. Every verdict counts as a wallet checked; a
// divergent one counts each token it found and leaves one line, and a
// consistent one leaves none: there is nothing to name, and a line per wallet
// per sweep would drown the ones that matter.
func (rep *Reporter) Checked(ctx context.Context, report reconcilewallet.Report) {
	rep.metrics.Checked(metrics.OriginWatch)
	if report.Consistent {
		return
	}
	tokens := make([]string, 0, len(report.Divergences))
	for _, token := range report.Divergences {
		rep.metrics.Diverged(metrics.OriginWatch, token)
		tokens = append(tokens, token.String())
	}
	rep.log.LogAttrs(ctx, slog.LevelWarn, "wallet reconciliation diverged",
		slog.String("walletId", report.WalletID.String()),
		slog.Any("divergences", tokens),
	)
}

// Failed records a failure of the turn: the chain that names where it came
// from, the frames of where it was first seen, and the wallet when the failure
// was about one.
//
// A turn the shutdown cut is not a failure. It is read off the error and not
// off the context: once Stop has cancelled, a context test would drop every
// failure that merely raced the signal, and that is the last one the process
// gets to report.
func (rep *Reporter) Failed(ctx context.Context, message string, err error, wallets ...identity.WalletID) {
	if errors.Is(err, context.Canceled) {
		return
	}
	attrs := []slog.Attr{
		slog.String("error", err.Error()),
		slog.Any("stack", stackOf(message, err)),
	}
	for _, id := range wallets {
		attrs = append(attrs, slog.String("walletId", id.String()))
	}
	rep.log.LogAttrs(ctx, slog.LevelError, message, attrs...)
}

// stackOf answers the frames of the failure, capturing them here when the chain
// carries none.
func stackOf(op string, err error) []string {
	if frames := fault.Stack(err); len(frames) > 0 {
		return frames
	}
	return fault.Stack(fault.Wrap(op, err))
}
