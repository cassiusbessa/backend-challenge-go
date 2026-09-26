package money

import (
	"errors"
	"testing"
)

func TestParseCurrency_acceptsAThreeLetterUppercaseCode(t *testing.T) {
	t.Parallel()
	currency, err := ParseCurrency("BRL")
	if err != nil {
		t.Fatalf("ParseCurrency(\"BRL\") error = %v, want nil", err)
	}
	if currency.Code() != "BRL" {
		t.Fatalf("code = %q, want %q", currency.Code(), "BRL")
	}
}

func TestParseCurrency_refusesWhatIsNotAnISOCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{name: "empty text has no code", text: ""},
		{name: "two letters are too short", text: "BR"},
		{name: "four letters are too long", text: "BRLL"},
		{name: "lowercase is not the ISO form", text: "brl"},
		{name: "digits are not letters", text: "123"},
		{name: "a symbol is not a letter", text: "B$L"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			currency, err := ParseCurrency(testCase.text)
			if !errors.Is(err, ErrInvalidCurrency) {
				t.Fatalf("ParseCurrency(%q) error = %v, want ErrInvalidCurrency", testCase.text, err)
			}
			if !currency.IsZero() {
				t.Fatalf("refused currency = %q, want the zero value", currency.Code())
			}
		})
	}
}

func TestParseCurrency_acceptsBothEndsOfTheLetterRange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
	}{
		{name: "the first letter of the range", text: "AUD"},
		{name: "the last letter of the range", text: "NZD"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			currency, err := ParseCurrency(testCase.text)
			if err != nil {
				t.Fatalf("ParseCurrency(%q) at the range boundary error = %v, want nil", testCase.text, err)
			}
			if currency.Code() != testCase.text {
				t.Fatalf("boundary code = %q, want %q", currency.Code(), testCase.text)
			}
		})
	}
}

func TestIsZero_answersForTheUnsetCurrency(t *testing.T) {
	t.Parallel()
	var unset Currency
	if !unset.IsZero() {
		t.Fatalf("zero value IsZero() = false, want true")
	}
	currency, err := ParseCurrency("USD")
	if err != nil {
		t.Fatalf("ParseCurrency(\"USD\") error = %v, want nil", err)
	}
	if currency.IsZero() {
		t.Fatalf("parsed currency IsZero() = true, want false")
	}
}

func TestString_showsTheISOCode(t *testing.T) {
	t.Parallel()
	currency, err := ParseCurrency("BRL")
	if err != nil {
		t.Fatalf("ParseCurrency before String error = %v, want nil", err)
	}
	if currency.String() != "BRL" {
		t.Fatalf("String() = %q, want %q", currency.String(), "BRL")
	}
}
