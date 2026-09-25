package bodyhash

import (
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestOf_writesTheKeysInAlphabeticalOrder(t *testing.T) {
	t.Parallel()
	written := string(canonical(businessOf(t, "25.00").fields()))
	keys := []string{"amount", "currency", "externalTransactionId", "gameId", "kind", "playerId", "providerId", "roundId", "walletId"}
	at := 0
	for _, key := range keys {
		found := strings.Index(written, `"`+key+`"`)
		if found < at {
			t.Fatalf("%s is at %d, want it past %d in %s", key, found, at, written)
		}
		at = found
	}
}

func TestOf_writesNoSpareSpace(t *testing.T) {
	t.Parallel()
	written := string(canonical(businessOf(t, "25.00").fields()))
	for _, spare := range []string{" ", "\n", "\t"} {
		if strings.Contains(written, spare) {
			t.Fatalf("canonical JSON = %q, want it without %q", written, spare)
		}
	}
}

// The provider identifiers are kept exactly as they arrived, so the canonical
// form must not escape them: a hash that depends on the escape would not match
// the one another channel takes of the same business.
func TestOf_leavesAngleBracketsAndAmpersandUnescaped(t *testing.T) {
	t.Parallel()
	business := businessOf(t, "25.00")
	business.RoundID = roundOf(t, "round<a>&b")
	written := string(canonical(business.fields()))
	if !strings.Contains(written, "round<a>&b") {
		t.Fatalf("canonical JSON = %s, want it carrying round<a>&b unescaped", written)
	}
	if strings.Contains(written, "\\u") {
		t.Fatalf("canonical JSON = %s, want no HTML escape in it", written)
	}
}

func TestOf_readsTwoSpellingsOfTheSameAmountAsOne(t *testing.T) {
	t.Parallel()
	if got, want := Of(businessOf(t, "25.0")), Of(businessOf(t, "25.00")); got != want {
		t.Fatalf("hash of 25.0 = %s, want the %s of 25.00", got, want)
	}
}

func TestOf_answersAnotherHashForAnotherAmount(t *testing.T) {
	t.Parallel()
	if got, other := Of(businessOf(t, "25.00")), Of(businessOf(t, "25.01")); got == other {
		t.Fatalf("hash of 25.00 = %s, want it apart from the one of 25.01", got)
	}
}

func TestOf_takesOurIdentifierInLowercase(t *testing.T) {
	t.Parallel()
	business := businessOf(t, "25.00")
	upper, err := identity.ParsePlayerID("3F8C4A2E-1B5D-4E7A-9C3F-2D6B8A1E5C40")
	if err != nil {
		t.Fatalf("ParsePlayerID of the uppercase identifier = %v, want nil", err)
	}
	business.PlayerID = upper
	written := string(canonical(business.fields()))
	if !strings.Contains(written, "3f8c4a2e-1b5d-4e7a-9c3f-2d6b8a1e5c40") {
		t.Fatalf("canonical JSON = %s, want the player in lowercase", written)
	}
}

func TestOf_leavesAnAbsentReferenceOutOfTheJSON(t *testing.T) {
	t.Parallel()
	written := string(canonical(businessOf(t, "25.00").fields()))
	if strings.Contains(written, "referenceExternalTransactionId") {
		t.Fatalf("canonical JSON = %s, want no cited operation in it", written)
	}
	if strings.Contains(written, "null") {
		t.Fatalf("canonical JSON = %s, want no null in it", written)
	}
}

func TestOf_carriesTheReferenceWhenThereIsOne(t *testing.T) {
	t.Parallel()
	business := businessOf(t, "25.00")
	cited, err := identity.ParseExternalTransactionID("bet-1")
	if err != nil {
		t.Fatalf("ParseExternalTransactionID of the reference = %v, want nil", err)
	}
	business.ReferenceExternalID = cited
	written := string(canonical(business.fields()))
	if !strings.Contains(written, `"referenceExternalTransactionId":"bet-1"`) {
		t.Fatalf("canonical JSON = %s, want the cited operation in it", written)
	}
	if Of(business) == Of(businessOf(t, "25.00")) {
		t.Fatalf("hash with a cited operation = %s, want it apart from the one without it", Of(business))
	}
}

// The key and the envelope are absent from the type itself, which is what keeps
// them out of every hash. The canonical form is checked for the names anyway, so
// that a field added later does not slip in unnoticed.
func TestOf_keepsTheKeyAndTheEnvelopeOut(t *testing.T) {
	t.Parallel()
	written := string(canonical(businessOf(t, "25.00").fields()))
	for _, outside := range []string{"idempotencyKey", "messageId", "occurredAt", "correlationId", "type"} {
		if strings.Contains(written, outside) {
			t.Fatalf("canonical JSON = %s, want it without %s", written, outside)
		}
	}
}

func TestOf_answersTheSameHashForTheSameBusiness(t *testing.T) {
	t.Parallel()
	if got, want := Of(businessOf(t, "25.00")), Of(businessOf(t, "25.00")); got != want {
		t.Fatalf("hash = %s, want the %s of the same business", got, want)
	}
}

func TestOf_answersSixtyFourHexadecimalCharacters(t *testing.T) {
	t.Parallel()
	got := Of(businessOf(t, "25.00"))
	if len(got) != 64 {
		t.Fatalf("hash length = %d, want 64", len(got))
	}
	if strings.Trim(got, "0123456789abcdef") != "" {
		t.Fatalf("hash = %s, want only lowercase hexadecimal", got)
	}
}

func businessOf(t *testing.T, amount string) Business {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	return Business{
		ProviderID: providerOf(t),
		ExternalID: externalOf(t),
		PlayerID:   playerOf(t),
		WalletID:   walletOf(t),
		RoundID:    roundOf(t, "round-1"),
		GameID:     gameOf(t),
		Kind:       wager.KindBet,
		Amount:     parsed,
	}
}

func providerOf(t *testing.T) identity.ProviderID {
	t.Helper()
	parsed, err := identity.ParseProviderID("provider-a")
	if err != nil {
		t.Fatalf("ParseProviderID = %v, want nil", err)
	}
	return parsed
}

func externalOf(t *testing.T) identity.ExternalTransactionID {
	t.Helper()
	parsed, err := identity.ParseExternalTransactionID("external-1")
	if err != nil {
		t.Fatalf("ParseExternalTransactionID in the helper = %v, want nil", err)
	}
	return parsed
}

func playerOf(t *testing.T) identity.PlayerID {
	t.Helper()
	parsed, err := identity.ParsePlayerID("3f8c4a2e-1b5d-4e7a-9c3f-2d6b8a1e5c40")
	if err != nil {
		t.Fatalf("ParsePlayerID in the helper = %v, want nil", err)
	}
	return parsed
}

func walletOf(t *testing.T) identity.WalletID {
	t.Helper()
	parsed, err := identity.ParseWalletID("6b1f0c8a-4d2e-4a7b-8c1d-9e3f5a7b2c4d")
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	return parsed
}

func roundOf(t *testing.T, text string) identity.RoundID {
	t.Helper()
	parsed, err := identity.ParseRoundID(text)
	if err != nil {
		t.Fatalf("ParseRoundID = %v, want nil", err)
	}
	return parsed
}

func gameOf(t *testing.T) identity.GameID {
	t.Helper()
	parsed, err := identity.ParseGameID("game-1")
	if err != nil {
		t.Fatalf("ParseGameID = %v, want nil", err)
	}
	return parsed
}

// fields has one branch, and it is the only field of the business that may be
// absent: an absent reference is left out of the map instead of carrying an empty
// value, so null never reaches the JSON.
func TestFields_leavesTheAbsentReferenceOutOfTheMapAndCarriesThePresentOne(t *testing.T) {
	t.Parallel()
	const key = "referenceExternalTransactionId"
	absent := businessOf(t, "25.00").fields()
	if _, ok := absent[key]; ok {
		t.Fatalf("map of a business citing nothing = %v, want %s left out", absent, key)
	}
	if len(absent) != 9 {
		t.Fatalf("keys of a business citing nothing = %d, want 9", len(absent))
	}
	citing := businessOf(t, "25.00")
	citing.ReferenceExternalID = externalOf(t)
	present := citing.fields()
	if present[key] != "external-1" {
		t.Fatalf("map of a business citing an operation = %v, want %s carried", present, key)
	}
}
