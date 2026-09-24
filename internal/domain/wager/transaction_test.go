package wager

import (
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

const (
	transactionUUID = "11111111-1111-4111-8111-111111111111"
	playerUUID      = "22222222-2222-4222-8222-222222222222"
	walletUUID      = "33333333-3333-4333-8333-333333333333"
)

func at(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, time.March, 14, 10, 30, 0, 0, time.UTC)
}

func amountIn(t *testing.T, cents int64, code string) money.Money {
	t.Helper()
	currency, err := money.ParseCurrency(code)
	if err != nil {
		t.Fatalf("ParseCurrency(%q) for the fixture error = %v, want nil", code, err)
	}
	value, err := money.FromCents(cents, currency)
	if err != nil {
		t.Fatalf("FromCents(%d, %s) for the fixture error = %v, want nil", cents, code, err)
	}
	return value
}

func brl(t *testing.T, cents int64) money.Money {
	t.Helper()
	return amountIn(t, cents, "BRL")
}

func transactionOf(t *testing.T) identity.TransactionID {
	t.Helper()
	parsed, err := identity.ParseTransactionID(transactionUUID)
	if err != nil {
		t.Fatalf("ParseTransactionID for the fixture error = %v, want nil", err)
	}
	return parsed
}

func playerOf(t *testing.T) identity.PlayerID {
	t.Helper()
	parsed, err := identity.ParsePlayerID(playerUUID)
	if err != nil {
		t.Fatalf("ParsePlayerID for the fixture error = %v, want nil", err)
	}
	return parsed
}

func walletOf(t *testing.T) identity.WalletID {
	t.Helper()
	parsed, err := identity.ParseWalletID(walletUUID)
	if err != nil {
		t.Fatalf("ParseWalletID for the fixture error = %v, want nil", err)
	}
	return parsed
}

func tokenOf(t *testing.T, text string) identity.ExternalTransactionID {
	t.Helper()
	parsed, err := identity.ParseExternalTransactionID(text)
	if err != nil {
		t.Fatalf("ParseExternalTransactionID(%q) for the fixture error = %v, want nil", text, err)
	}
	return parsed
}

func betSpec(t *testing.T) ExternalSpec {
	t.Helper()
	provider, err := identity.ParseProviderID("provider-a")
	if err != nil {
		t.Fatalf("ParseProviderID for the fixture error = %v, want nil", err)
	}
	key, err := identity.ParseIdempotencyKey("key-001")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey for the fixture error = %v, want nil", err)
	}
	round, err := identity.ParseRoundID("round-42")
	if err != nil {
		t.Fatalf("ParseRoundID for the fixture error = %v, want nil", err)
	}
	game, err := identity.ParseGameID("crash")
	if err != nil {
		t.Fatalf("ParseGameID for the fixture error = %v, want nil", err)
	}
	return ExternalSpec{
		ID:             transactionOf(t),
		ProviderID:     provider,
		ExternalID:     tokenOf(t, "tx-001"),
		IdempotencyKey: key,
		BodyHash:       "6b86b273ff34fce19d6b804eff5a3f57",
		PlayerID:       playerOf(t),
		WalletID:       walletOf(t),
		RoundID:        round,
		GameID:         game,
		Kind:           KindBet,
		Amount:         brl(t, 2500),
		At:             at(t),
	}
}

func mustExternal(t *testing.T, spec ExternalSpec) *Transaction {
	t.Helper()
	built, err := NewExternal(spec)
	if err != nil {
		t.Fatalf("NewExternal for the fixture error = %v, want nil", err)
	}
	return built
}

func rejectionCode(t *testing.T, err error) FailureCode {
	t.Helper()
	var rejection Rejection
	if !errors.As(err, &rejection) {
		t.Fatalf("error = %v, want a Rejection carrying a catalog token", err)
	}
	return rejection.Code()
}

func TestNewExternal_isBornPendingWithTheProviderIdentity(t *testing.T) {
	t.Parallel()
	built := mustExternal(t, betSpec(t))
	if built.Status() != Pending {
		t.Fatalf("status = %s, want PENDING", built.Status())
	}
	if built.Kind() != KindBet {
		t.Fatalf("kind = %s, want BET", built.Kind())
	}
	if built.ProviderID().String() != "provider-a" {
		t.Fatalf("provider = %s, want provider-a", built.ProviderID())
	}
	if built.Amount().Amount() != "25.00" {
		t.Fatalf("amount = %s, want 25.00", built.Amount().Amount())
	}
}

func TestNewExternal_refusesAnOpeningArrivingFromOutside(t *testing.T) {
	t.Parallel()
	spec := betSpec(t)
	spec.Kind = KindOpening
	built, err := NewExternal(spec)
	if got := rejectionCode(t, err); got != OpeningNotAllowed {
		t.Fatalf("rejection = %s, want OPENING_NOT_ALLOWED", got)
	}
	if built != nil {
		t.Fatalf("a refused opening produced a transaction, want none")
	}
}

func TestNewExternal_refusesTheOperationMissingAProviderField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		spoil func(*ExternalSpec)
	}{
		{name: "without a round", spoil: func(s *ExternalSpec) { s.RoundID = identity.RoundID{} }},
		{name: "without a game", spoil: func(s *ExternalSpec) { s.GameID = identity.GameID{} }},
		{name: "without a provider", spoil: func(s *ExternalSpec) { s.ProviderID = identity.ProviderID{} }},
		{name: "without an external id", spoil: func(s *ExternalSpec) { s.ExternalID = identity.ExternalTransactionID{} }},
		{name: "without an idempotency key", spoil: func(s *ExternalSpec) { s.IdempotencyKey = identity.IdempotencyKey{} }},
		{name: "without a body hash", spoil: func(s *ExternalSpec) { s.BodyHash = "" }},
		{name: "without a transaction id", spoil: func(s *ExternalSpec) { s.ID = identity.TransactionID{} }},
		{name: "without a player", spoil: func(s *ExternalSpec) { s.PlayerID = identity.PlayerID{} }},
		{name: "without a wallet", spoil: func(s *ExternalSpec) { s.WalletID = identity.WalletID{} }},
		{name: "without an instant", spoil: func(s *ExternalSpec) { s.At = time.Time{} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			spec := betSpec(t)
			testCase.spoil(&spec)
			built, err := NewExternal(spec)
			if !errors.Is(err, ErrIncompleteTransaction) {
				t.Fatalf("NewExternal %s error = %v, want ErrIncompleteTransaction", testCase.name, err)
			}
			if built != nil {
				t.Fatalf("NewExternal %s produced a transaction, want none", testCase.name)
			}
		})
	}
}

func TestNewExternal_refusesAnUnknownKind(t *testing.T) {
	t.Parallel()
	spec := betSpec(t)
	spec.Kind = Kind(200)
	if _, err := NewExternal(spec); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("NewExternal with an unknown kind error = %v, want ErrUnknownKind", err)
	}
}

func TestCheckAmountForKind_bindsTheAmountToTheKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		kind  Kind
		cents int64
		want  FailureCode
	}{
		{name: "a LOSS carrying ten reais is refused", kind: KindLoss, cents: 1000, want: AmountNotAllowedForKind},
		{name: "a BET of zero is refused", kind: KindBet, cents: 0, want: AmountNotAllowedForKind},
		{name: "a WIN of zero is refused", kind: KindWin, cents: 0, want: AmountNotAllowedForKind},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			spec := betSpec(t)
			spec.Kind = testCase.kind
			spec.Amount = brl(t, testCase.cents)
			built, err := NewExternal(spec)
			if got := rejectionCode(t, err); got != testCase.want {
				t.Fatalf("rejection for %s = %s, want %s", testCase.name, got, testCase.want)
			}
			if built != nil {
				t.Fatalf("the refusal for %s produced a transaction, want none", testCase.name)
			}
		})
	}
}

func TestCheckAmountForKind_acceptsALossOfZero(t *testing.T) {
	t.Parallel()
	spec := betSpec(t)
	spec.Kind = KindLoss
	spec.Amount = brl(t, 0)
	built := mustExternal(t, spec)
	if !built.Amount().IsZero() {
		t.Fatalf("LOSS amount = %s, want 0.00", built.Amount().Amount())
	}
}

func TestCheckReferenceForKind_demandsTheCitedOperationOnAReversal(t *testing.T) {
	t.Parallel()
	for _, kind := range []Kind{KindRefund, KindRollback} {
		t.Run(kind.String()+" without a reference is refused", func(t *testing.T) {
			spec := betSpec(t)
			spec.Kind = kind
			built, err := NewExternal(spec)
			if got := rejectionCode(t, err); got != ReferenceRequired {
				t.Fatalf("rejection for %s = %s, want REFERENCE_REQUIRED", kind, got)
			}
			if built != nil {
				t.Fatalf("a %s without a reference produced a transaction, want none", kind)
			}
		})
	}
}

func TestReferenceExternalID_reportsWhetherTheOperationCitesAnother(t *testing.T) {
	t.Parallel()
	plain := mustExternal(t, betSpec(t))
	if _, ok := plain.ReferenceExternalID(); ok {
		t.Fatalf("a plain BET reported a cited operation, want none")
	}
	spec := betSpec(t)
	spec.Kind = KindRefund
	spec.ReferenceExternalID = tokenOf(t, "tx-000")
	reversal := mustExternal(t, spec)
	cited, ok := reversal.ReferenceExternalID()
	if !ok {
		t.Fatalf("a REFUND reported no cited operation, want one")
	}
	if cited.String() != "tx-000" {
		t.Fatalf("cited operation = %s, want tx-000", cited)
	}
}

func TestNewOpening_carriesNoProviderField(t *testing.T) {
	t.Parallel()
	built, err := NewOpening(OpeningSpec{
		ID:       transactionOf(t),
		PlayerID: playerOf(t),
		WalletID: walletOf(t),
		Amount:   brl(t, 100000),
		At:       at(t),
	})
	if err != nil {
		t.Fatalf("NewOpening error = %v, want nil", err)
	}
	if built.Kind() != KindOpening {
		t.Fatalf("opening kind = %s, want OPENING", built.Kind())
	}
	if built.Status() != Pending {
		t.Fatalf("opening status = %s, want PENDING", built.Status())
	}
	assertNoProviderField(t, built)
}

func assertNoProviderField(t *testing.T, built *Transaction) {
	t.Helper()
	empty := map[string]bool{
		"provider":        built.ProviderID().IsZero(),
		"external id":     built.ExternalID().IsZero(),
		"idempotency key": built.IdempotencyKey().IsZero(),
		"body hash":       built.BodyHash() == "",
		"round":           built.RoundID().IsZero(),
		"game":            built.GameID().IsZero(),
	}
	for field, isEmpty := range empty {
		if !isEmpty {
			t.Fatalf("the opening carries a %s, want none", field)
		}
	}
}

func TestNewOpening_refusesTheIncompleteOrEmptyOpening(t *testing.T) {
	t.Parallel()
	complete := OpeningSpec{
		ID:       transactionOf(t),
		PlayerID: playerOf(t),
		WalletID: walletOf(t),
		Amount:   brl(t, 100000),
		At:       at(t),
	}
	spec := complete
	spec.WalletID = identity.WalletID{}
	if _, err := NewOpening(spec); !errors.Is(err, ErrIncompleteTransaction) {
		t.Fatalf("NewOpening without a wallet error = %v, want ErrIncompleteTransaction", err)
	}
	empty := complete
	empty.Amount = brl(t, 0)
	if got := rejectionCode(t, mustFail(t, empty)); got != AmountNotAllowedForKind {
		t.Fatalf("rejection for an opening at zero = %s, want AMOUNT_NOT_ALLOWED_FOR_KIND", got)
	}
}

func mustFail(t *testing.T, spec OpeningSpec) error {
	t.Helper()
	built, err := NewOpening(spec)
	if err == nil {
		t.Fatalf("NewOpening error = nil, want a refusal")
	}
	if built != nil {
		t.Fatalf("a refused opening produced a transaction, want none")
	}
	return err
}
