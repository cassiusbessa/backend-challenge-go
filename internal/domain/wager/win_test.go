package wager

import "testing"

func TestWin_withoutAReferenceCreditsImmediately(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	decision, err := Win(account, operation(t, KindWin, 5000), Reference{}, movement(t, "2"))
	if err != nil {
		t.Fatalf("Win of 50.00 over 975.00 error = %v, want nil", err)
	}
	if decision.Balance().Amount() != "1025.00" {
		t.Fatalf("balance after the win = %s, want 1025.00", decision.Balance().Amount())
	}
	if _, ok := decision.Entry(); !ok {
		t.Fatalf("the win produced no entry, want one")
	}
}

func TestWin_citingAMissingBetAsksToWait(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	spec := betSpec(t)
	spec.Kind = KindWin
	spec.Amount = brl(t, 5000)
	spec.ReferenceExternalID = tokenOf(t, "tx-000")
	decision, err := Win(account, mustExternal(t, spec), Reference{}, movement(t, "2"))
	if err != nil {
		t.Fatalf("Win citing a missing bet error = %v, want nil", err)
	}
	if !decision.IsWaiting() {
		t.Fatalf("the decision was not to wait, want the wait for the reference")
	}
	if account.Balance().Amount() != "975.00" || account.Version() != 1 {
		t.Fatalf("the wallet moved to %s at version %d, want 975.00 at version 1", account.Balance().Amount(), account.Version())
	}
}

func citingWin(t *testing.T) *Transaction {
	t.Helper()
	spec := betSpec(t)
	spec.Kind = KindWin
	spec.Amount = brl(t, 5000)
	spec.ReferenceExternalID = tokenOf(t, "tx-000")
	return mustExternal(t, spec)
}

func TestWin_citingAnotherRoundIsRejected(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	cited := citedProcessed(t, KindBet, 2500)
	cited.roundID = roundOf(t, "round-99")
	decision, err := Win(account, citingWin(t), Reference{Cited: cited}, movement(t, "2"))
	if got := rejectionCode(t, err); got != ReferenceMismatch {
		t.Fatalf("rejection for another round = %s, want REFERENCE_MISMATCH", got)
	}
	if _, ok := decision.Entry(); ok {
		t.Fatalf("the refused win produced an entry, want none")
	}
	if account.Balance().Amount() != "975.00" {
		t.Fatalf("balance after the refused win = %s, want 975.00", account.Balance().Amount())
	}
}

func TestWin_citingSomethingOtherThanABetIsRejected(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	cited := citedProcessed(t, KindWin, 2500)
	_, err := Win(account, citingWin(t), Reference{Cited: cited}, movement(t, "2"))
	if got := rejectionCode(t, err); got != ReferenceMismatch {
		t.Fatalf("rejection for a cited win = %s, want REFERENCE_MISMATCH", got)
	}
}

func TestWin_citingAProcessedBetCredits(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	cited := citedProcessed(t, KindBet, 2500)
	decision, err := Win(account, citingWin(t), Reference{Cited: cited}, movement(t, "2"))
	if err != nil {
		t.Fatalf("Win citing a processed bet error = %v, want nil", err)
	}
	if decision.Balance().Amount() != "1025.00" {
		t.Fatalf("balance after the cited win = %s, want 1025.00", decision.Balance().Amount())
	}
}

func TestWin_citingAnUnsuccessfulBetIsRejectedNow(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	cited := operation(t, KindBet, 2500)
	if err := cited.Reject(InsufficientFunds, later(t)); err != nil {
		t.Fatalf("Reject of the cited bet for the fixture error = %v, want nil", err)
	}
	_, err := Win(account, citingWin(t), Reference{Cited: cited}, movement(t, "2"))
	if got := rejectionCode(t, err); got != ReferenceUnsuccessful {
		t.Fatalf("rejection for a rejected bet = %s, want REFERENCE_UNSUCCESSFUL", got)
	}
}

func TestWin_citingABetStillInFlightAsksToWait(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	cited := operation(t, KindBet, 2500)
	decision, err := Win(account, citingWin(t), Reference{Cited: cited}, movement(t, "2"))
	if err != nil {
		t.Fatalf("Win citing a pending bet error = %v, want nil", err)
	}
	if !decision.IsWaiting() {
		t.Fatalf("the decision was not to wait, want the wait while the bet is in flight")
	}
}
