package money

import "encoding/json"

// Money is an immutable amount in cents with the currency inside the type. The
// zero value is invalid: Parse, FromCents and Zero are the only constructors.
type Money struct {
	cents    int64
	currency Currency
}

// Parse reads the pair of the external contract.
//
// A negative amount is refused here: a negative value exists as an internal
// difference, never as input.
func Parse(amount, currency string) (Money, error) {
	parsed, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	cents, err := parseCents(amount)
	if err != nil {
		return Money{}, err
	}
	return Money{cents: cents, currency: parsed}, nil
}

// FromCents rebuilds the amount stored in BIGINT. Only the currency is
// required, because an internal difference is allowed to be negative.
func FromCents(cents int64, currency Currency) (Money, error) {
	if currency.IsZero() {
		return Money{}, ErrInvalidCurrency
	}
	return Money{cents: cents, currency: currency}, nil
}

// Zero is the empty amount in the given currency. It fails when the currency
// is unset, because an amount with no currency is not money.
func Zero(currency Currency) (Money, error) {
	return FromCents(0, currency)
}

func (m Money) Cents() int64 {
	return m.cents
}

func (m Money) Currency() Currency {
	return m.currency
}

func (m Money) IsZero() bool {
	return m.cents == 0
}

func (m Money) IsPositive() bool {
	return m.cents > 0
}

func (m Money) IsNegative() bool {
	return m.cents < 0
}

// Amount returns the external contract form, always with two places.
func (m Money) Amount() string {
	return formatCents(m.cents)
}

func (m Money) String() string {
	return m.Amount() + " " + m.currency.Code()
}

type externalMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (m Money) MarshalJSON() ([]byte, error) {
	return json.Marshal(externalMoney{Amount: m.Amount(), Currency: m.currency.Code()})
}
