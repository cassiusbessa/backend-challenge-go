package wager

import (
	"errors"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

func TestCatalog_listsEverySixteenTokenOnce(t *testing.T) {
	t.Parallel()
	catalog := Catalog()
	if len(catalog) != 16 {
		t.Fatalf("catalog holds %d codes, want 16", len(catalog))
	}
	seen := map[string]bool{}
	for _, code := range catalog {
		token := code.String()
		if token == "" {
			t.Fatalf("code %d carries no token, want one", code)
		}
		if seen[token] {
			t.Fatalf("token %s appears twice in the catalog, want once", token)
		}
		seen[token] = true
	}
}

// Every token of the closed catalog needs a scenario that produces it. A token
// nobody produces is one the border maps to no status and the metric never
// counts, which is what the closed list exists to prevent — so adding a token
// without a scenario fails here.
func TestCatalog_hasAScenarioForEveryToken(t *testing.T) {
	t.Parallel()
	scenarios := domainScenarios()
	elsewhere := producedAboveTheDomain()
	for _, code := range Catalog() {
		scenario, inDomain := scenarios[code]
		if !inDomain {
			if _, noted := elsewhere[code]; !noted {
				t.Fatalf("token %s has no scenario in this package and is not noted as produced elsewhere", code)
			}
			continue
		}
		t.Run(code.String(), func(t *testing.T) {
			if got := rejectionCode(t, scenario(t)); got != code {
				t.Fatalf("the scenario for %s produced %s instead", code, got)
			}
		})
	}
}

func domainScenarios() map[FailureCode]func(*testing.T) error {
	return map[FailureCode]func(*testing.T) error{
		InsufficientFunds:         refusedBet,
		ReversalInsufficientFunds: refusedRollback,
		ReferenceUnsuccessful:     scenarioReferenceUnsuccessful,
		AlreadyReversed:           scenarioAlreadyReversed,
		PlayerWalletMismatch:      scenarioPlayerWalletMismatch,
		CurrencyMismatch:          scenarioCurrencyMismatch,
		ReversalAmountMismatch:    scenarioReversalAmountMismatch,
		ReferenceMismatch:         scenarioReferenceMismatch,
		WalletNotFound:            scenarioWalletNotFound,
		OpeningNotAllowed:         scenarioOpeningNotAllowed,
		AmountNotAllowedForKind:   scenarioAmountNotAllowedForKind,
		ReferenceRequired:         scenarioReferenceRequired,
	}
}

// These four refuse a wager operation, so they belong to the catalog, but no
// function of this package can produce one: two are translated from a unique
// violation by the use case, and two are the reference worker closing a wait
// on the clock.
func producedAboveTheDomain() map[FailureCode]string {
	return map[FailureCode]string{
		IdempotencyConflict:          "internal/app, translating the unique violation on the key",
		DuplicateExternalTransaction: "internal/app, translating the unique violation on the provider pair",
		ReferenceNotFound:            "the reference worker, when the deadline passes with nothing cited",
		ReferenceNotProcessed:        "the reference worker, when the deadline passes with the cited operation unfinished",
	}
}

func scenarioReferenceUnsuccessful(t *testing.T) error {
	t.Helper()
	cited := operation(t, KindBet, 2500)
	if err := cited.Reject(InsufficientFunds, later(t)); err != nil {
		t.Fatalf("Reject of the cited bet for the scenario error = %v, want nil", err)
	}
	_, err := Win(openedWallet(t, 97500), citingWin(t), Reference{Cited: cited}, movement(t, "2"))
	return err
}

func scenarioAlreadyReversed(t *testing.T) error {
	t.Helper()
	reference := Reference{Cited: citedProcessed(t, KindBet, 2500), AlreadyReversed: true}
	_, err := Refund(openedWallet(t, 97500), operation(t, KindRefund, 2500), reference, movement(t, "2"))
	return err
}

func scenarioPlayerWalletMismatch(t *testing.T) error {
	t.Helper()
	stranger, err := identity.ParsePlayerID(otherPlayerUUID)
	if err != nil {
		t.Fatalf("ParsePlayerID for the scenario error = %v, want nil", err)
	}
	spec := betSpec(t)
	spec.PlayerID = stranger
	_, err = Bet(openedWallet(t, 100000), mustExternal(t, spec), movement(t, "2"))
	return err
}

func scenarioCurrencyMismatch(t *testing.T) error {
	t.Helper()
	spec := betSpec(t)
	spec.Amount = amountIn(t, 2500, "USD")
	_, err := Bet(openedWallet(t, 100000), mustExternal(t, spec), movement(t, "2"))
	return err
}

func scenarioReversalAmountMismatch(t *testing.T) error {
	t.Helper()
	reference := Reference{Cited: citedProcessed(t, KindBet, 2500)}
	_, err := Refund(openedWallet(t, 97500), operation(t, KindRefund, 1000), reference, movement(t, "2"))
	return err
}

func scenarioReferenceMismatch(t *testing.T) error {
	t.Helper()
	reference := Reference{Cited: citedProcessed(t, KindWin, 2500)}
	_, err := Win(openedWallet(t, 97500), citingWin(t), reference, movement(t, "2"))
	return err
}

func scenarioWalletNotFound(t *testing.T) error {
	t.Helper()
	_, err := Bet(nil, operation(t, KindBet, 2500), movement(t, "2"))
	return err
}

func scenarioOpeningNotAllowed(t *testing.T) error {
	t.Helper()
	spec := betSpec(t)
	spec.Kind = KindOpening
	_, err := NewExternal(spec)
	return err
}

func scenarioAmountNotAllowedForKind(t *testing.T) error {
	t.Helper()
	spec := betSpec(t)
	spec.Kind = KindLoss
	spec.Amount = brl(t, 1000)
	_, err := NewExternal(spec)
	return err
}

func scenarioReferenceRequired(t *testing.T) error {
	t.Helper()
	spec := betSpec(t)
	spec.Kind = KindRefund
	_, err := NewExternal(spec)
	return err
}

func TestString_writesTheTokenOfTheSpec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code FailureCode
		want string
	}{
		{code: InsufficientFunds, want: "INSUFFICIENT_FUNDS"},
		{code: ReversalInsufficientFunds, want: "REVERSAL_INSUFFICIENT_FUNDS"},
		{code: ReferenceNotFound, want: "REFERENCE_NOT_FOUND"},
		{code: ReferenceNotProcessed, want: "REFERENCE_NOT_PROCESSED"},
		{code: ReferenceUnsuccessful, want: "REFERENCE_UNSUCCESSFUL"},
		{code: AlreadyReversed, want: "ALREADY_REVERSED"},
		{code: PlayerWalletMismatch, want: "PLAYER_WALLET_MISMATCH"},
		{code: CurrencyMismatch, want: "CURRENCY_MISMATCH"},
		{code: ReversalAmountMismatch, want: "REVERSAL_AMOUNT_MISMATCH"},
		{code: ReferenceMismatch, want: "REFERENCE_MISMATCH"},
		{code: IdempotencyConflict, want: "IDEMPOTENCY_CONFLICT"},
		{code: DuplicateExternalTransaction, want: "DUPLICATE_EXTERNAL_TRANSACTION"},
		{code: WalletNotFound, want: "WALLET_NOT_FOUND"},
		{code: OpeningNotAllowed, want: "OPENING_NOT_ALLOWED"},
		{code: AmountNotAllowedForKind, want: "AMOUNT_NOT_ALLOWED_FOR_KIND"},
		{code: ReferenceRequired, want: "REFERENCE_REQUIRED"},
	}
	if len(cases) != len(Catalog()) {
		t.Fatalf("the table names %d tokens, want the %d of the catalog", len(cases), len(Catalog()))
	}
	for _, testCase := range cases {
		t.Run(testCase.want, func(t *testing.T) {
			if got := testCase.code.String(); got != testCase.want {
				t.Fatalf("String() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestString_answersEmptyOutsideTheCatalog(t *testing.T) {
	t.Parallel()
	if got := noFailureCode.String(); got != "" {
		t.Fatalf("the zero code answered %q, want the empty string", got)
	}
	if got := FailureCode(200).String(); got != "" {
		t.Fatalf("a code past the catalog answered %q, want the empty string", got)
	}
	if got := failureCodeCount.String(); got != "" {
		t.Fatalf("the code at the catalog limit answered %q, want the empty string", got)
	}
}

func TestIsZero_answersForTheUnsetCode(t *testing.T) {
	t.Parallel()
	if !noFailureCode.IsZero() {
		t.Fatalf("the zero code reported IsZero() = false, want true")
	}
	if InsufficientFunds.IsZero() {
		t.Fatalf("a catalog code reported IsZero() = true, want false")
	}
}

func TestParseFailureCode_readsTheTokenBack(t *testing.T) {
	t.Parallel()
	code, err := ParseFailureCode("ALREADY_REVERSED")
	if err != nil {
		t.Fatalf("ParseFailureCode error = %v, want nil", err)
	}
	if code != AlreadyReversed {
		t.Fatalf("parsed code = %s, want ALREADY_REVERSED", code)
	}
	unknown, err := ParseFailureCode("SOMETHING_ELSE")
	if !errors.Is(err, ErrUnknownFailureCode) {
		t.Fatalf("ParseFailureCode of a token outside the catalog error = %v, want ErrUnknownFailureCode", err)
	}
	if !unknown.IsZero() {
		t.Fatalf("a refused token produced code %s, want the zero value", unknown)
	}
}

func TestNewRejection_exposesTheTokenThroughErrorsAs(t *testing.T) {
	t.Parallel()
	err := NewRejection(InsufficientFunds, nil)
	var rejection Rejection
	if !errors.As(err, &rejection) {
		t.Fatalf("errors.As on the rejection = false, want true")
	}
	if rejection.Code() != InsufficientFunds {
		t.Fatalf("rejection code = %s, want INSUFFICIENT_FUNDS", rejection.Code())
	}
	if !strings.Contains(err.Error(), "INSUFFICIENT_FUNDS") {
		t.Fatalf("rejection message = %q, want it to name the token", err.Error())
	}
}

func TestNewRejection_refusesATokenOutsideTheCatalog(t *testing.T) {
	t.Parallel()
	err := NewRejection(FailureCode(200), nil)
	if !errors.Is(err, ErrUnknownFailureCode) {
		t.Fatalf("NewRejection with an unknown code error = %v, want ErrUnknownFailureCode", err)
	}
	var rejection Rejection
	if errors.As(err, &rejection) {
		t.Fatalf("an unknown code produced a Rejection, want none")
	}
	if err := NewRejection(noFailureCode, nil); !errors.Is(err, ErrUnknownFailureCode) {
		t.Fatalf("NewRejection with the zero code error = %v, want ErrUnknownFailureCode", err)
	}
}

func TestUnwrap_keepsTheConditionThatProducedTheRejection(t *testing.T) {
	t.Parallel()
	cause := errors.New("wallet: balance does not cover the debit")
	err := NewRejection(InsufficientFunds, cause)
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is on the cause = false, want true")
	}
	var rejection Rejection
	if !errors.As(err, &rejection) || rejection.Code() != InsufficientFunds {
		t.Fatalf("the wrapped rejection lost its token, want INSUFFICIENT_FUNDS")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("rejection message = %q, want a single line", err.Error())
	}
}
