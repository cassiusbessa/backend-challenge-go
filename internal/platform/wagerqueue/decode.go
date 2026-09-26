// Package wagerqueue is the queue border of the wager ingress: it decodes the
// message, hands the command to the use case that decides it, and answers the
// broker with what that outcome means for the message.
//
// Nothing here decides business. The four answers the broker takes — remove,
// return, abandon and remove without reapplying — are the four outcomes the use
// case already classifies, translated.
package wagerqueue

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// ErrInvalidMessage is a body this border did not take: a field missing, an
// amount that is not a two-place decimal, an unknown currency, an identifier out
// of format, an unknown kind, or no idempotency key.
//
// It is one of the reasons a message is abandoned to the dead-letter queue, and
// it never writes a financial row: nothing about it is answered differently on a
// second delivery of the same bytes.
var ErrInvalidMessage = errors.New("wagerqueue: message is not valid")

// refusedField is input the border did not take. It carries the name of the
// field and never its value, because the amount and the key of the operation must
// not travel into a log line.
type refusedField struct {
	name string
}

func (e refusedField) Error() string {
	return "wagerqueue: " + e.name + " is not valid"
}

// Unwrap puts the refusal in the invalid message class, which is what the
// consumer reads to send the message to the dead-letter queue.
func (e refusedField) Unwrap() error {
	return ErrInvalidMessage
}

func invalid(name string) error {
	return refusedField{name: name}
}

// Message is one decoded message: the identity of the envelope, the correlation
// it carried, and the operation its body asked for.
//
// CorrelationID is what the envelope carried, zero when it carried none. The
// consumer takes it only when it is a short opaque token, as over HTTP, and
// otherwise correlates the operation by the identity of the message itself.
type Message struct {
	MessageID     string
	CorrelationID string
	Command       submitwager.Command
}

// envelope is the message as it arrives on the queue. The identity of the message
// is of the envelope and the operation is of the data, which is what lets the
// same business body travel over HTTP and over the queue and hash alike.
//
// Everything outside data — the identity of the message, the correlation, the
// type and the instant — is left out of the hash by go-idempotency, so a
// redelivery that differs only there is the same business.
type envelope struct {
	MessageID     string     `json:"messageId"`
	CorrelationID string     `json:"correlationId"`
	Data          submitData `json:"data"`
}

// submitData is the business body, which is the body of the HTTP route plus the
// idempotency key that arrives in a header there.
//
// Money crosses the contract as a decimal string of two places, never as a JSON
// number, the same as over HTTP.
type submitData struct {
	ProviderID            string `json:"providerId"`
	ExternalTransactionID string `json:"externalTransactionId"`
	IdempotencyKey        string `json:"idempotencyKey"`
	PlayerID              string `json:"playerId"`
	WalletID              string `json:"walletId"`
	RoundID               string `json:"roundId"`
	GameID                string `json:"gameId"`
	Kind                  string `json:"kind"`
	Money                 struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	} `json:"money"`
	// The reference stays raw so the border can tell an absent field from one
	// that is there and empty, the same as over HTTP: a body that meant to cite
	// an operation and spelled it wrong must not settle as though it cited none.
	ReferenceExternalTransactionID json.RawMessage `json:"referenceExternalTransactionId"`
}

// Decode reads the message. Every refusal here is an invalid message, so no row
// is written and the message leaves for the dead-letter queue.
func Decode(raw []byte) (Message, error) {
	var body envelope
	if err := json.Unmarshal(raw, &body); err != nil {
		return Message{}, invalid("body")
	}
	// The identity is written to the inbox, whose text column refuses NUL. JSON
	// already replaced any byte outside UTF-8, so NUL is the one left to refuse.
	if body.MessageID == "" || strings.ContainsRune(body.MessageID, 0) {
		return Message{}, invalid("messageId")
	}
	cmd, err := body.Data.command()
	if err != nil {
		return Message{}, err
	}
	return Message{MessageID: body.MessageID, CorrelationID: body.CorrelationID, Command: cmd}, nil
}

func (d submitData) command() (submitwager.Command, error) {
	var parse fields
	cmd := submitwager.Command{
		ProviderID:          parse.provider(d.ProviderID),
		ExternalID:          parse.external(d.ExternalTransactionID),
		IdempotencyKey:      parse.key(d.IdempotencyKey),
		PlayerID:            parse.player(d.PlayerID),
		WalletID:            parse.wallet(d.WalletID),
		RoundID:             parse.round(d.RoundID),
		GameID:              parse.game(d.GameID),
		Kind:                parse.kind(d.Kind),
		Amount:              parse.money(d.Money.Amount, d.Money.Currency),
		ReferenceExternalID: parse.reference(d.ReferenceExternalTransactionID),
	}
	if parse.err != nil {
		return submitwager.Command{}, parse.err
	}
	return cmd, nil
}

// fields parses the body into the command and keeps the first field the border
// refused.
//
// A body of ten fields would otherwise need a branch per field, and one refusal
// is all that is logged: it names the first field and never its value.
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
// operation cannot do without is refused here and never reaches the aggregate.
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

// key parses the idempotency key of the body. Over HTTP it arrives in a header
// and here it is a field of the data, and the two are the same key: it is scoped
// to the provider either way, and it stays out of the hash either way.
func (f *fields) key(text string) identity.IdempotencyKey {
	parsed, err := identity.ParseIdempotencyKey(text)
	f.keep(err, "idempotencyKey")
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
// go-idempotency says of a null: it does not enter the business at all.
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
// empty string, NaN, Infinity, scientific notation, a scale past two places and
// a negative value, and a refused amount stays refused.
func (f *fields) money(amount, currency string) money.Money {
	parsed, err := money.Parse(amount, currency)
	f.keep(err, "money")
	return parsed
}
