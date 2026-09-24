// Package walletapi is the HTTP border of the wallet routes: it decodes, hands
// the command to the use case and writes the answer.
package walletapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/junglegaming/backend-challenge-go/internal/app/openwallet"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

// maxBody bounds the opening payload. It holds a player and an amount, so
// anything larger is not a request this route can serve.
const maxBody = 4 << 10

// invalidField is input the border refused. It carries the name of the field and
// never its value, because the amount and the credential of the request must not
// travel back in the error body.
type invalidField struct {
	name string
}

func (e invalidField) Error() string {
	return "walletapi: " + e.name + " is not valid"
}

// Unwrap puts the refusal in the invalid input class, so the border answers 400
// with no failureCode.
func (e invalidField) Unwrap() error {
	return problem.ErrInvalidInput
}

// openRequest is the body of POST /wallets. Money crosses the contract as a
// decimal string, never as a JSON number.
type openRequest struct {
	PlayerID       string `json:"playerId"`
	InitialBalance struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"initialBalance"`
}

// decodeOpen reads the command. Every refusal here is invalid input, so no row
// is written and the answer carries no token.
//
// The amount is not rounded on the way in: money.Parse refuses the empty string,
// NaN, Infinity, scientific notation, a scale past two places and a negative
// value, and a refused amount stays refused.
func decodeOpen(w http.ResponseWriter, r *http.Request) (openwallet.Command, error) {
	var body openRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&body); err != nil {
		return openwallet.Command{}, invalidField{name: "body"}
	}
	playerID, err := identity.ParsePlayerID(body.PlayerID)
	if err != nil {
		return openwallet.Command{}, invalidField{name: "playerId"}
	}
	balance, err := money.Parse(body.InitialBalance.Amount, body.InitialBalance.Currency)
	if err != nil {
		return openwallet.Command{}, invalidField{name: "initialBalance"}
	}
	return openwallet.Command{PlayerID: playerID, InitialBalance: balance}, nil
}

// decodeWalletID reads the identity the URL names.
func decodeWalletID(r *http.Request) (identity.WalletID, error) {
	id, err := identity.ParseWalletID(r.PathValue("walletId"))
	if err != nil {
		return identity.WalletID{}, invalidField{name: "walletId"}
	}
	return id, nil
}

// detailOf names the field that was refused. An error of another class carries no
// detail, because only the field name is safe to repeat.
func detailOf(err error) string {
	var field invalidField
	if !errors.As(err, &field) {
		return ""
	}
	return field.name + " is not valid"
}
