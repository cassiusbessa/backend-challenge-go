package wagerqueue

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestDecode_readsTheEnvelopeAndTheOperationOfTheBody(t *testing.T) {
	t.Parallel()
	decoded, err := Decode(envelopeWith(nil, nil))
	if err != nil {
		t.Fatalf("Decode = %v, want nil", err)
	}
	assertEnvelope(t, decoded)
	assertOperation(t, decoded)
}

func assertEnvelope(t *testing.T, decoded Message) {
	t.Helper()
	if decoded.MessageID != messageID {
		t.Fatalf("messageId = %q, want %q", decoded.MessageID, messageID)
	}
	if decoded.CorrelationID != correlationID {
		t.Fatalf("correlationId = %q, want %q", decoded.CorrelationID, correlationID)
	}
}

func assertOperation(t *testing.T, decoded Message) {
	t.Helper()
	if decoded.Command.Kind != wager.KindBet {
		t.Fatalf("kind = %s, want BET", decoded.Command.Kind)
	}
	if got := decoded.Command.IdempotencyKey.String(); got != idempotencyKey {
		t.Fatalf("idempotencyKey = %q, want %q", got, idempotencyKey)
	}
	if got := decoded.Command.Amount.Amount(); got != "25.00" {
		t.Fatalf("amount = %q, want 25.00", got)
	}
	if !decoded.Command.ReferenceExternalID.IsZero() {
		t.Fatalf("reference = %s, want none: a bet cites no operation", decoded.Command.ReferenceExternalID)
	}
}

// An envelope with no correlation is not refused: the consumer correlates the
// operation by the identity of the message itself.
func TestDecode_acceptsAnEnvelopeThatCarriesNoCorrelation(t *testing.T) {
	t.Parallel()
	decoded, err := Decode(envelopeWith(map[string]any{"correlationId": nil}, nil))
	if err != nil {
		t.Fatalf("Decode with no correlation = %v, want nil", err)
	}
	if decoded.CorrelationID != "" {
		t.Fatalf("correlationId = %q, want empty", decoded.CorrelationID)
	}
}

// A reversal cites the operation it undoes, and the field is read as an
// identifier rather than as text.
func TestDecode_readsTheOperationAReversalCites(t *testing.T) {
	t.Parallel()
	body := map[string]any{"kind": "REFUND", "referenceExternalTransactionId": "external-1"}
	decoded, err := Decode(envelopeWith(nil, body))
	if err != nil {
		t.Fatalf("Decode of a refund = %v, want nil", err)
	}
	if decoded.Command.Kind != wager.KindRefund {
		t.Fatalf("kind = %s, want REFUND", decoded.Command.Kind)
	}
	if got := decoded.Command.ReferenceExternalID.String(); got != "external-1" {
		t.Fatalf("reference = %q, want external-1", got)
	}
}

// An absent field and an explicit null both mean citing nothing, which is what
// go-idempotency says of a null: it does not enter the business at all.
func TestDecode_readsAnExplicitNullReferenceAsCitingNothing(t *testing.T) {
	t.Parallel()
	decoded, err := Decode(envelopeWith(nil, map[string]any{"referenceExternalTransactionId": nil}))
	if err != nil {
		t.Fatalf("Decode with a null reference = %v, want nil", err)
	}
	if !decoded.Command.ReferenceExternalID.IsZero() {
		t.Fatalf("reference = %s, want none", decoded.Command.ReferenceExternalID)
	}
}

func TestDecode_refusesTheBodyItCannotTake(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		raw   []byte
		field string
	}{
		{name: "a body that is not json is refused", raw: []byte("{this is not json"), field: "body"},
		{name: "an envelope with no message identifier is refused", raw: envelopeWith(map[string]any{"messageId": ""}, nil), field: "messageId"},
		{name: "a missing provider is refused", raw: envelopeWith(nil, map[string]any{"providerId": ""}), field: "providerId"},
		{name: "a missing external identifier is refused", raw: envelopeWith(nil, map[string]any{"externalTransactionId": ""}), field: "externalTransactionId"},
		{name: "a missing idempotency key is refused", raw: envelopeWith(nil, map[string]any{"idempotencyKey": ""}), field: "idempotencyKey"},
		{name: "a player out of format is refused", raw: envelopeWith(nil, map[string]any{"playerId": "not-a-uuid"}), field: "playerId"},
		{name: "a nil player is refused", raw: envelopeWith(nil, map[string]any{"playerId": nilUUID}), field: "playerId"},
		{name: "a wallet out of format is refused", raw: envelopeWith(nil, map[string]any{"walletId": "not-a-uuid"}), field: "walletId"},
		{name: "a nil wallet is refused", raw: envelopeWith(nil, map[string]any{"walletId": nilUUID}), field: "walletId"},
		{name: "a missing round is refused", raw: envelopeWith(nil, map[string]any{"roundId": ""}), field: "roundId"},
		{name: "a missing game is refused", raw: envelopeWith(nil, map[string]any{"gameId": ""}), field: "gameId"},
		{name: "an unknown kind is refused", raw: envelopeWith(nil, map[string]any{"kind": "CASHOUT"}), field: "kind"},
		{name: "an amount past two places is refused", raw: amountOf("25.001", "BRL"), field: "money"},
		{name: "an amount in scientific notation is refused", raw: amountOf("2.5e1", "BRL"), field: "money"},
		{name: "a negative amount is refused", raw: amountOf("-25.00", "BRL"), field: "money"},
		{name: "a currency that is not an ISO code is refused", raw: amountOf("25.00", "brl"), field: "money"},
		{name: "a reference that is not an identifier is refused", raw: envelopeWith(nil, map[string]any{"referenceExternalTransactionId": 7}), field: referenceField},
		{name: "a reference that is there and empty is refused", raw: envelopeWith(nil, map[string]any{"referenceExternalTransactionId": ""}), field: referenceField},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(tc.raw)
			if !errors.Is(err, ErrInvalidMessage) {
				t.Fatalf("Decode = %v, want %v", err, ErrInvalidMessage)
			}
			var field refusedField
			if !errors.As(err, &field) || field.name != tc.field {
				t.Fatalf("refused field = %v, want %s", err, tc.field)
			}
		})
	}
}

// The values a valid message carries. They are fixed so a case overrides only
// what it is about.
const (
	messageID      = "0199e4d0-0000-7000-8000-000000000001"
	correlationID  = "corr-1"
	idempotencyKey = "key-1"
	playerID       = "0199e4d0-0000-7000-8000-00000000000a"
	walletID       = "0199e4d0-0000-7000-8000-00000000000b"
	nilUUID        = "00000000-0000-0000-0000-000000000000"
)

// envelopeWith builds one message, overriding the envelope and the business body
// where a case needs it. A nil value in an override removes the field, which is
// how an absent field is told from one that is there and empty.
func envelopeWith(top, data map[string]any) []byte {
	body := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": "external-1",
		"idempotencyKey":        idempotencyKey,
		"playerId":              playerID,
		"walletId":              walletID,
		"roundId":               "round-1",
		"gameId":                "game-1",
		"kind":                  "BET",
		"money":                 map[string]string{"amount": "25.00", "currency": "BRL"},
	}
	apply(body, data)
	message := map[string]any{
		"messageId":     messageID,
		"correlationId": correlationID,
		"data":          body,
	}
	apply(message, top)
	raw, err := json.Marshal(message)
	if err != nil {
		panic(err)
	}
	return raw
}

func apply(into, overrides map[string]any) {
	for field, value := range overrides {
		if value == nil {
			delete(into, field)
			continue
		}
		into[field] = value
	}
}

func amountOf(amount, currency string) []byte {
	return envelopeWith(nil, map[string]any{"money": map[string]string{"amount": amount, "currency": currency}})
}
