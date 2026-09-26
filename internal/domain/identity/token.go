package identity

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrMissingIdentifier = errors.New("identity: external identifier is empty")

var ErrMalformedIdentifier = errors.New("identity: external identifier is not valid text")

// token is the opaque identifier of the provider.
//
// Provider, external id, round and game are kept exactly as they arrived, as
// go-idempotency requires, so nothing is normalized: the refusals are absence
// and bytes the database cannot store.
type token struct {
	value string
}

func parseToken(text string) (token, error) {
	if strings.TrimSpace(text) == "" {
		return token{}, ErrMissingIdentifier
	}
	// PostgreSQL stores text as UTF-8 and refuses NUL in it: a token carrying
	// either would reach a query and answer an outage, not invalid input.
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return token{}, ErrMalformedIdentifier
	}
	return token{value: text}, nil
}

func (t token) String() string {
	return t.value
}

func (t token) IsZero() bool {
	return t.value == ""
}
