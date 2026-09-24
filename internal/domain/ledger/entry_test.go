package ledger

import (
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

const (
	entryUUIDPrefix   = "00000000-0000-4000-8000-00000000000"
	walletUUID        = "11111111-1111-4111-8111-111111111111"
	transactionUUID   = "22222222-2222-4222-8222-222222222222"
	otherTransactionU = "33333333-3333-4333-8333-333333333333"
)

func at(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, time.March, 14, 10, 30, 0, 0, time.UTC)
}

func currencyOf(t *testing.T, code string) money.Currency {
	t.Helper()
	currency, err := money.ParseCurrency(code)
	if err != nil {
		t.Fatalf("ParseCurrency(%q) for the fixture error = %v, want nil", code, err)
	}
	return currency
}

func amountOf(t *testing.T, cents int64, code string) money.Money {
	t.Helper()
	value, err := money.FromCents(cents, currencyOf(t, code))
	if err != nil {
		t.Fatalf("FromCents(%d, %s) for the fixture error = %v, want nil", cents, code, err)
	}
	return value
}

func brl(t *testing.T, cents int64) money.Money {
	t.Helper()
	return amountOf(t, cents, "BRL")
}

func entryID(t *testing.T, last string) identity.LedgerEntryID {
	t.Helper()
	parsed, err := identity.ParseLedgerEntryID(entryUUIDPrefix + last)
	if err != nil {
		t.Fatalf("ParseLedgerEntryID ending in %q error = %v, want nil", last, err)
	}
	return parsed
}

func walletOf(t *testing.T) identity.WalletID {
	t.Helper()
	parsed, err := identity.ParseWalletID(walletUUID)
	if err != nil {
		t.Fatalf("ParseWalletID for the fixture error = %v, want nil", err)
	}
	return parsed
}

func transactionOf(t *testing.T, text string) identity.TransactionID {
	t.Helper()
	parsed, err := identity.ParseTransactionID(text)
	if err != nil {
		t.Fatalf("ParseTransactionID for the fixture error = %v, want nil", err)
	}
	return parsed
}

// creditSpec is the scenario of the spec: a wallet holding 75.00 BRL credits
// 30.00 BRL.
func creditSpec(t *testing.T) EntrySpec {
	t.Helper()
	return EntrySpec{
		ID:            entryID(t, "1"),
		WalletID:      walletOf(t),
		TransactionID: transactionOf(t, transactionUUID),
		Direction:     Credit,
		Amount:        brl(t, 3000),
		BalanceBefore: brl(t, 7500),
		BalanceAfter:  brl(t, 10500),
		Sequence:      2,
		CreatedAt:     at(t),
	}
}

func mustEntry(t *testing.T, spec EntrySpec) Entry {
	t.Helper()
	entry, err := NewEntry(spec)
	if err != nil {
		t.Fatalf("NewEntry for the fixture error = %v, want nil", err)
	}
	return entry
}

func TestNewEntry_recordsBothBalancesOfACredit(t *testing.T) {
	t.Parallel()
	entry := mustEntry(t, creditSpec(t))
	if entry.Direction() != Credit {
		t.Fatalf("direction = %s, want CREDIT", entry.Direction())
	}
	if entry.Amount().Amount() != "30.00" {
		t.Fatalf("amount = %s, want 30.00", entry.Amount().Amount())
	}
	if entry.BalanceBefore().Amount() != "75.00" {
		t.Fatalf("balance before = %s, want 75.00", entry.BalanceBefore().Amount())
	}
	if entry.BalanceAfter().Amount() != "105.00" {
		t.Fatalf("balance after = %s, want 105.00", entry.BalanceAfter().Amount())
	}
}

func TestNewEntry_keepsTheIdentityItWasGiven(t *testing.T) {
	t.Parallel()
	entry := mustEntry(t, creditSpec(t))
	if entry.ID() != entryID(t, "1") {
		t.Fatalf("entry id = %s, want %s", entry.ID(), entryID(t, "1"))
	}
	if entry.WalletID() != walletOf(t) {
		t.Fatalf("wallet id = %s, want %s", entry.WalletID(), walletOf(t))
	}
	if entry.TransactionID() != transactionOf(t, transactionUUID) {
		t.Fatalf("transaction id = %s, want %s", entry.TransactionID(), transactionUUID)
	}
}

func TestNewEntry_keepsThePlacementItWasGiven(t *testing.T) {
	t.Parallel()
	entry := mustEntry(t, creditSpec(t))
	if entry.Sequence() != 2 {
		t.Fatalf("sequence = %d, want 2", entry.Sequence())
	}
	if !entry.CreatedAt().Equal(at(t)) {
		t.Fatalf("created at = %s, want %s", entry.CreatedAt(), at(t))
	}
	if entry.IsZero() {
		t.Fatalf("a built entry reported IsZero() = true, want false")
	}
}

func TestNewEntry_refusesTheIncompleteEntry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		spoil func(*EntrySpec)
	}{
		{name: "without an entry id", spoil: func(s *EntrySpec) { s.ID = identity.LedgerEntryID{} }},
		{name: "without a wallet", spoil: func(s *EntrySpec) { s.WalletID = identity.WalletID{} }},
		{name: "without a transaction", spoil: func(s *EntrySpec) { s.TransactionID = identity.TransactionID{} }},
		{name: "with a sequence below one", spoil: func(s *EntrySpec) { s.Sequence = 0 }},
		{name: "with a negative sequence", spoil: func(s *EntrySpec) { s.Sequence = -1 }},
		{name: "without an instant", spoil: func(s *EntrySpec) { s.CreatedAt = time.Time{} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			spec := creditSpec(t)
			testCase.spoil(&spec)
			entry, err := NewEntry(spec)
			if !errors.Is(err, ErrIncompleteEntry) {
				t.Fatalf("NewEntry %s error = %v, want ErrIncompleteEntry", testCase.name, err)
			}
			if !entry.IsZero() {
				t.Fatalf("NewEntry %s produced an entry, want the zero value", testCase.name)
			}
		})
	}
}

func TestNewEntry_refusesAnAmountThatIsNotPositive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cents int64
	}{
		{name: "a zero amount produces no entry", cents: 0},
		{name: "a negative amount produces no entry", cents: -3000},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			spec := creditSpec(t)
			spec.Amount = brl(t, testCase.cents)
			_, err := NewEntry(spec)
			if !errors.Is(err, ErrNonPositiveAmount) {
				t.Fatalf("NewEntry with %d cents error = %v, want ErrNonPositiveAmount", testCase.cents, err)
			}
		})
	}
}

func TestNewEntry_refusesAnAmountOutsideTheWalletCurrency(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		spoil func(*testing.T, *EntrySpec)
	}{
		{
			name:  "the amount is in another currency",
			spoil: func(t *testing.T, s *EntrySpec) { s.Amount = amountOf(t, 3000, "USD") },
		},
		{
			name:  "the balance before is in another currency",
			spoil: func(t *testing.T, s *EntrySpec) { s.BalanceBefore = amountOf(t, 7500, "USD") },
		},
		{
			name:  "the balance after is in another currency",
			spoil: func(t *testing.T, s *EntrySpec) { s.BalanceAfter = amountOf(t, 10500, "USD") },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			spec := creditSpec(t)
			testCase.spoil(t, &spec)
			_, err := NewEntry(spec)
			if !errors.Is(err, ErrCurrencyMismatch) {
				t.Fatalf("NewEntry when %s error = %v, want ErrCurrencyMismatch", testCase.name, err)
			}
		})
	}
}

func TestNewEntry_refusesABalanceThatDoesNotFollowTheDirection(t *testing.T) {
	t.Parallel()
	spec := creditSpec(t)
	spec.BalanceAfter = brl(t, 10501)
	_, err := NewEntry(spec)
	if !errors.Is(err, ErrBalanceMismatch) {
		t.Fatalf("NewEntry with a credit off by one cent error = %v, want ErrBalanceMismatch", err)
	}
}

func TestNewEntry_refusesTheUnsetDirection(t *testing.T) {
	t.Parallel()
	spec := creditSpec(t)
	spec.Direction = noDirection
	_, err := NewEntry(spec)
	if !errors.Is(err, ErrInvalidDirection) {
		t.Fatalf("NewEntry without a direction error = %v, want ErrInvalidDirection", err)
	}
}

func TestNewEntry_subtractsOnADebit(t *testing.T) {
	t.Parallel()
	spec := creditSpec(t)
	spec.Direction = Debit
	spec.Amount = brl(t, 2500)
	spec.BalanceBefore = brl(t, 10000)
	spec.BalanceAfter = brl(t, 7500)
	entry := mustEntry(t, spec)
	if entry.Direction() != Debit {
		t.Fatalf("debit direction = %s, want DEBIT", entry.Direction())
	}
	if entry.BalanceAfter().Amount() != "75.00" {
		t.Fatalf("debit balance after = %s, want 75.00", entry.BalanceAfter().Amount())
	}
}

func TestEntry_doesNotChangeWhenTheCallerAltersWhatItReturned(t *testing.T) {
	t.Parallel()
	entry := mustEntry(t, creditSpec(t))
	copied := entry
	copied.amount = brl(t, 1)
	if entry.Amount().Amount() != "30.00" {
		t.Fatalf("stored amount after altering the copy = %s, want 30.00", entry.Amount().Amount())
	}
}
