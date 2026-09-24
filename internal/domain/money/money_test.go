package money

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func brl(t *testing.T) Currency {
	t.Helper()
	currency, err := ParseCurrency("BRL")
	if err != nil {
		t.Fatalf("ParseCurrency for the test fixture error = %v, want nil", err)
	}
	return currency
}

func TestParse_keepsCentsAndCurrencyTogether(t *testing.T) {
	t.Parallel()
	amount, err := Parse("25.00", "BRL")
	if err != nil {
		t.Fatalf("Parse of a valid pair error = %v, want nil", err)
	}
	if amount.Cents() != 2500 {
		t.Fatalf("cents = %d, want 2500", amount.Cents())
	}
	if amount.Currency().Code() != "BRL" {
		t.Fatalf("currency = %q, want BRL", amount.Currency().Code())
	}
}

func TestParse_refusesTheCurrencyBeforeTheAmount(t *testing.T) {
	t.Parallel()
	amount, err := Parse("25.00", "brl")
	if !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("Parse with a lowercase currency error = %v, want ErrInvalidCurrency", err)
	}
	if !amount.Currency().IsZero() {
		t.Fatalf("refused pair kept the currency %q, want the zero value", amount.Currency().Code())
	}
}

func TestParse_refusesAnAmountThatIsNotADecimalString(t *testing.T) {
	t.Parallel()
	amount, err := Parse("25.005", "BRL")
	if !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("Parse with three decimal places error = %v, want ErrInvalidAmount", err)
	}
	if amount.Cents() != 0 {
		t.Fatalf("refused amount produced %d cents, want none", amount.Cents())
	}
}

func TestFromCents_needsACurrencyAndAcceptsANegativeDifference(t *testing.T) {
	t.Parallel()
	difference, err := FromCents(-1500, brl(t))
	if err != nil {
		t.Fatalf("FromCents with a negative difference error = %v, want nil", err)
	}
	if difference.Cents() != -1500 {
		t.Fatalf("difference = %d cents, want -1500", difference.Cents())
	}
	var unset Currency
	_, err = FromCents(2500, unset)
	if !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("FromCents without a currency error = %v, want ErrInvalidCurrency", err)
	}
}

func TestZero_buildsTheEmptyBalance(t *testing.T) {
	t.Parallel()
	empty, err := Zero(brl(t))
	if err != nil {
		t.Fatalf("Zero error = %v, want nil", err)
	}
	if !empty.IsZero() {
		t.Fatalf("Zero() = %d cents, want 0", empty.Cents())
	}
}

func TestIsPositive_answersForEachSideOfZero(t *testing.T) {
	t.Parallel()
	currency := brl(t)
	cases := []struct {
		name         string
		cents        int64
		wantPositive bool
		wantNegative bool
	}{
		{name: "a credit is positive", cents: 2500, wantPositive: true, wantNegative: false},
		{name: "zero is neither side", cents: 0, wantPositive: false, wantNegative: false},
		{name: "a difference is negative", cents: -2500, wantPositive: false, wantNegative: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			amount, err := FromCents(testCase.cents, currency)
			if err != nil {
				t.Fatalf("FromCents(%d) error = %v, want nil", testCase.cents, err)
			}
			if amount.IsPositive() != testCase.wantPositive {
				t.Fatalf("IsPositive() on %d = %t, want %t", testCase.cents, amount.IsPositive(), testCase.wantPositive)
			}
			if amount.IsNegative() != testCase.wantNegative {
				t.Fatalf("IsNegative() on %d = %t, want %t", testCase.cents, amount.IsNegative(), testCase.wantNegative)
			}
		})
	}
}

func TestAmount_keepsTwoDecimalPlacesForZero(t *testing.T) {
	t.Parallel()
	empty, err := Zero(brl(t))
	if err != nil {
		t.Fatalf("Zero before Amount error = %v, want nil", err)
	}
	if empty.Amount() != "0.00" {
		t.Fatalf("Amount() of zero = %q, want %q", empty.Amount(), "0.00")
	}
}

func TestString_joinsTheAmountAndTheCurrency(t *testing.T) {
	t.Parallel()
	amount, err := Parse("1975.50", "BRL")
	if err != nil {
		t.Fatalf("Parse before String error = %v, want nil", err)
	}
	if amount.String() != "1975.50 BRL" {
		t.Fatalf("String() = %q, want %q", amount.String(), "1975.50 BRL")
	}
}

func TestMarshalJSON_writesTheExternalContract(t *testing.T) {
	t.Parallel()
	amount, err := Parse("25.00", "BRL")
	if err != nil {
		t.Fatalf("Parse before MarshalJSON error = %v, want nil", err)
	}
	encoded, err := json.Marshal(amount)
	if err != nil {
		t.Fatalf("json.Marshal error = %v, want nil", err)
	}
	const want = `{"amount":"25.00","currency":"BRL"}`
	if string(encoded) != want {
		t.Fatalf("encoded money = %s, want %s", encoded, want)
	}
}

func TestAmount_roundTripsTheCentsThroughTheStringForm(t *testing.T) {
	t.Parallel()
	original, err := FromCents(197550, brl(t))
	if err != nil {
		t.Fatalf("FromCents before the round trip error = %v, want nil", err)
	}
	parsed, err := Parse(original.Amount(), original.Currency().Code())
	if err != nil {
		t.Fatalf("Parse of the serialized amount error = %v, want nil", err)
	}
	if parsed.Cents() != original.Cents() {
		t.Fatalf("round trip kept %d cents, want %d", parsed.Cents(), original.Cents())
	}
	if parsed.Currency() != original.Currency() {
		t.Fatalf("round trip kept the currency %q, want %q", parsed.Currency().Code(), original.Currency().Code())
	}
}

func TestParse_refusesTheAmountAtTheInt64Boundary(t *testing.T) {
	t.Parallel()
	limit, err := FromCents(math.MaxInt64, brl(t))
	if err != nil {
		t.Fatalf("FromCents at the limit error = %v, want nil", err)
	}
	reparsed, err := Parse(limit.Amount(), "BRL")
	if err != nil {
		t.Fatalf("Parse of the largest amount error = %v, want nil", err)
	}
	if reparsed.Cents() != math.MaxInt64 {
		t.Fatalf("largest amount reparsed as %d cents, want %d", reparsed.Cents(), int64(math.MaxInt64))
	}
}
