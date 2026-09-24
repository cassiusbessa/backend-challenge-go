package wager

import "testing"

func TestRefund_creditsTheExactBetAmount(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	cited := citedProcessed(t, KindBet, 2500)
	decision, err := Refund(account, operation(t, KindRefund, 2500), Reference{Cited: cited}, movement(t, "2"))
	if err != nil {
		t.Fatalf("Refund of the whole bet error = %v, want nil", err)
	}
	entry, ok := decision.Entry()
	if !ok {
		t.Fatalf("the refund produced no entry, want one")
	}
	if entry.Amount().Amount() != "25.00" {
		t.Fatalf("refund entry = %s, want a credit of 25.00", entry.Amount().Amount())
	}
	if decision.Balance().Amount() != "1000.00" {
		t.Fatalf("balance after the refund = %s, want 1000.00", decision.Balance().Amount())
	}
}

func TestRefund_refusesAPartialAmount(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	cited := citedProcessed(t, KindBet, 2500)
	decision, err := Refund(account, operation(t, KindRefund, 1000), Reference{Cited: cited}, movement(t, "2"))
	if got := rejectionCode(t, err); got != ReversalAmountMismatch {
		t.Fatalf("rejection for a partial refund = %s, want REVERSAL_AMOUNT_MISMATCH", got)
	}
	if _, ok := decision.Entry(); ok {
		t.Fatalf("the partial refund produced an entry, want none")
	}
}

func TestRefund_refusesASecondReversalOfTheSameBet(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	cited := citedProcessed(t, KindBet, 2500)
	reference := Reference{Cited: cited, AlreadyReversed: true}
	decision, err := Refund(account, operation(t, KindRefund, 2500), reference, movement(t, "2"))
	if got := rejectionCode(t, err); got != AlreadyReversed {
		t.Fatalf("rejection for a second reversal = %s, want ALREADY_REVERSED", got)
	}
	if _, ok := decision.Entry(); ok {
		t.Fatalf("the second reversal produced an entry, want none")
	}
	if account.Balance().Amount() != "975.00" {
		t.Fatalf("balance after the second reversal = %s, want 975.00", account.Balance().Amount())
	}
}

func TestRefund_refusesToCiteSomethingOtherThanABet(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	cited := citedProcessed(t, KindWin, 2500)
	_, err := Refund(account, operation(t, KindRefund, 2500), Reference{Cited: cited}, movement(t, "2"))
	if got := rejectionCode(t, err); got != ReferenceMismatch {
		t.Fatalf("rejection for a refund citing a win = %s, want REFERENCE_MISMATCH", got)
	}
}

func TestRollback_creditsWhenTheCitedOperationIsABet(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	cited := citedProcessed(t, KindBet, 2500)
	decision, err := Rollback(account, operation(t, KindRollback, 2500), Reference{Cited: cited}, movement(t, "2"))
	if err != nil {
		t.Fatalf("Rollback of a bet error = %v, want nil", err)
	}
	entry, ok := decision.Entry()
	if !ok {
		t.Fatalf("the rollback produced no entry, want one")
	}
	if entry.Amount().Amount() != "25.00" || decision.Balance().Amount() != "1000.00" {
		t.Fatalf("rollback entry = %s leaving %s, want 25.00 leaving 1000.00", entry.Amount().Amount(), decision.Balance().Amount())
	}
}

func TestRollback_debitsWhenTheCitedOperationIsAWin(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 6000)
	cited := citedProcessed(t, KindWin, 5000)
	decision, err := Rollback(account, operation(t, KindRollback, 5000), Reference{Cited: cited}, movement(t, "2"))
	if err != nil {
		t.Fatalf("Rollback of a win error = %v, want nil", err)
	}
	if decision.Balance().Amount() != "10.00" {
		t.Fatalf("balance after rolling back the win = %s, want 10.00", decision.Balance().Amount())
	}
}

func TestRollback_withoutFundsForTheReversalIsRejected(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 1000)
	cited := citedProcessed(t, KindWin, 5000)
	decision, err := Rollback(account, operation(t, KindRollback, 5000), Reference{Cited: cited}, movement(t, "2"))
	if got := rejectionCode(t, err); got != ReversalInsufficientFunds {
		t.Fatalf("rejection = %s, want REVERSAL_INSUFFICIENT_FUNDS", got)
	}
	if _, ok := decision.Entry(); ok {
		t.Fatalf("the refused reversal produced an entry, want none")
	}
	if account.Balance().Amount() != "10.00" {
		t.Fatalf("balance after the refused reversal = %s, want 10.00", account.Balance().Amount())
	}
}

func TestRollback_debitsWhenTheCitedOperationIsARefund(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 6000)
	cited := citedProcessed(t, KindRefund, 5000)
	decision, err := Rollback(account, operation(t, KindRollback, 5000), Reference{Cited: cited}, movement(t, "2"))
	if err != nil {
		t.Fatalf("Rollback of a refund error = %v, want nil", err)
	}
	if decision.Balance().Amount() != "10.00" {
		t.Fatalf("balance after rolling back the refund = %s, want 10.00", decision.Balance().Amount())
	}
}

func TestRollback_refusesToCiteAnOperationThatMovedNothing(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 6000)
	cited := citedProcessed(t, KindLoss, 0)
	_, err := Rollback(account, operation(t, KindRollback, 5000), Reference{Cited: cited}, movement(t, "2"))
	if got := rejectionCode(t, err); got != ReversalAmountMismatch {
		t.Fatalf("rejection for a rollback citing a loss = %s, want REVERSAL_AMOUNT_MISMATCH", got)
	}
}

func TestRollback_inAnotherCurrencyIsRejected(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	spec := betSpec(t)
	spec.Kind = KindRollback
	spec.Amount = amountIn(t, 2500, "USD")
	spec.ReferenceExternalID = tokenOf(t, "tx-000")
	cited := citedProcessed(t, KindBet, 2500)
	_, err := Rollback(account, mustExternal(t, spec), Reference{Cited: cited}, movement(t, "2"))
	if got := rejectionCode(t, err); got != CurrencyMismatch {
		t.Fatalf("rejection for a USD rollback over a BRL wallet = %s, want CURRENCY_MISMATCH", got)
	}
}

// The wallet and the operation agree on the currency, and the cited operation
// is the one that does not. That is still CURRENCY_MISMATCH, not a reference
// that fails to close.
func TestCheckCitedCurrency_refusesWhenOnlyTheCitedOperationDiffers(t *testing.T) {
	t.Parallel()
	account := openedWalletIn(t, 97500, "USD")
	spec := betSpec(t)
	spec.Kind = KindRollback
	spec.Amount = amountIn(t, 2500, "USD")
	spec.ReferenceExternalID = tokenOf(t, "tx-000")
	cited := citedProcessed(t, KindBet, 2500)
	_, err := Rollback(account, mustExternal(t, spec), Reference{Cited: cited}, movement(t, "2"))
	if got := rejectionCode(t, err); got != CurrencyMismatch {
		t.Fatalf("rejection for a BRL cited bet = %s, want CURRENCY_MISMATCH", got)
	}
}

func TestPrepareReversal_asksToWaitWhenTheCitedOperationHasNotArrived(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	decision, err := Refund(account, operation(t, KindRefund, 2500), Reference{}, movement(t, "2"))
	if err != nil {
		t.Fatalf("Refund citing a missing bet error = %v, want nil", err)
	}
	if !decision.IsWaiting() {
		t.Fatalf("the decision was not to wait, want the wait for the reference")
	}
	if account.Version() != 1 {
		t.Fatalf("the wallet moved to version %d, want 1", account.Version())
	}
}
