// Package walletapi is the HTTP border of the wallet routes: it decodes, hands
// the command to the use case and writes the answer.
package walletapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/junglegaming/backend-challenge-go/internal/app/listledger"
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

// decodeLedgerQuery reads the page asked for: the wallet of the URL, the limit
// and the cursor of the query string.
//
// An absent limit is zero, which the use case reads as the default. A limit that
// is present is refused here when it is not an integer or is below one: zero is
// the only way to tell the use case nothing was asked, so an explicit zero has
// to be refused before it could be mistaken for that. The ceiling stays with
// the use case, which owns the range.
func decodeLedgerQuery(r *http.Request) (listledger.Query, error) {
	id, err := decodeWalletID(r)
	if err != nil {
		return listledger.Query{}, err
	}
	limit, err := decodeLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return listledger.Query{}, err
	}
	return listledger.Query{WalletID: id, Limit: limit, Cursor: r.URL.Query().Get("cursor")}, nil
}

func decodeLimit(text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(text)
	if err != nil || limit < 1 {
		return 0, invalidField{name: "limit"}
	}
	return limit, nil
}

// refusalOf translates what the use case refused into the field the client has
// to fix. The refusal names the field and never its value: a cursor echoed back
// would tell the client what the token carries, and a limit echoed back is
// nothing the client does not already know.
func refusalOf(err error) error {
	switch {
	case errors.Is(err, listledger.ErrInvalidCursor):
		return invalidField{name: "cursor"}
	case errors.Is(err, listledger.ErrInvalidLimit):
		return invalidField{name: "limit"}
	}
	return err
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
