// Package bodyhash answers the hash of the business body of a wager operation.
//
// It lives in the application and not inside a border because HTTP and the
// queue must answer the same value for the same business: the envelope of each
// channel is its own, and the operation is one.
package bodyhash

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// Business is the operation the provider asked for, with every field already
// parsed: the hash is taken from the values and never from the text that
// arrived, so "25.0" and "25.00" reach it as the same amount.
//
// The idempotency key, the headers, the message identity, the envelope instant
// and the correlation are absent on purpose. Those tell two arrivals apart, and
// what this hash answers is whether the business behind them is the same.
type Business struct {
	ProviderID          identity.ProviderID
	ExternalID          identity.ExternalTransactionID
	PlayerID            identity.PlayerID
	WalletID            identity.WalletID
	RoundID             identity.RoundID
	GameID              identity.GameID
	Kind                wager.Kind
	Amount              money.Money
	ReferenceExternalID identity.ExternalTransactionID
}

// Of answers the SHA-256 in hexadecimal of the canonical JSON of the business.
func Of(business Business) string {
	sum := sha256.Sum256(canonical(business.fields()))
	return hex.EncodeToString(sum[:])
}

// fields is the business as a flat map, which is what makes the order canonical:
// encoding/json sorts the keys of a map, while the fields of a struct leave in
// declaration order.
//
// The amount goes through the cents of Money and comes back with two places, and
// an identifier of ours answers in the canonical lowercase form of its own type.
// An absent field is left out of the map instead of carrying an empty value, so
// null never reaches the JSON.
func (b Business) fields() map[string]string {
	out := map[string]string{
		"amount":                b.Amount.Amount(),
		"currency":              b.Amount.Currency().Code(),
		"externalTransactionId": b.ExternalID.String(),
		"gameId":                b.GameID.String(),
		"kind":                  b.Kind.String(),
		"playerId":              b.PlayerID.String(),
		"providerId":            b.ProviderID.String(),
		"roundId":               b.RoundID.String(),
		"walletId":              b.WalletID.String(),
	}
	if !b.ReferenceExternalID.IsZero() {
		out["referenceExternalTransactionId"] = b.ReferenceExternalID.String()
	}
	return out
}

// canonical writes the JSON with the HTML escape off, which only the encoder can
// do: json.Marshal escapes <, > and & with no way to ask it otherwise, and a
// provider identifier is kept exactly as it arrived.
//
// Encode closes the value with a newline, and that byte is dropped because the
// canonical form carries no spare space.
func canonical(fields map[string]string) []byte {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	// A map of strings carries no value the encoder can refuse, and a
	// bytes.Buffer never fails a write.
	_ = encoder.Encode(fields)
	return bytes.TrimRight(buffer.Bytes(), "\n")
}
