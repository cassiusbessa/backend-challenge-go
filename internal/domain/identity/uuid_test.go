package identity

import (
	"errors"
	"strings"
	"testing"
)

const (
	lowercaseUUID = "9b2f1c6e-3a44-4c2b-8d5e-7f0a1b2c3d4e"
	uppercaseUUID = "9B2F1C6E-3A44-4C2B-8D5E-7F0A1B2C3D4E"
	nilUUID       = "00000000-0000-0000-0000-000000000000"
)

func TestParseCanonical_keepsTheCanonicalLowercaseForm(t *testing.T) {
	t.Parallel()
	parsed, err := parseCanonical(lowercaseUUID)
	if err != nil {
		t.Fatalf("parseCanonical(%q) error = %v, want nil", lowercaseUUID, err)
	}
	if parsed.String() != lowercaseUUID {
		t.Fatalf("parsed value = %q, want %q", parsed.String(), lowercaseUUID)
	}
	if parsed.IsZero() {
		t.Fatalf("a parsed identifier reported IsZero() = true, want false")
	}
}

// The border may send the UUID uppercase or in the urn form. What the domain
// stores is always the canonical form, which is what go-idempotency requires
// of the hash.
func TestParseCanonical_normalizesTheFormsTheBorderMaySend(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{name: "uppercase becomes lowercase", text: uppercaseUUID},
		{name: "the urn form drops the prefix", text: "urn:uuid:" + lowercaseUUID},
		{name: "the braced form drops the braces", text: "{" + lowercaseUUID + "}"},
		{name: "the unhyphenated form gains the hyphens", text: "9b2f1c6e3a444c2b8d5e7f0a1b2c3d4e"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, err := parseCanonical(testCase.text)
			if err != nil {
				t.Fatalf("parseCanonical(%q) error = %v, want nil", testCase.text, err)
			}
			if parsed.String() != lowercaseUUID {
				t.Fatalf("parseCanonical(%q) stored %q, want %q", testCase.text, parsed.String(), lowercaseUUID)
			}
		})
	}
}

func TestParseCanonical_refusesWhatIsNotAUUID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{name: "empty text is not a UUID", text: ""},
		{name: "one character short is refused", text: lowercaseUUID[:35]},
		{name: "a non hex letter is refused", text: "9b2f1c6g-3a44-4c2b-8d5e-7f0a1b2c3d4e"},
		{name: "plain words are refused", text: "not-a-uuid"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parsed, err := parseCanonical(testCase.text)
			if !errors.Is(err, ErrInvalidUUID) {
				t.Fatalf("parseCanonical(%q) error = %v, want ErrInvalidUUID", testCase.text, err)
			}
			if !parsed.IsZero() {
				t.Fatalf("refused %q produced %q, want the zero value", testCase.text, parsed.String())
			}
		})
	}
}

// A refusal ends up in a log line, so the message has to stay on one line and
// still carry both the sentinel and the standard library detail.
func TestParseCanonical_refusesOnASingleLine(t *testing.T) {
	t.Parallel()
	_, err := parseCanonical("not-a-uuid")
	if err == nil {
		t.Fatalf("parseCanonical of plain text error = nil, want a refusal")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("refusal message = %q, want a single line", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "identity: ") {
		t.Fatalf("refusal message = %q, want the package prefix", err.Error())
	}
}

func TestIsZero_treatsTheNilUUIDAsNoIdentity(t *testing.T) {
	t.Parallel()
	parsed, err := parseCanonical(nilUUID)
	if err != nil {
		t.Fatalf("parseCanonical of the nil UUID error = %v, want nil", err)
	}
	if !parsed.IsZero() {
		t.Fatalf("the nil UUID reported IsZero() = false, want true")
	}
}
