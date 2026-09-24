package money

import (
	"errors"
	"math"
	"testing"
)

func amountIn(t *testing.T, cents int64, code string) Money {
	t.Helper()
	currency, err := ParseCurrency(code)
	if err != nil {
		t.Fatalf("ParseCurrency(%q) for the fixture error = %v, want nil", code, err)
	}
	amount, err := FromCents(cents, currency)
	if err != nil {
		t.Fatalf("FromCents(%d, %s) for the fixture error = %v, want nil", cents, code, err)
	}
	return amount
}

func TestAdd_sumsWithinTheSameCurrency(t *testing.T) {
	t.Parallel()
	sum, err := amountIn(t, 10000, "BRL").Add(amountIn(t, 2500, "BRL"))
	if err != nil {
		t.Fatalf("Add within one currency error = %v, want nil", err)
	}
	if sum.Cents() != 12500 {
		t.Fatalf("sum = %d cents, want 12500", sum.Cents())
	}
}

func TestAdd_refusesTheSumPastTheInt64Limit(t *testing.T) {
	t.Parallel()
	sum, err := amountIn(t, math.MaxInt64, "BRL").Add(amountIn(t, 1, "BRL"))
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("Add one cent past the limit error = %v, want ErrOverflow", err)
	}
	if sum.IsNegative() {
		t.Fatalf("overflowing sum wrapped to %d cents, want no value at all", sum.Cents())
	}
	if sum.Cents() != 0 {
		t.Fatalf("overflowing sum produced %d cents, want none", sum.Cents())
	}
}

func TestAdd_refusesTwoDifferentCurrencies(t *testing.T) {
	t.Parallel()
	sum, err := amountIn(t, 10000, "BRL").Add(amountIn(t, 2500, "USD"))
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("BRL plus USD error = %v, want ErrCurrencyMismatch", err)
	}
	if !sum.Currency().IsZero() {
		t.Fatalf("mismatched sum kept the currency %q, want the zero value", sum.Currency().Code())
	}
}

func TestSub_producesTheNegativeInternalDifference(t *testing.T) {
	t.Parallel()
	difference, err := amountIn(t, 1000, "BRL").Sub(amountIn(t, 2500, "BRL"))
	if err != nil {
		t.Fatalf("Sub below zero error = %v, want nil", err)
	}
	if difference.Cents() != -1500 {
		t.Fatalf("difference = %d cents, want -1500", difference.Cents())
	}
	if _, err := Parse(difference.Amount(), "BRL"); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("Parse of a negative difference error = %v, want ErrInvalidAmount", err)
	}
}

func TestSub_refusesTheDifferencePastTheInt64Limit(t *testing.T) {
	t.Parallel()
	difference, err := amountIn(t, math.MinInt64, "BRL").Sub(amountIn(t, 1, "BRL"))
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("Sub one cent below the limit error = %v, want ErrOverflow", err)
	}
	if difference.Cents() != 0 {
		t.Fatalf("underflowing difference produced %d cents, want none", difference.Cents())
	}
}

func TestSub_refusesTwoDifferentCurrencies(t *testing.T) {
	t.Parallel()
	_, err := amountIn(t, 10000, "BRL").Sub(amountIn(t, 2500, "USD"))
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("BRL minus USD error = %v, want ErrCurrencyMismatch", err)
	}
}

func TestNeg_flipsTheSignAndKeepsTheCurrency(t *testing.T) {
	t.Parallel()
	negated, err := amountIn(t, 2500, "BRL").Neg()
	if err != nil {
		t.Fatalf("Neg of a positive amount error = %v, want nil", err)
	}
	if negated.Cents() != -2500 {
		t.Fatalf("negated = %d cents, want -2500", negated.Cents())
	}
	if negated.Currency().Code() != "BRL" {
		t.Fatalf("negated currency = %q, want BRL", negated.Currency().Code())
	}
}

func TestNeg_refusesTheSmallestInt64(t *testing.T) {
	t.Parallel()
	negated, err := amountIn(t, math.MinInt64, "BRL").Neg()
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("Neg of the smallest int64 error = %v, want ErrOverflow", err)
	}
	if negated.Cents() != 0 {
		t.Fatalf("refused negation produced %d cents, want none", negated.Cents())
	}
}

func TestCmp_ordersWithinTheSameCurrency(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		left  int64
		right int64
		want  int
	}{
		{name: "a smaller amount comes first", left: 1000, right: 2500, want: -1},
		{name: "a larger amount comes last", left: 2500, right: 1000, want: 1},
		{name: "equal amounts tie", left: 2500, right: 2500, want: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := amountIn(t, testCase.left, "BRL").Cmp(amountIn(t, testCase.right, "BRL"))
			if err != nil {
				t.Fatalf("Cmp(%d, %d) error = %v, want nil", testCase.left, testCase.right, err)
			}
			if got != testCase.want {
				t.Fatalf("Cmp(%d, %d) = %d, want %d", testCase.left, testCase.right, got, testCase.want)
			}
		})
	}
}

func TestCmp_refusesTwoDifferentCurrencies(t *testing.T) {
	t.Parallel()
	got, err := amountIn(t, 2500, "BRL").Cmp(amountIn(t, 2500, "USD"))
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("BRL compared to USD error = %v, want ErrCurrencyMismatch", err)
	}
	if got != 0 {
		t.Fatalf("mismatched comparison = %d, want 0", got)
	}
}

func TestEqual_takesTheCurrencyIntoAccount(t *testing.T) {
	t.Parallel()
	if !amountIn(t, 2500, "BRL").Equal(amountIn(t, 2500, "BRL")) {
		t.Fatalf("two identical BRL amounts compared unequal, want equal")
	}
	if amountIn(t, 2500, "BRL").Equal(amountIn(t, 2500, "USD")) {
		t.Fatalf("the same cents in two currencies compared equal, want unequal")
	}
	if amountIn(t, 2500, "BRL").Equal(amountIn(t, 1000, "BRL")) {
		t.Fatalf("two different BRL amounts compared equal, want unequal")
	}
}

func TestMulInt64_shortCircuitsOnZeroAndCatchesOverflow(t *testing.T) {
	t.Parallel()
	product, err := mulInt64(0, math.MaxInt64)
	if err != nil {
		t.Fatalf("mulInt64 with a zero factor error = %v, want nil", err)
	}
	if product != 0 {
		t.Fatalf("mulInt64 with a zero factor = %d, want 0", product)
	}
	if _, err := mulInt64(math.MaxInt64, 2); !errors.Is(err, ErrOverflow) {
		t.Fatalf("mulInt64 past the limit error = %v, want ErrOverflow", err)
	}
}
