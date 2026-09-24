package wager

import "github.com/junglegaming/backend-challenge-go/internal/domain/wallet"

// Win credits the wallet.
//
// Without a cited operation it credits right away. Citing one, it waits when
// that bet has not arrived yet, and refuses with REFERENCE_MISMATCH when the
// bet that did arrive does not close with it.
func Win(w *wallet.Wallet, op *Transaction, ref Reference, move Movement) (Decision, error) {
	if err := guard(w, op); err != nil {
		return Decision{}, err
	}
	if _, cites := op.ReferenceExternalID(); !cites {
		return credit(w, op, move)
	}
	decision, proceed, err := checkReference(ref.Cited)
	if !proceed {
		return decision, err
	}
	if err := checkWinReference(op, ref.Cited); err != nil {
		return Decision{}, err
	}
	return credit(w, op, move)
}

func checkWinReference(op, cited *Transaction) error {
	if err := checkCitedCurrency(op, cited); err != nil {
		return err
	}
	if cited.kind != KindBet || !closesWith(op, cited) {
		return NewRejection(ReferenceMismatch, nil)
	}
	return nil
}
