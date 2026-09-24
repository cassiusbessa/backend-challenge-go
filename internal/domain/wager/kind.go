package wager

import "errors"

var ErrUnknownKind = errors.New("wager: kind is not one of the six")

// Kind is the operation the provider asked for. The zero value is invalid.
//
// The constants carry the Kind prefix because the package also exposes one
// action function per kind, and those names — Bet, Win, Loss, Refund,
// Rollback — are the ones go-wager-actions pins.
type Kind uint8

const (
	noKind Kind = iota
	KindOpening
	KindBet
	KindWin
	KindLoss
	KindRefund
	KindRollback
	kindCount
)

var kindTokens = [kindCount]string{
	KindOpening:  "OPENING",
	KindBet:      "BET",
	KindWin:      "WIN",
	KindLoss:     "LOSS",
	KindRefund:   "REFUND",
	KindRollback: "ROLLBACK",
}

func (k Kind) String() string {
	if k >= kindCount {
		return ""
	}
	return kindTokens[k]
}

func (k Kind) IsZero() bool {
	return k == noKind
}

func ParseKind(text string) (Kind, error) {
	for kind := KindOpening; kind < kindCount; kind++ {
		if kind.String() == text {
			return kind, nil
		}
	}
	return noKind, ErrUnknownKind
}

// IsReversal reports whether the kind undoes another operation, which is what
// makes the cited operation mandatory.
func (k Kind) IsReversal() bool {
	return k == KindRefund || k == KindRollback
}

// MovesMoney reports whether the kind carries an amount greater than zero.
// LOSS is the only one that does not: it closes the round without touching the
// wallet, so its amount is zero and it produces no entry.
func (k Kind) MovesMoney() bool {
	return k != KindLoss
}
