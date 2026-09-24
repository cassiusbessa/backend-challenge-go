package identity

import (
	"errors"
	"strings"
)

var ErrMissingIdentifier = errors.New("identity: external identifier is empty")

// token is the opaque identifier of the provider.
//
// Provider, external id, round and game are kept exactly as they arrived, as
// go-idempotency requires, so the only refusal here is absence.
type token struct {
	value string
}

func parseToken(text string) (token, error) {
	if strings.TrimSpace(text) == "" {
		return token{}, ErrMissingIdentifier
	}
	return token{value: text}, nil
}

func (t token) String() string {
	return t.value
}

func (t token) IsZero() bool {
	return t.value == ""
}
