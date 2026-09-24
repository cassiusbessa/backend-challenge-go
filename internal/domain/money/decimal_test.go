package money

import (
	"errors"
	"math"
	"strconv"
	"testing"
)

func TestParseCents_normalizesUpToTwoDecimalPlaces(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want int64
	}{
		{name: "two places become cents", text: "25.00", want: 2500},
		{name: "one place is normalized", text: "25.5", want: 2550},
		{name: "no separator means no cents", text: "25", want: 2500},
		{name: "zero keeps the scale", text: "0.00", want: 0},
		{name: "a single cent survives", text: "0.01", want: 1},
		{name: "leading zeros do not change the value", text: "0025.50", want: 2550},
		{name: "the largest int64 amount fits", text: "92233720368547758.07", want: math.MaxInt64},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseCents(testCase.text)
			if err != nil {
				t.Fatalf("parseCents(%q) error = %v, want nil", testCase.text, err)
			}
			if got != testCase.want {
				t.Fatalf("parseCents(%q) = %d, want %d", testCase.text, got, testCase.want)
			}
		})
	}
}

func TestParseCents_refusesWhatIsNotAFixedScaleDecimal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{name: "empty string carries no amount", text: ""},
		{name: "NaN is not a decimal", text: "NaN"},
		{name: "Infinity is not a decimal", text: "Infinity"},
		{name: "scientific notation is refused", text: "2.5e1"},
		{name: "a negative amount is refused", text: "-25.00"},
		{name: "an explicit plus sign is refused", text: "+25.00"},
		{name: "three decimal places exceed the scale", text: "25.005"},
		{name: "a trailing separator has no fraction", text: "25."},
		{name: "a leading separator has no units", text: ".25"},
		{name: "two separators are not a decimal", text: "2.5.0"},
		{name: "spaces are not digits", text: " 25.00"},
		{name: "a thousands separator is not a digit", text: "1,000.00"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseCents(testCase.text)
			if !errors.Is(err, ErrInvalidAmount) {
				t.Fatalf("parseCents(%q) error = %v, want ErrInvalidAmount", testCase.text, err)
			}
			if got != 0 {
				t.Fatalf("refused %q produced %d cents, want none", testCase.text, got)
			}
		})
	}
}

func TestParseCents_refusesAnAmountAboveInt64(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{name: "one cent past the limit overflows", text: "92233720368547758.08"},
		{name: "the units alone overflow the scaling", text: "99999999999999999999"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseCents(testCase.text)
			if !errors.Is(err, ErrOverflow) {
				t.Fatalf("parseCents(%q) error = %v, want ErrOverflow", testCase.text, err)
			}
			if got != 0 {
				t.Fatalf("overflowing %q produced %d cents, want none", testCase.text, got)
			}
		})
	}
}

func TestFormatCents_alwaysWritesTwoDecimalPlaces(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cents int64
		want  string
	}{
		{name: "zero keeps both places", cents: 0, want: "0.00"},
		{name: "a whole amount keeps both places", cents: 2500, want: "25.00"},
		{name: "a single cent is padded", cents: 1, want: "0.01"},
		{name: "tens of cents need no padding", cents: 50, want: "0.50"},
		{name: "thousands of cents keep the units", cents: 197550, want: "1975.50"},
		{name: "a negative difference keeps the sign", cents: -1500, want: "-15.00"},
		{name: "a negative amount below one unit keeps the sign", cents: -8, want: "-0.08"},
		{name: "the smallest int64 still formats", cents: math.MinInt64, want: "-92233720368547758.08"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := formatCents(testCase.cents)
			if got != testCase.want {
				t.Fatalf("formatCents(%d) = %q, want %q", testCase.cents, got, testCase.want)
			}
		})
	}
}

func TestFormatCents_roundTripsThroughTheParse(t *testing.T) {
	t.Parallel()
	const cents int64 = 197550
	text := formatCents(cents)
	got, err := parseCents(text)
	if err != nil {
		t.Fatalf("parseCents of the formatted %s error = %v, want nil", strconv.FormatInt(cents, 10), err)
	}
	if got != cents {
		t.Fatalf("round trip produced %d cents, want %d", got, cents)
	}
}
