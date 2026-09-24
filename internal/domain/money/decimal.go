package money

import (
	"errors"
	"strconv"
	"strings"
)

var (
	ErrInvalidAmount = errors.New("money: decimal amount is not valid")
	ErrOverflow      = errors.New("money: amount is outside the int64 range")
)

// The scale is fixed at two places for every currency in this context, not the
// minor unit of each ISO code. Recorded as a limitation in ARCHITECTURE.md.
const (
	centsPerUnit = 100
	maxScale     = 2
	separator    = "."
)

// parseCents reads the decimal amount by walking the digits and accumulating
// into int64.
//
// No path goes through strconv.ParseFloat or math/big.Float: binary floating
// point cannot hold every two-place decimal, and this value ends up in money.
func parseCents(text string) (int64, error) {
	units, fraction, err := splitDecimal(text)
	if err != nil {
		return 0, err
	}
	whole, err := parseDigits(units)
	if err != nil {
		return 0, err
	}
	scaled, err := mulInt64(whole, centsPerUnit)
	if err != nil {
		return 0, err
	}
	cents, err := fractionCents(fraction)
	if err != nil {
		return 0, err
	}
	return addInt64(scaled, cents)
}

// splitDecimal separates the units from the fraction. It admits at most one
// separator, with at least one digit after it.
func splitDecimal(text string) (string, string, error) {
	units, fraction, hasSeparator := strings.Cut(text, separator)
	if !hasSeparator {
		return units, "", nil
	}
	if fraction == "" || strings.Contains(fraction, separator) {
		return "", "", ErrInvalidAmount
	}
	return units, fraction, nil
}

// fractionCents normalizes the fraction to two places. A longer scale is
// refused rather than rounded: rounding external input silently changes money.
func fractionCents(fraction string) (int64, error) {
	switch len(fraction) {
	case 0:
		return 0, nil
	case 1:
		tenths, err := parseDigits(fraction)
		if err != nil {
			return 0, err
		}
		return tenths * 10, nil
	case maxScale:
		return parseDigits(fraction)
	}
	return 0, ErrInvalidAmount
}

func parseDigits(text string) (int64, error) {
	if text == "" {
		return 0, ErrInvalidAmount
	}
	var value int64
	for index := 0; index < len(text); index++ {
		next, err := accumulate(value, text[index])
		if err != nil {
			return 0, err
		}
		value = next
	}
	return value, nil
}

// accumulate refuses signs, letters and anything outside zero to nine.
//
// That single check is what discards the empty string, NaN, Infinity,
// scientific notation and negative input, with no case for each one.
func accumulate(value int64, digit byte) (int64, error) {
	if digit < '0' || digit > '9' {
		return 0, ErrInvalidAmount
	}
	shifted, err := mulInt64(value, 10)
	if err != nil {
		return 0, err
	}
	return addInt64(shifted, int64(digit-'0'))
}

// formatCents writes the amount with two places, zero included.
func formatCents(cents int64) string {
	units := cents / centsPerUnit
	fraction := cents % centsPerUnit
	if fraction < 0 {
		fraction = -fraction
	}
	text := strconv.FormatInt(units, 10) + separator + twoDigits(fraction)
	if cents < 0 && units == 0 {
		return "-" + text
	}
	return text
}

func twoDigits(fraction int64) string {
	if fraction < 10 {
		return "0" + strconv.FormatInt(fraction, 10)
	}
	return strconv.FormatInt(fraction, 10)
}
