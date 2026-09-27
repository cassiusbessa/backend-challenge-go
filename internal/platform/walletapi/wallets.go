package walletapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/junglegaming/backend-challenge-go/internal/app/openwallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

// Opener is the use case of the opening, as the border needs it.
type Opener interface {
	Open(ctx context.Context, cmd openwallet.Command) (openwallet.Result, error)
}

// Reader is the query of one wallet, as the border needs it.
type Reader interface {
	Wallet(ctx context.Context, id identity.WalletID) (storage.WalletView, error)
}

// walletResponse is what both routes answer. Money leaves as a decimal string of
// two places, which is what money.MarshalJSON writes — never a JSON number.
type walletResponse struct {
	ID       string      `json:"id"`
	PlayerID string      `json:"playerId"`
	Balance  money.Money `json:"balance"`
	Version  int64       `json:"version"`
}

// Open serves POST /wallets. A positive initial balance records the wallet, the
// internal OPENING and the credit entry in one commit; zero records the wallet
// alone. The wallet is born at version 1 either way.
func Open(opener Opener, reporter *Reporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd, err := decodeOpen(w, r)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		ctx, done := telemetry.Step(r.Context(), "open wallet")
		opened, err := opener.Open(ctx, cmd)
		done(err)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		reporter.Opened(r, opened.WalletID)
		write(w, http.StatusCreated, walletResponse{
			ID:       opened.WalletID.String(),
			PlayerID: opened.PlayerID.String(),
			Balance:  opened.Balance,
			Version:  opened.Version,
		})
	})
}

// Read serves GET /wallets/{walletId}. A wallet that does not exist answers 404,
// because the wallet is the resource of the URL.
func Read(reader Reader, reporter *Reporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := decodeWalletID(r)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		ctx, done := telemetry.Step(r.Context(), "read wallet")
		found, err := reader.Wallet(ctx, id)
		done(err)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		write(w, http.StatusOK, walletResponse{
			ID:       found.ID.String(),
			PlayerID: found.PlayerID.String(),
			Balance:  found.Balance,
			Version:  found.Version,
		})
	})
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// A client that hung up leaves nothing to answer with, and the status line
	// already left.
	_ = json.NewEncoder(w).Encode(body)
}
