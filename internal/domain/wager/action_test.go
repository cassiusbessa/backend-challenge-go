package wager

import (
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

const (
	entryUUIDPrefix = "44444444-4444-4444-8444-44444444444"
	otherPlayerUUID = "55555555-5555-4555-8555-555555555555"
)

func entryOf(t *testing.T, last string) identity.LedgerEntryID {
	t.Helper()
	parsed, err := identity.ParseLedgerEntryID(entryUUIDPrefix + last)
	if err != nil {
		t.Fatalf("ParseLedgerEntryID ending in %q error = %v, want nil", last, err)
	}
	return parsed
}

func roundOf(t *testing.T, text string) identity.RoundID {
	t.Helper()
	parsed, err := identity.ParseRoundID(text)
	if err != nil {
		t.Fatalf("ParseRoundID(%q) for the fixture error = %v, want nil", text, err)
	}
	return parsed
}

func openedWallet(t *testing.T, cents int64) *wallet.Wallet {
	t.Helper()
	return openedWalletIn(t, cents, "BRL")
}

func openedWalletIn(t *testing.T, cents int64, code string) *wallet.Wallet {
	t.Helper()
	opened, _, err := wallet.Open(wallet.OpenSpec{
		ID:             walletOf(t),
		PlayerID:       playerOf(t),
		InitialBalance: amountIn(t, cents, code),
		EntryID:        entryOf(t, "0"),
		TransactionID:  transactionOf(t),
		At:             at(t),
	})
	if err != nil {
		t.Fatalf("wallet.Open with %d %s for the fixture error = %v, want nil", cents, code, err)
	}
	return opened
}

func movement(t *testing.T, last string) Movement {
	t.Helper()
	return Movement{EntryID: entryOf(t, last), At: later(t)}
}

// operation builds the transaction of one kind, already carrying the cited
// identifier when the kind demands one.
func operation(t *testing.T, kind Kind, cents int64) *Transaction {
	t.Helper()
	spec := betSpec(t)
	spec.Kind = kind
	spec.Amount = brl(t, cents)
	if kind.IsReversal() {
		spec.ReferenceExternalID = tokenOf(t, "tx-000")
	}
	return mustExternal(t, spec)
}

// citedProcessed builds the operation a reversal or a win points at, already
// closed as PROCESSED.
func citedProcessed(t *testing.T, kind Kind, cents int64) *Transaction {
	t.Helper()
	cited := operation(t, kind, cents)
	if err := cited.Process(brl(t, 0), later(t)); err != nil {
		t.Fatalf("Process of the cited %s for the fixture error = %v, want nil", kind, err)
	}
	return cited
}

func TestBet_debitsTheWallet(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 100000)
	decision, err := Bet(account, operation(t, KindBet, 2500), movement(t, "2"))
	if err != nil {
		t.Fatalf("Bet of 25.00 over 1000.00 error = %v, want nil", err)
	}
	if decision.Balance().Amount() != "975.00" {
		t.Fatalf("balance after the bet = %s, want 975.00", decision.Balance().Amount())
	}
	entry, ok := decision.Entry()
	if !ok {
		t.Fatalf("the bet produced no entry, want one")
	}
	if entry.Direction() != ledger.Debit || entry.Amount().Amount() != "25.00" {
		t.Fatalf("entry = %s of %s, want a DEBIT of 25.00", entry.Direction(), entry.Amount().Amount())
	}
}

func TestBet_withoutFundsIsRejectedWithoutMovement(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 2000)
	decision, err := Bet(account, operation(t, KindBet, 8000), movement(t, "2"))
	if got := rejectionCode(t, err); got != InsufficientFunds {
		t.Fatalf("rejection = %s, want INSUFFICIENT_FUNDS", got)
	}
	if _, ok := decision.Entry(); ok {
		t.Fatalf("a refused bet produced an entry, want none")
	}
	if account.Balance().Amount() != "20.00" {
		t.Fatalf("balance after the refused bet = %s, want 20.00", account.Balance().Amount())
	}
	if account.Version() != 1 {
		t.Fatalf("version after the refused bet = %d, want 1", account.Version())
	}
}

func TestLoss_touchesNothing(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 97500)
	decision, err := Loss(account, operation(t, KindLoss, 0))
	if err != nil {
		t.Fatalf("Loss error = %v, want nil", err)
	}
	if decision.Balance().Amount() != "975.00" {
		t.Fatalf("balance after the loss = %s, want 975.00", decision.Balance().Amount())
	}
	if _, ok := decision.Entry(); ok {
		t.Fatalf("a loss produced an entry, want none")
	}
	if decision.Version() != 1 || account.Version() != 1 {
		t.Fatalf("version after the loss = %d, want 1", account.Version())
	}
}

func TestGuard_refusesTheOperationOfAnotherPlayer(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 100000)
	stranger, err := identity.ParsePlayerID(otherPlayerUUID)
	if err != nil {
		t.Fatalf("ParsePlayerID for the stranger error = %v, want nil", err)
	}
	spec := betSpec(t)
	spec.PlayerID = stranger
	foreign := mustExternal(t, spec)
	if got := rejectionCode(t, mustReject(t, account, foreign)); got != PlayerWalletMismatch {
		t.Fatalf("rejection = %s, want PLAYER_WALLET_MISMATCH", got)
	}
	if account.Balance().Amount() != "1000.00" || account.Version() != 1 {
		t.Fatalf("wallet moved to %s at version %d, want 1000.00 at version 1", account.Balance().Amount(), account.Version())
	}
}

func mustReject(t *testing.T, account *wallet.Wallet, op *Transaction) error {
	t.Helper()
	decision, err := Bet(account, op, movement(t, "2"))
	if err == nil {
		t.Fatalf("Bet error = nil, want a rejection")
	}
	if _, ok := decision.Entry(); ok {
		t.Fatalf("a refused bet produced an entry, want none")
	}
	return err
}

func TestGuard_refusesAnAmountOutsideTheWalletCurrency(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 100000)
	spec := betSpec(t)
	spec.Amount = amountIn(t, 2500, "USD")
	foreign := mustExternal(t, spec)
	if got := rejectionCode(t, mustReject(t, account, foreign)); got != CurrencyMismatch {
		t.Fatalf("rejection for a USD bet on a BRL wallet = %s, want CURRENCY_MISMATCH", got)
	}
}

func TestGuard_refusesAnAbsentWallet(t *testing.T) {
	t.Parallel()
	_, err := Bet(nil, operation(t, KindBet, 2500), movement(t, "2"))
	if got := rejectionCode(t, err); got != WalletNotFound {
		t.Fatalf("rejection for an absent wallet = %s, want WALLET_NOT_FOUND", got)
	}
}

// The wallet answers a condition; the token depends on the path that called
// it. This is the one place both paths are compared side by side.
func TestTranslate_namesTwoTokensForOneWalletCondition(t *testing.T) {
	t.Parallel()
	betError := refusedBet(t)
	rollbackError := refusedRollback(t)
	if got := rejectionCode(t, betError); got != InsufficientFunds {
		t.Fatalf("the bet path named %s, want INSUFFICIENT_FUNDS", got)
	}
	if got := rejectionCode(t, rollbackError); got != ReversalInsufficientFunds {
		t.Fatalf("the rollback path named %s, want REVERSAL_INSUFFICIENT_FUNDS", got)
	}
	if !errors.Is(betError, wallet.ErrInsufficientFunds) {
		t.Fatalf("the bet rejection lost the wallet condition, want it reachable by errors.Is")
	}
	if !errors.Is(rollbackError, wallet.ErrInsufficientFunds) {
		t.Fatalf("the rollback rejection lost the wallet condition, want it reachable by errors.Is")
	}
}

func refusedBet(t *testing.T) error {
	t.Helper()
	_, err := Bet(openedWallet(t, 1000), operation(t, KindBet, 5000), movement(t, "2"))
	if err == nil {
		t.Fatalf("the bet over an empty wallet error = nil, want a rejection")
	}
	return err
}

func refusedRollback(t *testing.T) error {
	t.Helper()
	cited := citedProcessed(t, KindWin, 5000)
	_, err := Rollback(openedWallet(t, 1000), operation(t, KindRollback, 5000), Reference{Cited: cited}, movement(t, "2"))
	if err == nil {
		t.Fatalf("the rollback over an empty wallet error = nil, want a rejection")
	}
	return err
}

// Only a debit can run out of funds, so no crediting path may ever name one of
// the two tokens.
func TestTranslate_leavesTheFundTokensToTheDebitingPaths(t *testing.T) {
	t.Parallel()
	account := openedWallet(t, 100000)
	if _, err := Win(account, operation(t, KindWin, 5000), Reference{}, movement(t, "2")); err != nil {
		t.Fatalf("a plain win error = %v, want nil", err)
	}
	if _, err := Loss(account, operation(t, KindLoss, 0)); err != nil {
		t.Fatalf("a loss error = %v, want nil", err)
	}
	cited := citedProcessed(t, KindBet, 2500)
	if _, err := Refund(account, operation(t, KindRefund, 2500), Reference{Cited: cited}, movement(t, "3")); err != nil {
		t.Fatalf("a refund error = %v, want nil", err)
	}
}
