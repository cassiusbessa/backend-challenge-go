// Package wagerapi is the HTTP border of the wager routes: it decodes, checks the
// provider of the body against the client of the token, hands the command to the
// use case and writes the answer.
package wagerapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

// maxBody bounds the submission payload. It holds nine identifiers and an amount,
// so anything larger is not a request this route can serve.
const maxBody = 8 << 10

// idempotencyHeader is where the key arrives over HTTP. Over the queue the same
// key arrives in the envelope, and the two produce the same business hash.
const idempotencyHeader = "Idempotency-Key"

// refusedField is input the border did not take. It carries the name of the field
// and never its value, because the amount and the key of the request must not
// travel back in the error body.
type refusedField struct {
	name string
	why  string
}

func (e refusedField) Error() string {
	return "wagerapi: " + e.name + " " + e.why
}

// Unwrap puts the refusal in the invalid input class, so the border answers 400
// with no failureCode: nothing was written and no rule refused anything.
func (e refusedField) Unwrap() error {
	return problem.ErrInvalidInput
}

func invalid(name string) error {
	return refusedField{name: name, why: "is not valid"}
}

// notAccepted is input this delivery does not take yet. It is not a business
// rejection: the answer carries no token and claims no rule refused the
// operation.
//
// It exists to be removed by the delivery that brings the wait for a cited
// operation, which is what the two reversals need.
func notAccepted(name string) error {
	return refusedField{name: name, why: "is not accepted by this delivery"}
}

// submitRequest is the body of POST /wagering/transactions. Money crosses the
// contract as a decimal string of two places, never as a JSON number.
type submitRequest struct {
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	PlayerID              string `json:"playerId"`
	WalletID              string `json:"walletId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Money                 struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"money"`
	// The reference stays raw so the border can tell an absent key from a present
	// one. encoding/json leaves both a missing field and an explicit null as the
	// zero value of a string or a pointer, and a present key is exactly what this
	// delivery has to refuse.
	ReferenceExternalTransactionID json.RawMessage `json:"referenceExternalTransactionId"`
}

// decodeSubmit reads the command. Every refusal here is invalid input, so no row
// is written and the answer carries no token.
func decodeSubmit(w http.ResponseWriter, r *http.Request) (submitwager.Command, error) {
	var body submitRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&body); err != nil {
		return submitwager.Command{}, invalid("body")
	}
	key, err := identity.ParseIdempotencyKey(r.Header.Get(idempotencyHeader))
	if err != nil {
		return submitwager.Command{}, invalid(idempotencyHeader)
	}
	if err := body.accepted(); err != nil {
		return submitwager.Command{}, err
	}
	return body.command(key)
}

// accepted refuses what this delivery does not settle: an operation citing
// another one, which needs a wait nothing here can close, and the two reversals,
// which never exist without that wait.
func (b submitRequest) accepted() error {
	if len(b.ReferenceExternalTransactionID) > 0 {
		return notAccepted("referenceExternalTransactionId")
	}
	if b.Kind == wager.KindRefund.String() || b.Kind == wager.KindRollback.String() {
		return notAccepted("kind")
	}
	return nil
}

func (b submitRequest) command(key identity.IdempotencyKey) (submitwager.Command, error) {
	var parse fields
	cmd := submitwager.Command{
		ProviderID:     parse.provider(b.ProviderID),
		ExternalID:     parse.external(b.ExternalTransactionID),
		IdempotencyKey: key,
		PlayerID:       parse.player(b.PlayerID),
		WalletID:       parse.wallet(b.WalletID),
		RoundID:        parse.round(b.RoundID),
		GameID:         parse.game(b.GameID),
		Kind:           parse.kind(b.Kind),
		Amount:         parse.money(b.Money.Amount, b.Money.Currency),
	}
	if parse.err != nil {
		return submitwager.Command{}, parse.err
	}
	return cmd, nil
}

// fields parses the body into the command and keeps the first field the border
// refused.
//
// A body of ten fields would otherwise need a branch per field, and one refusal is
// all the answer carries: it names the first field and never its value.
type fields struct {
	err error
}

func (f *fields) keep(err error, name string) {
	if err != nil && f.err == nil {
		f.err = invalid(name)
	}
}

// required refuses an identifier the parse accepted as no identity. The nil UUID
// is well formed and identity reports it as absent on purpose, so a field the
// operation cannot do without is refused here and never reaches the aggregate,
// where an incomplete transaction is a defect and not invalid input.
func (f *fields) required(absent bool, name string) {
	if absent && f.err == nil {
		f.err = invalid(name)
	}
}

func (f *fields) provider(text string) identity.ProviderID {
	parsed, err := identity.ParseProviderID(text)
	f.keep(err, "providerId")
	return parsed
}

func (f *fields) external(text string) identity.ExternalTransactionID {
	parsed, err := identity.ParseExternalTransactionID(text)
	f.keep(err, "externalTransactionId")
	return parsed
}

func (f *fields) player(text string) identity.PlayerID {
	parsed, err := identity.ParsePlayerID(text)
	f.keep(err, "playerId")
	f.required(parsed.IsZero(), "playerId")
	return parsed
}

func (f *fields) wallet(text string) identity.WalletID {
	parsed, err := identity.ParseWalletID(text)
	f.keep(err, "walletId")
	f.required(parsed.IsZero(), "walletId")
	return parsed
}

func (f *fields) round(text string) identity.RoundID {
	parsed, err := identity.ParseRoundID(text)
	f.keep(err, "roundId")
	return parsed
}

func (f *fields) game(text string) identity.GameID {
	parsed, err := identity.ParseGameID(text)
	f.keep(err, "gameId")
	return parsed
}

func (f *fields) kind(text string) wager.Kind {
	parsed, err := wager.ParseKind(text)
	f.keep(err, "kind")
	return parsed
}

// money refuses the amount instead of rounding it: money.Parse turns down the
// empty string, NaN, Infinity, scientific notation, a scale past two places and a
// negative value, and a refused amount stays refused.
func (f *fields) money(amount, currency string) money.Money {
	parsed, err := money.Parse(amount, currency)
	f.keep(err, "money")
	return parsed
}

// decodeTransactionID reads the identity the URL names.
func decodeTransactionID(r *http.Request) (identity.TransactionID, error) {
	id, err := identity.ParseTransactionID(r.PathValue("transactionId"))
	if err != nil {
		return identity.TransactionID{}, invalid("transactionId")
	}
	return id, nil
}

// detailOf names the field that was refused and why. An error of another class
// carries no detail, because only the name of the field is safe to repeat.
func detailOf(err error) string {
	var field refusedField
	if !errors.As(err, &field) {
		return ""
	}
	return field.name + " " + field.why
}
