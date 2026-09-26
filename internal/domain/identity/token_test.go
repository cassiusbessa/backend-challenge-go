package identity

import (
	"errors"
	"testing"
)

func TestParseToken_keepsTheProviderTextAsItArrived(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{name: "an opaque provider slug is kept", text: "Provider-A"},
		{name: "a UUID shaped token is kept", text: lowercaseUUID},
		{name: "mixed case is not normalized", text: "TX-00A1b2"},
		{name: "inner spaces are kept", text: "round 42"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, err := parseToken(testCase.text)
			if err != nil {
				t.Fatalf("parseToken(%q) error = %v, want nil", testCase.text, err)
			}
			if parsed.String() != testCase.text {
				t.Fatalf("parseToken(%q) kept %q, want the text unchanged", testCase.text, parsed.String())
			}
		})
	}
}

func TestParseToken_refusesTheAbsentIdentifier(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{name: "empty text carries no identifier", text: ""},
		{name: "spaces alone carry no identifier", text: "   "},
		{name: "a tab alone carries no identifier", text: "\t"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, err := parseToken(testCase.text)
			if !errors.Is(err, ErrMissingIdentifier) {
				t.Fatalf("parseToken(%q) error = %v, want ErrMissingIdentifier", testCase.text, err)
			}
			if !parsed.IsZero() {
				t.Fatalf("refused %q produced %q, want the zero value", testCase.text, parsed.String())
			}
		})
	}
}

func TestParseToken_refusesTextTheDatabaseCannotStore(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{name: "a byte out of UTF-8 is not text", text: "external-\xff"},
		{name: "a lone continuation byte is not text", text: "\x80"},
		{name: "a NUL inside the token is refused", text: "external-\x00-1"},
		{name: "a NUL alone is refused", text: "\x00"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, err := parseToken(testCase.text)
			if !errors.Is(err, ErrMalformedIdentifier) {
				t.Fatalf("parseToken(%q) error = %v, want ErrMalformedIdentifier", testCase.text, err)
			}
			if !parsed.IsZero() {
				t.Fatalf("malformed %q produced %q, want the zero value", testCase.text, parsed.String())
			}
		})
	}
}
