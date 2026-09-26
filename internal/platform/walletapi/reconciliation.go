package walletapi

import (
	"context"
	"net/http"

	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// Reconciler is the reconciliation of one wallet, as the border needs it.
type Reconciler interface {
	Reconcile(ctx context.Context, id identity.WalletID) (reconcilewallet.Report, error)
}

// reconciliationResponse is what the reconciliation route answers. Divergences
// and FirstBreakSequence are left out of a consistent wallet: a field that is
// absent says there is nothing to name, and an empty list would say the same
// thing twice.
type reconciliationResponse struct {
	WalletID           string      `json:"walletId"`
	StoredBalance      money.Money `json:"storedBalance"`
	LedgerBalance      money.Money `json:"ledgerBalance"`
	Version            int64       `json:"version"`
	EntryCount         int64       `json:"entryCount"`
	LastSequence       int64       `json:"lastSequence"`
	Consistent         bool        `json:"consistent"`
	Divergences        []string    `json:"divergences,omitempty"`
	FirstBreakSequence int64       `json:"firstBreakSequence,omitempty"`
}

// Reconcile serves GET /wallets/{walletId}/reconciliation. It reads and never
// corrects, and a wallet that does not exist answers 404. A divergence is a
// result the read reports, not a failure of the service: it is logged, and the
// span stays ok.
func Reconcile(reconciler Reconciler, reporter *Reporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := decodeWalletID(r)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		report, err := reconciler.Reconcile(r.Context(), id)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		reporter.Diverged(r, report)
		write(w, http.StatusOK, reconciliationResponse{
			WalletID:           report.WalletID.String(),
			StoredBalance:      report.StoredBalance,
			LedgerBalance:      report.LedgerBalance,
			Version:            report.Version,
			EntryCount:         report.EntryCount,
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
