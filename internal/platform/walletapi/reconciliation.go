package walletapi

import (
	"context"
	"net/http"

	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

// Reconciler is the reconciliation of one wallet, as the border needs it.
type Reconciler interface {
	Reconcile(ctx context.Context, id identity.WalletID) (reconcilewallet.Report, error)
}

// reconciliationResponse is what the reconciliation route answers, under the
// names of the challenge statement. Divergences and FirstBreakSequence are left
// out of a consistent wallet: a field that is absent says there is nothing to
// name, and an empty list would say the same thing twice.
type reconciliationResponse struct {
	WalletID           string      `json:"walletId"`
	StoredBalance      money.Money `json:"storedBalance"`
	CalculatedBalance  money.Money `json:"calculatedBalance"`
	Difference         money.Money `json:"difference"`
	Version            int64       `json:"version"`
	CheckedEntries     int64       `json:"checkedEntries"`
	LastSequence       int64       `json:"lastSequence"`
	Consistent         bool        `json:"consistent"`
	Divergences        []string    `json:"divergences,omitempty"`
	FirstBreakSequence int64       `json:"firstBreakSequence,omitempty"`
}

// Reconcile serves POST /wallets/{walletId}/reconciliation. The verb is the one
// the challenge statement names, and the effect is still a read's: it takes no
// body, so one that is sent changes nothing, and it never corrects. A wallet that
// does not exist answers 404. A divergence is a result the read reports, not a
// failure of the service: it is logged, and the span stays ok.
func Reconcile(reconciler Reconciler, reporter *Reporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := decodeWalletID(r)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		ctx, done := telemetry.Step(r.Context(), "reconcile wallet")
		report, err := reconciler.Reconcile(ctx, id)
		done(err)
		if err != nil {
			reporter.Unreconciled(w, r, err)
			return
		}
		reporter.Diverged(r, report)
		write(w, http.StatusOK, reconciliationResponse{
			WalletID:           report.WalletID.String(),
			StoredBalance:      report.StoredBalance,
			CalculatedBalance:  report.LedgerBalance,
			Difference:         report.Difference,
			Version:            report.Version,
			CheckedEntries:     report.EntryCount,
			LastSequence:       report.LastSequence,
			Consistent:         report.Consistent,
			Divergences:        tokensOf(report.Divergences),
			FirstBreakSequence: report.FirstBreakSequence,
		})
	})
}

// tokensOf answers nil for no divergence, so the field is left out rather than
// written as an empty list.
func tokensOf(divergences []reconcilewallet.Divergence) []string {
	if len(divergences) == 0 {
		return nil
	}
	tokens := make([]string, 0, len(divergences))
	for _, divergence := range divergences {
		tokens = append(tokens, divergence.String())
	}
	return tokens
}
