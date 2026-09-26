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
	// The reference stays raw so the border can tell an absent field from one that
	// is there and empty. An operation citing none leaves the field out; one that
	// cites another carries an identifier, and an empty or malformed value is
	// refused rather than read as citing nothing.
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
	return body.command(key)
}

func (b submitRequest) command(key identity.IdempotencyKey) (submitwager.Command, error) {
	var parse fields
	cmd := submitwager.Command{
		ProviderID:          parse.provider(b.ProviderID),
		ExternalID:          parse.external(b.ExternalTransactionID),
		IdempotencyKey:      key,
		PlayerID:            parse.player(b.PlayerID),
		WalletID:            parse.wallet(b.WalletID),
		RoundID:             parse.round(b.RoundID),
		GameID:              parse.game(b.GameID),
		Kind:                parse.kind(b.Kind),
		Amount:              parse.money(b.Money.Amount, b.Money.Currency),
		ReferenceExternalID: parse.reference(b.ReferenceExternalTransactionID),
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

// referenceField is the one field of the body that may be absent altogether,
// which is why its name is spelled once and read back twice.
const referenceField = "referenceExternalTransactionId"

// reference parses the operation the body cites, and answers the zero identifier
// for a body that cites none.
//
// An absent field and an explicit null both mean citing nothing, which is what
// go-idempotency says of a null: it does not enter the business at all. A field
// that is there and is not an identifier is refused as invalid input, because a
// body that meant to cite an operation and spelled it wrong must not settle as
// though it cited none.
func (f *fields) reference(raw json.RawMessage) identity.ExternalTransactionID {
	if len(raw) == 0 || string(raw) == "null" {
		return identity.ExternalTransactionID{}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		f.keep(err, referenceField)
		return identity.ExternalTransactionID{}
	}
	parsed, err := identity.ParseExternalTransactionID(text)
	f.keep(err, referenceField)
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

// decodeExternal reads the provider and the external identifier the URL names,
// and refuses the first one out of format by its name.
func decodeExternal(r *http.Request) (identity.ProviderID, identity.ExternalTransactionID, error) {
	var parse fields
	provider := parse.provider(r.PathValue("providerId"))
	external := parse.external(r.PathValue("externalTransactionId"))
	return provider, external, parse.err
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
