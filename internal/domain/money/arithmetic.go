package money

import "errors"

var ErrCurrencyMismatch = errors.New("money: operation between different currencies")

func (m Money) Add(other Money) (Money, error) {
	return m.combine(other, addInt64)
}

func (m Money) Sub(other Money) (Money, error) {
	return m.combine(other, subInt64)
}

func (m Money) Neg() (Money, error) {
	cents, err := subInt64(0, m.cents)
	if err != nil {
		return Money{}, err
	}
	return Money{cents: cents, currency: m.currency}, nil
}

func (m Money) Cmp(other Money) (int, error) {
	if m.currency != other.currency {
		return 0, ErrCurrencyMismatch
	}
	switch {
	case m.cents < other.cents:
		return -1, nil
	case m.cents > other.cents:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports whether both the amount and the currency match.
//
// Unlike Cmp it returns no error: two currencies are two different values, not
// an invalid comparison.
func (m Money) Equal(other Money) bool {
	return m == other
}

func (m Money) combine(other Money, operation func(int64, int64) (int64, error)) (Money, error) {
	if m.currency != other.currency {
		return Money{}, ErrCurrencyMismatch
	}
	cents, err := operation(m.cents, other.cents)
	if err != nil {
		return Money{}, err
	}
	return Money{cents: cents, currency: m.currency}, nil
}

func addInt64(left, right int64) (int64, error) {
	sum := left + right
	if (right > 0 && sum < left) || (right < 0 && sum > left) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func subInt64(left, right int64) (int64, error) {
	difference := left - right
	if (right < 0 && difference < left) || (right > 0 && difference > left) {
		return 0, ErrOverflow
	}
	return difference, nil
}

func mulInt64(left, right int64) (int64, error) {
	if left == 0 || right == 0 {
		return 0, nil
	}
	product := left * right
	if product/right != left {
		return 0, ErrOverflow
	}
	return product, nil
}
