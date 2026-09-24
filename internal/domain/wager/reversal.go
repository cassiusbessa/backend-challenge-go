package wager

import "github.com/junglegaming/backend-challenge-go/internal/domain/wallet"

// Refund credits the full amount of a processed bet.
//
// Partial reversal does not exist: an amount other than the whole bet is
// refused, and a second reversal of the same bet is refused without an entry.
func Refund(w *wallet.Wallet, op *Transaction, ref Reference, move Movement) (Decision, error) {
	decision, cited, err := prepareReversal(w, op, ref)
	if cited == nil {
		return decision, err
	}
	if cited.kind != KindBet {
		return Decision{}, NewRejection(ReferenceMismatch, nil)
	}
	return credit(w, op, move)
}

// Rollback moves against the cited operation: it credits when that operation
// was a BET, and debits when it was a WIN or a REFUND.
//
// The debit runs through the same wallet condition as a bet, and this is where
// that condition is named REVERSAL_INSUFFICIENT_FUNDS.
func Rollback(w *wallet.Wallet, op *Transaction, ref Reference, move Movement) (Decision, error) {
	decision, cited, err := prepareReversal(w, op, ref)
	if cited == nil {
		return decision, err
	}
	if cited.kind == KindBet {
		return credit(w, op, move)
	}
	if cited.kind != KindWin && cited.kind != KindRefund {
		return Decision{}, NewRejection(ReferenceMismatch, nil)
	}
	return debit(w, op, move, ReversalInsufficientFunds)
}

// prepareReversal runs everything both reversals share. A nil transaction
// means the caller must stop and answer the decision and the error given.
func prepareReversal(w *wallet.Wallet, op *Transaction, ref Reference) (Decision, *Transaction, error) {
	if err := guard(w, op); err != nil {
		return Decision{}, nil, err
	}
	decision, proceed, err := checkReference(ref.Cited)
	if !proceed {
		return decision, nil, err
	}
	if ref.AlreadyReversed {
		return Decision{}, nil, NewRejection(AlreadyReversed, nil)
	}
	if err := checkReversalMatch(op, ref.Cited); err != nil {
		return Decision{}, nil, err
	}
	return Decision{}, ref.Cited, nil
}

func checkReversalMatch(op, cited *Transaction) error {
	if err := checkCitedCurrency(op, cited); err != nil {
		return err
	}
	if !closesWith(op, cited) {
		return NewRejection(ReferenceMismatch, nil)
	}
	if !op.amount.Equal(cited.amount) {
		return NewRejection(ReversalAmountMismatch, nil)
	}
	return nil
}
