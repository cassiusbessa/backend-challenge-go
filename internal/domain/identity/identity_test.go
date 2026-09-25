package identity

import (
	"errors"
	"testing"
)

func TestParseWalletID_takesTheUUIDAndNothingElse(t *testing.T) {
	t.Parallel()
	parsed, err := ParseWalletID(uppercaseUUID)
	if err != nil {
		t.Fatalf("ParseWalletID error = %v, want nil", err)
	}
	if parsed.String() != lowercaseUUID {
		t.Fatalf("wallet id = %q, want %q", parsed.String(), lowercaseUUID)
	}
	refused, err := ParseWalletID("not-a-uuid")
	if !errors.Is(err, ErrInvalidUUID) {
		t.Fatalf("ParseWalletID of plain text error = %v, want ErrInvalidUUID", err)
	}
	if !refused.IsZero() {
		t.Fatalf("refused wallet id = %q, want the zero value", refused.String())
	}
}

func TestParsePlayerID_takesTheUUIDAndNothingElse(t *testing.T) {
	t.Parallel()
	parsed, err := ParsePlayerID(lowercaseUUID)
	if err != nil {
		t.Fatalf("ParsePlayerID error = %v, want nil", err)
	}
	if parsed.String() != lowercaseUUID {
		t.Fatalf("player id = %q, want %q", parsed.String(), lowercaseUUID)
	}
	refused, err := ParsePlayerID("")
	if !errors.Is(err, ErrInvalidUUID) {
		t.Fatalf("ParsePlayerID of empty text error = %v, want ErrInvalidUUID", err)
	}
	if !refused.IsZero() {
		t.Fatalf("refused player id = %q, want the zero value", refused.String())
	}
}

func TestParseTransactionID_takesTheUUIDAndNothingElse(t *testing.T) {
	t.Parallel()
	parsed, err := ParseTransactionID(lowercaseUUID)
	if err != nil {
		t.Fatalf("ParseTransactionID error = %v, want nil", err)
	}
	if parsed.String() != lowercaseUUID {
		t.Fatalf("transaction id = %q, want %q", parsed.String(), lowercaseUUID)
	}
	refused, err := ParseTransactionID(nilUUID)
	if err != nil {
		t.Fatalf("ParseTransactionID of the nil UUID error = %v, want nil", err)
	}
	if !refused.IsZero() {
		t.Fatalf("the nil transaction id = %q reported IsZero() = false, want true", refused.String())
	}
}

func TestParseLedgerEntryID_takesTheUUIDAndNothingElse(t *testing.T) {
	t.Parallel()
	parsed, err := ParseLedgerEntryID(lowercaseUUID)
	if err != nil {
		t.Fatalf("ParseLedgerEntryID error = %v, want nil", err)
	}
	if parsed.String() != lowercaseUUID {
		t.Fatalf("ledger entry id = %q, want %q", parsed.String(), lowercaseUUID)
	}
	refused, err := ParseLedgerEntryID("9b2f1c6e-3a44-4c2b-8d5e")
	if !errors.Is(err, ErrInvalidUUID) {
		t.Fatalf("ParseLedgerEntryID of a truncated UUID error = %v, want ErrInvalidUUID", err)
	}
	if !refused.IsZero() {
		t.Fatalf("refused ledger entry id = %q, want the zero value", refused.String())
	}
}

func TestParseEventID_takesTheUUIDAndNothingElse(t *testing.T) {
	t.Parallel()
	parsed, err := ParseEventID(uppercaseUUID)
	if err != nil {
		t.Fatalf("ParseEventID error = %v, want nil", err)
	}
	if parsed.String() != lowercaseUUID {
		t.Fatalf("event id = %q, want %q", parsed.String(), lowercaseUUID)
	}
	refused, err := ParseEventID("not-a-uuid")
	if !errors.Is(err, ErrInvalidUUID) {
		t.Fatalf("ParseEventID of plain text error = %v, want ErrInvalidUUID", err)
	}
	if !refused.IsZero() {
		t.Fatalf("refused event id = %q, want the zero value", refused.String())
	}
}

func TestParseProviderID_keepsTheProviderTextAndRefusesTheAbsence(t *testing.T) {
	t.Parallel()
	parsed, err := ParseProviderID("Provider-A")
	if err != nil {
		t.Fatalf("ParseProviderID error = %v, want nil", err)
	}
	if parsed.String() != "Provider-A" {
		t.Fatalf("provider id = %q, want %q", parsed.String(), "Provider-A")
	}
	refused, err := ParseProviderID("")
	if !errors.Is(err, ErrMissingIdentifier) {
		t.Fatalf("ParseProviderID of empty text error = %v, want ErrMissingIdentifier", err)
	}
	if !refused.IsZero() {
		t.Fatalf("refused provider id = %q, want the zero value", refused.String())
	}
}

func TestParseExternalTransactionID_keepsTheProviderTextAndRefusesTheAbsence(t *testing.T) {
	t.Parallel()
	parsed, err := ParseExternalTransactionID("TX-00A1b2")
	if err != nil {
		t.Fatalf("ParseExternalTransactionID error = %v, want nil", err)
	}
	if parsed.String() != "TX-00A1b2" {
		t.Fatalf("external transaction id = %q, want %q", parsed.String(), "TX-00A1b2")
	}
	refused, err := ParseExternalTransactionID(" ")
	if !errors.Is(err, ErrMissingIdentifier) {
		t.Fatalf("ParseExternalTransactionID of a blank text error = %v, want ErrMissingIdentifier", err)
	}
	if !refused.IsZero() {
		t.Fatalf("refused external transaction id = %q, want the zero value", refused.String())
	}
}

func TestParseRoundID_keepsTheProviderTextAndRefusesTheAbsence(t *testing.T) {
	t.Parallel()
	parsed, err := ParseRoundID("round-42")
	if err != nil {
		t.Fatalf("ParseRoundID error = %v, want nil", err)
	}
	if parsed.String() != "round-42" {
		t.Fatalf("round id = %q, want %q", parsed.String(), "round-42")
	}
	refused, err := ParseRoundID("")
	if !errors.Is(err, ErrMissingIdentifier) {
		t.Fatalf("ParseRoundID of empty text error = %v, want ErrMissingIdentifier", err)
	}
	if !refused.IsZero() {
		t.Fatalf("refused round id = %q, want the zero value", refused.String())
	}
}

func TestParseGameID_keepsTheProviderTextAndRefusesTheAbsence(t *testing.T) {
	t.Parallel()
	parsed, err := ParseGameID("crash")
	if err != nil {
		t.Fatalf("ParseGameID error = %v, want nil", err)
	}
	if parsed.String() != "crash" {
		t.Fatalf("game id = %q, want %q", parsed.String(), "crash")
	}
	refused, err := ParseGameID("")
	if !errors.Is(err, ErrMissingIdentifier) {
		t.Fatalf("ParseGameID of empty text error = %v, want ErrMissingIdentifier", err)
	}
	if !refused.IsZero() {
		t.Fatalf("refused game id = %q, want the zero value", refused.String())
	}
}

func TestParseIdempotencyKey_keepsTheKeyAsItArrivedAndRefusesTheAbsence(t *testing.T) {
	t.Parallel()
	parsed, err := ParseIdempotencyKey("Key-001")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey error = %v, want nil", err)
	}
	if parsed.String() != "Key-001" {
		t.Fatalf("idempotency key = %q, want %q", parsed.String(), "Key-001")
	}
	refused, err := ParseIdempotencyKey("")
	if !errors.Is(err, ErrMissingIdentifier) {
		t.Fatalf("ParseIdempotencyKey of empty text error = %v, want ErrMissingIdentifier", err)
	}
	if !refused.IsZero() {
		t.Fatalf("refused idempotency key = %q, want the zero value", refused.String())
	}
}
