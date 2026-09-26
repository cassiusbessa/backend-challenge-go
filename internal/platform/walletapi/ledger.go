package walletapi

import (
	"context"
	"net/http"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/listledger"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// Lister is the read of one page of the ledger, as the border needs it.
type Lister interface {
	Page(ctx context.Context, query listledger.Query) (listledger.Page, error)
}

// ledgerResponse is what the ledger route answers. NextCursor is left out on
// the last page, which is what tells the client to stop.
type ledgerResponse struct {
	WalletID   string          `json:"walletId"`
	Entries    []entryResponse `json:"entries"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// entryResponse is one entry as the client reads it. Money leaves as a decimal
// string of two places, never as a JSON number, and the instant leaves in UTC.
type entryResponse struct {
	ID            string      `json:"id"`
	TransactionID string      `json:"transactionId"`
	Direction     string      `json:"direction"`
	Amount        money.Money `json:"amount"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	Sequence      int64       `json:"sequenceNumber"`
	CreatedAt     time.Time   `json:"createdAt"`
}

// ListLedger serves GET /wallets/{walletId}/ledger. The limit and the cursor
// are refused before the ledger is read, and a wallet that does not exist
// answers 404, because the wallet is the resource of the URL.
func ListLedger(lister Lister, reporter *Reporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, err := decodeLedgerQuery(r)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		page, err := lister.Page(r.Context(), query)
		if err != nil {
			reporter.Refuse(w, r, refusalOf(err))
			return
		}
		write(w, http.StatusOK, ledgerResponse{
			WalletID:   query.WalletID.String(),
			Entries:    entriesOf(page.Entries),
			NextCursor: page.NextCursor,
		})
	})
}

// entriesOf answers a list even when there is nothing in it: an empty ledger is
// an empty array on the wire, not null.
func entriesOf(entries []storage.EntryView) []entryResponse {
	answered := make([]entryResponse, 0, len(entries))
	for _, entry := range entries {
		answered = append(answered, entryResponse{
			ID:            entry.ID.String(),
			TransactionID: entry.TransactionID.String(),
			Direction:     entry.Direction.String(),
			Amount:        entry.Amount,
			BalanceBefore: entry.BalanceBefore,
			BalanceAfter:  entry.BalanceAfter,
			Sequence:      entry.Sequence,
			CreatedAt:     entry.CreatedAt.UTC(),
		})
	}
	return answered
}
