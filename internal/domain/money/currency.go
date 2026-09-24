package money

import "errors"

var ErrInvalidCurrency = errors.New("money: currency is not an ISO 4217 code")

const isoCodeLength = 3

// Currency is an ISO 4217 code of three uppercase letters. The zero value is
// invalid: ParseCurrency is the only constructor.
type Currency struct {
	code string
}

func ParseCurrency(text string) (Currency, error) {
	if len(text) != isoCodeLength {
		return Currency{}, ErrInvalidCurrency
	}
	for index := 0; index < len(text); index++ {
		if text[index] < 'A' || text[index] > 'Z' {
			return Currency{}, ErrInvalidCurrency
		}
	}
	return Currency{code: text}, nil
}

func (c Currency) Code() string {
	return c.code
}

func (c Currency) IsZero() bool {
	return c.code == ""
}

func (c Currency) String() string {
	return c.code
}
