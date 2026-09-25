package postgres

import "github.com/junglegaming/backend-challenge-go/internal/domain/money"

// balanceOf rebuilds the stored money: the currency is the ISO code of the row
// and the amount is the BIGINT of cents.
func balanceOf(currency string, cents int64) (money.Money, error) {
	parsed, err := money.ParseCurrency(currency)
	if err != nil {
		return money.Money{}, err
	}
	return money.FromCents(cents, parsed)
}
