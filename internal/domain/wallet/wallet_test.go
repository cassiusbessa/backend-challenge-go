package wallet

import (
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

const (
	walletUUID      = "11111111-1111-4111-8111-111111111111"
	playerUUID      = "22222222-2222-4222-8222-222222222222"
	transactionUUID = "33333333-3333-4333-8333-333333333333"
	entryUUIDPrefix = "44444444-4444-4444-8444-44444444444"
)

func at(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, time.March, 14, 10, 30, 0, 0, time.UTC)
}

func amountIn(t *testing.T, cents int64, code string) money.Money {
	t.Helper()
	currency, err := money.ParseCurrency(code)
	if err != nil {
		t.Fatalf("ParseCurrency(%q) for the fixture error = %v, want nil", code, err)
	}
	value, err := money.FromCents(cents, currency)
	if err != nil {
		t.Fatalf("FromCents(%d, %s) for the fixture error = %v, want nil", cents, code, err)
	}
	return value
}

func brl(t *testing.T, cents int64) money.Money {
	t.Helper()
	return amountIn(t, cents, "BRL")
}

func walletOf(t *testing.T) identity.WalletID {
	t.Helper()
	parsed, err := identity.ParseWalletID(walletUUID)
	if err != nil {
		t.Fatalf("ParseWalletID for the fixture error = %v, want nil", err)
	}
	return parsed
}

func playerOf(t *testing.T) identity.PlayerID {
	t.Helper()
	parsed, err := identity.ParsePlayerID(playerUUID)
	if err != nil {
		t.Fatalf("ParsePlayerID for the fixture error = %v, want nil", err)
	}
	return parsed
}

func transactionOf(t *testing.T) identity.TransactionID {
	t.Helper()
	parsed, err := identity.ParseTransactionID(transactionUUID)
	if err != nil {
		t.Fatalf("ParseTransactionID for the fixture error = %v, want nil", err)
	}
	return parsed
}

func entryOf(t *testing.T, last string) identity.LedgerEntryID {
	t.Helper()
	parsed, err := identity.ParseLedgerEntryID(entryUUIDPrefix + last)
	if err != nil {
		t.Fatalf("ParseLedgerEntryID ending in %q error = %v, want nil", last, err)
	}
	return parsed
}

func openSpec(t *testing.T, cents int64) OpenSpec {
	t.Helper()
	return OpenSpec{
		ID:             walletOf(t),
		PlayerID:       playerOf(t),
		InitialBalance: brl(t, cents),
		EntryID:        entryOf(t, "1"),
		TransactionID:  transactionOf(t),
		At:             at(t),
	}
}

func openedWith(t *testing.T, cents int64) *Wallet {
	t.Helper()
	opened, _, err := Open(openSpec(t, cents))
	if err != nil {
		t.Fatalf("Open with %d cents for the fixture error = %v, want nil", cents, err)
	}
	return opened
}

func TestOpen_withAPositiveBalanceYieldsTheCreditEntry(t *testing.T) {
	t.Parallel()
	opened, result, err := Open(openSpec(t, 100000))
	if err != nil {
		t.Fatalf("Open with 1000.00 error = %v, want nil", err)
	}
	if opened.Balance().Amount() != "1000.00" {
		t.Fatalf("balance = %s, want 1000.00", opened.Balance().Amount())
	}
	if opened.Version() != 1 {
		t.Fatalf("version = %d, want 1", opened.Version())
	}
	if _, ok := result.Entry(); !ok {
		t.Fatalf("opening with a positive balance yielded no entry, want one")
	}
}

func TestOpen_yieldsACreditOfTheWholeInitialBalance(t *testing.T) {
	t.Parallel()
	_, result, err := Open(openSpec(t, 100000))
	if err != nil {
		t.Fatalf("Open before reading the direction error = %v, want nil", err)
	}
	entry, _ := result.Entry()
	if entry.Direction() != ledger.Credit {
		t.Fatalf("opening entry direction = %s, want CREDIT", entry.Direction())
	}
	if entry.Amount().Amount() != "1000.00" {
		t.Fatalf("opening entry amount = %s, want 1000.00", entry.Amount().Amount())
	}
}

func TestOpen_recordsTheOpeningEntryFromZeroAtTheFirstSequence(t *testing.T) {
	t.Parallel()
	_, result, err := Open(openSpec(t, 100000))
	if err != nil {
		t.Fatalf("Open before reading the entry error = %v, want nil", err)
	}
	entry, _ := result.Entry()
	if entry.BalanceBefore().Amount() != "0.00" {
		t.Fatalf("opening entry balance before = %s, want 0.00", entry.BalanceBefore().Amount())
	}
	if entry.BalanceAfter().Amount() != "1000.00" {
		t.Fatalf("opening entry balance after = %s, want 1000.00", entry.BalanceAfter().Amount())
	}
	if entry.Sequence() != 1 {
		t.Fatalf("opening entry sequence = %d, want 1", entry.Sequence())
	}
}

func TestOpen_withZeroProducesNoEntry(t *testing.T) {
	t.Parallel()
	opened, result, err := Open(openSpec(t, 0))
	if err != nil {
		t.Fatalf("Open with 0.00 error = %v, want nil", err)
	}
	if opened.Balance().Amount() != "0.00" {
		t.Fatalf("zero opening balance = %s, want 0.00", opened.Balance().Amount())
	}
	if opened.Version() != 1 {
		t.Fatalf("zero opening version = %d, want 1", opened.Version())
	}
	if _, ok := result.Entry(); ok {
		t.Fatalf("opening with zero yielded an entry, want none")
	}
}

func TestOpen_withoutTheOpeningIdentifiersIsRefusedOnlyWhenThereIsAnEntry(t *testing.T) {
	t.Parallel()
	bare := openSpec(t, 0)
	bare.EntryID = identity.LedgerEntryID{}
	bare.TransactionID = identity.TransactionID{}
	if _, _, err := Open(bare); err != nil {
		t.Fatalf("Open with zero and no entry identifiers error = %v, want nil", err)
	}
	funded := openSpec(t, 100000)
	funded.EntryID = identity.LedgerEntryID{}
	if _, _, err := Open(funded); !errors.Is(err, ErrIncompleteWallet) {
		t.Fatalf("Open with a positive balance and no entry id error = %v, want ErrIncompleteWallet", err)
	}
}

func TestOpen_refusesTheIncompleteSpec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		spoil func(*OpenSpec)
	}{
		{name: "without a wallet id", spoil: func(s *OpenSpec) { s.ID = identity.WalletID{} }},
		{name: "without a player", spoil: func(s *OpenSpec) { s.PlayerID = identity.PlayerID{} }},
		{name: "without an instant", spoil: func(s *OpenSpec) { s.At = time.Time{} }},
		{name: "without a transaction", spoil: func(s *OpenSpec) { s.TransactionID = identity.TransactionID{} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			spec := openSpec(t, 100000)
			testCase.spoil(&spec)
			opened, _, err := Open(spec)
			if !errors.Is(err, ErrIncompleteWallet) {
				t.Fatalf("Open %s error = %v, want ErrIncompleteWallet", testCase.name, err)
			}
			if opened != nil {
				t.Fatalf("Open %s returned a wallet, want none", testCase.name)
			}
		})
	}
}

func TestOpen_refusesABalanceThatCannotOpenAWallet(t *testing.T) {
	t.Parallel()
	negative := openSpec(t, 0)
	negative.InitialBalance = brl(t, -1)
	if _, _, err := Open(negative); !errors.Is(err, ErrNegativeBalance) {
		t.Fatalf("Open with a negative balance error = %v, want ErrNegativeBalance", err)
	}
	unset := openSpec(t, 0)
	unset.InitialBalance = money.Money{}
	if _, _, err := Open(unset); !errors.Is(err, ErrMissingCurrency) {
		t.Fatalf("Open without a currency error = %v, want ErrMissingCurrency", err)
	}
}

func TestRehydrate_keepsTheStoredVersionAndProducesNoEntry(t *testing.T) {
	t.Parallel()
	restored, err := Rehydrate(State{
		ID:        walletOf(t),
		PlayerID:  playerOf(t),
		Balance:   brl(t, 25000),
		Version:   7,
		CreatedAt: at(t),
		UpdatedAt: at(t),
	})
	if err != nil {
		t.Fatalf("Rehydrate error = %v, want nil", err)
	}
	if restored.Balance().Amount() != "250.00" {
		t.Fatalf("rehydrated balance = %s, want 250.00", restored.Balance().Amount())
	}
	if restored.Version() != 7 {
		t.Fatalf("rehydrated version = %d, want 7", restored.Version())
	}
	if restored.ID() != walletOf(t) || restored.PlayerID() != playerOf(t) {
		t.Fatalf("rehydrated identity = %s and %s, want the stored pair", restored.ID(), restored.PlayerID())
	}
}

func TestRehydrate_acceptsTheVersionTheWalletIsBornAt(t *testing.T) {
	t.Parallel()
	restored, err := Rehydrate(State{
		ID:        walletOf(t),
		PlayerID:  playerOf(t),
		Balance:   brl(t, 25000),
		Version:   firstVersion,
		CreatedAt: at(t),
		UpdatedAt: at(t),
	})
	if err != nil {
		t.Fatalf("Rehydrate at the first version error = %v, want nil", err)
	}
	if restored.Version() != firstVersion {
		t.Fatalf("version after rehydrating the newborn = %d, want %d", restored.Version(), firstVersion)
	}
}

func TestRehydrate_refusesTheIncompleteState(t *testing.T) {
	t.Parallel()
	complete := State{
		ID:        walletOf(t),
		PlayerID:  playerOf(t),
		Balance:   brl(t, 25000),
		Version:   7,
		CreatedAt: at(t),
		UpdatedAt: at(t),
	}
	cases := []struct {
		name  string
		spoil func(*State)
		want  error
	}{
		{name: "without a wallet id", spoil: func(s *State) { s.ID = identity.WalletID{} }, want: ErrIncompleteWallet},
		{name: "without a player", spoil: func(s *State) { s.PlayerID = identity.PlayerID{} }, want: ErrIncompleteWallet},
		{name: "below the first version", spoil: func(s *State) { s.Version = 0 }, want: ErrIncompleteWallet},
		{name: "without a creation instant", spoil: func(s *State) { s.CreatedAt = time.Time{} }, want: ErrIncompleteWallet},
		{name: "without an update instant", spoil: func(s *State) { s.UpdatedAt = time.Time{} }, want: ErrIncompleteWallet},
		{name: "with a negative balance", spoil: func(s *State) { s.Balance = brl(t, -1) }, want: ErrNegativeBalance},
		{name: "without a currency", spoil: func(s *State) { s.Balance = money.Money{} }, want: ErrMissingCurrency},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			state := complete
			testCase.spoil(&state)
			restored, err := Rehydrate(state)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Rehydrate %s error = %v, want %v", testCase.name, err, testCase.want)
			}
			if restored != nil {
				t.Fatalf("Rehydrate %s returned a wallet, want none", testCase.name)
			}
		})
	}
}

func TestBalance_doesNotAliasTheStoredAmount(t *testing.T) {
	t.Parallel()
	opened := openedWith(t, 100000)
	taken := opened.Balance()
	drained, err := taken.Sub(brl(t, 100000))
	if err != nil {
		t.Fatalf("Sub on the returned amount error = %v, want nil", err)
	}
	if !drained.IsZero() {
		t.Fatalf("the local copy after subtracting = %s, want 0.00", drained.Amount())
	}
	if opened.Balance().Amount() != "1000.00" {
		t.Fatalf("wallet balance after altering the returned amount = %s, want 1000.00", opened.Balance().Amount())
	}
}

func TestCreatedAt_reportsTheInstantTheOpeningWasGiven(t *testing.T) {
	t.Parallel()
	opened := openedWith(t, 100000)
	if !opened.CreatedAt().Equal(at(t)) {
		t.Fatalf("created at = %s, want %s", opened.CreatedAt(), at(t))
	}
	if !opened.UpdatedAt().Equal(at(t)) {
		t.Fatalf("updated at = %s, want %s", opened.UpdatedAt(), at(t))
	}
	if opened.Currency().Code() != "BRL" {
		t.Fatalf("currency = %s, want BRL", opened.Currency().Code())
	}
}
