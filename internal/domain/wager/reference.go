package wager

// referenceState is what the cited operation allows right now.
type referenceState uint8

const (
	referenceReady referenceState = iota
	referenceAbsent
	referenceUnsuccessful
	referenceInFlight
)

func stateOf(cited *Transaction) referenceState {
	switch {
	case cited == nil:
		return referenceAbsent
	case cited.status == Processed:
		return referenceReady
	case cited.status.IsTerminal():
		return referenceUnsuccessful
	}
	return referenceInFlight
}

// checkReference decides what to do about the cited operation before anything
// touches the wallet. The bool reports whether the caller may proceed.
//
// A cited operation that already ended badly is refused now, because no later
// arrival changes it. One that is absent or still running produces the wait:
// the clock closes it, and that clock belongs to the reference worker.
func checkReference(cited *Transaction) (Decision, bool, error) {
	switch stateOf(cited) {
	case referenceReady:
		return Decision{}, true, nil
	case referenceUnsuccessful:
		return Decision{}, false, NewRejection(ReferenceUnsuccessful, nil)
	}
	return waitForReference(), false, nil
}

// closesWith reports whether the cited operation belongs to the same provider,
// player, wallet and round as the one citing it. Currency is checked apart,
// because a currency that does not close has its own token.
func closesWith(op, cited *Transaction) bool {
	if op.providerID != cited.providerID || op.playerID != cited.playerID {
		return false
	}
	return op.walletID == cited.walletID && op.roundID == cited.roundID
}

func checkCitedCurrency(op, cited *Transaction) error {
	if op.amount.Currency() != cited.amount.Currency() {
		return NewRejection(CurrencyMismatch, nil)
	}
	return nil
}
