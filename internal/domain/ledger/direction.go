package ledger

import (
	"errors"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

var ErrInvalidDirection = errors.New("ledger: direction is neither DEBIT nor CREDIT")

type Direction uint8

const (
	noDirection Direction = iota
	Debit
	Credit
)

func (d Direction) String() string {
	switch d {
	case Debit:
		return "DEBIT"
	case Credit:
		return "CREDIT"
	}
	return ""
}

func (d Direction) IsZero() bool {
	return d == noDirection
}

// Apply is the single place where the direction decides the sign of a
// movement: credit adds and debit subtracts.
//
// The entry, the wallet movement and the balance rebuild all go through it, so
// the three cannot drift apart.
func (d Direction) Apply(balance, amount money.Money) (money.Money, error) {
	switch d {
	case Credit:
		return balance.Add(amount)
	case Debit:
		return balance.Sub(amount)
	}
	return money.Money{}, ErrInvalidDirection
}
