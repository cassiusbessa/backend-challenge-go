package wallet

import (
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func moveSpec(t *testing.T, last string, amount money.Money) MoveSpec {
	t.Helper()
	return MoveSpec{
		EntryID:       entryOf(t, last),
		TransactionID: transactionOf(t),
		Amount:        amount,
		At:            at(t).Add(time.Hour),
	}
}

func TestDebit_reportsTheFollowingState(t *testing.T) {
	t.Parallel()
	opened := openedWith(t, 10000)
	result, err := opened.Debit(moveSpec(t, "2", brl(t, 2500)))
	if err != nil {
		t.Fatalf("Debit of 25.00 over 100.00 error = %v, want nil", err)
	}
	if result.Balance().Amount() != "75.00" {
		t.Fatalf("balance after the debit = %s, want 75.00", result.Balance().Amount())
	}
	if result.Version() != 2 {
		t.Fatalf("version after the debit = %d, want 2", result.Version())
	}
	if opened.Balance().Amount() != "75.00" {
		t.Fatalf("wallet balance after the debit = %s, want 75.00", opened.Balance().Amount())
	}
}

func TestDebit_yieldsTheEntryWithBothBalances(t *testing.T) {
	t.Parallel()
	opened := openedWith(t, 10000)
	result, err := opened.Debit(moveSpec(t, "2", brl(t, 2500)))
	if err != nil {
		t.Fatalf("Debit before reading the entry error = %v, want nil", err)
	}
	entry, ok := result.Entry()
	if !ok {
		t.Fatalf("a debit yielded no entry, want one")
	}
	if entry.Direction() != ledger.Debit {
		t.Fatalf("entry direction = %s, want DEBIT", entry.Direction())
	}
	if entry.BalanceBefore().Amount() != "100.00" {
		t.Fatalf("entry balance before = %s, want 100.00", entry.BalanceBefore().Amount())
	}
	if entry.BalanceAfter().Amount() != "75.00" {
		t.Fatalf("entry balance after = %s, want 75.00", entry.BalanceAfter().Amount())
	}
}

func TestMove_numbersTheEntryWithTheResultingVersion(t *testing.T) {
	t.Parallel()
	opened := openedWith(t, 10000)
	result, err := opened.Debit(moveSpec(t, "2", brl(t, 2500)))
	if err != nil {
		t.Fatalf("Debit before reading the sequence error = %v, want nil", err)
	}
	entry, _ := result.Entry()
	if entry.Sequence() != result.Version() {
		t.Fatalf("entry sequence = %d, want the resulting version %d", entry.Sequence(), result.Version())
	}
	if entry.Sequence() != 2 {
		t.Fatalf("first movement sequence = %d, want 2", entry.Sequence())
	}
}

func TestCredit_reportsTheFollowingState(t *testing.T) {
	t.Parallel()
	opened := openedWith(t, 10000)
	if _, err := opened.Debit(moveSpec(t, "2", brl(t, 2500))); err != nil {
		t.Fatalf("Debit before the credit error = %v, want nil", err)
	}
	result, err := opened.Credit(moveSpec(t, "3", brl(t, 3000)))
	if err != nil {
		t.Fatalf("Credit of 30.00 over 75.00 error = %v, want nil", err)
	}
	if result.Balance().Amount() != "105.00" {
		t.Fatalf("balance after the credit = %s, want 105.00", result.Balance().Amount())
	}
	if result.Version() != 3 {
		t.Fatalf("version after the credit = %d, want 3", result.Version())
	}
}

func TestDebit_equalToTheBalanceIsAccepted(t *testing.T) {
	t.Parallel()
	opened := openedWith(t, 10000)
	result, err := opened.Debit(moveSpec(t, "2", brl(t, 10000)))
	if err != nil {
		t.Fatalf("Debit of exactly the balance error = %v, want nil", err)
	}
	if !result.Balance().IsZero() {
		t.Fatalf("balance after draining the wallet = %s, want 0.00", result.Balance().Amount())
	}
	if result.Version() != 2 {
		t.Fatalf("version after draining the wallet = %d, want 2", result.Version())
	}
}

func TestDebit_oneCentAboveTheBalanceLeavesTheWalletUntouched(t *testing.T) {
	t.Parallel()
	opened := openedWith(t, 10000)
	result, err := opened.Debit(moveSpec(t, "2", brl(t, 10001)))
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("Debit of 100.01 over 100.00 error = %v, want ErrInsufficientFunds", err)
	}
	if _, ok := result.Entry(); ok {
		t.Fatalf("a refused debit yielded an entry, want none")
	}
	if opened.Balance().Amount() != "100.00" {
		t.Fatalf("balance after the refused debit = %s, want 100.00", opened.Balance().Amount())
	}
	if opened.Version() != 1 {
		t.Fatalf("version after the refused debit = %d, want 1", opened.Version())
	}
}

func TestMove_refusesAnotherCurrencyWithoutTouchingTheWallet(t *testing.T) {
	t.Parallel()
	opened := openedWith(t, 10000)
	_, err := opened.Debit(moveSpec(t, "2", amountIn(t, 2500, "USD")))
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Debit in USD over a BRL wallet error = %v, want ErrCurrencyMismatch", err)
	}
	if opened.Balance().Amount() != "100.00" {
		t.Fatalf("balance after the refused currency = %s, want 100.00", opened.Balance().Amount())
	}
	if opened.Version() != 1 {
		t.Fatalf("version after the refused currency = %d, want 1", opened.Version())
	}
}

func TestMove_refusesAnAmountThatIsNotPositive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cents int64
	}{
		{name: "zero moves nothing", cents: 0},
		{name: "a negative amount is not a movement", cents: -2500},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			opened := openedWith(t, 10000)
			_, err := opened.Credit(moveSpec(t, "2", brl(t, testCase.cents)))
			if !errors.Is(err, ErrNonPositiveAmount) {
				t.Fatalf("Credit of %d cents error = %v, want ErrNonPositiveAmount", testCase.cents, err)
			}
			if opened.Version() != 1 {
				t.Fatalf("version after a %d cent movement = %d, want 1", testCase.cents, opened.Version())
			}
		})
	}
}

func TestMove_refusesTheMovementWithoutTheLedgerIdentifiers(t *testing.T) {
	t.Parallel()
	opened := openedWith(t, 10000)
	spec := moveSpec(t, "2", brl(t, 2500))
	spec.At = time.Time{}
	_, err := opened.Debit(spec)
	if !errors.Is(err, ledger.ErrIncompleteEntry) {
		t.Fatalf("Debit without an instant error = %v, want ErrIncompleteEntry", err)
	}
	if opened.Balance().Amount() != "100.00" {
		t.Fatalf("balance after the refused entry = %s, want 100.00", opened.Balance().Amount())
	}
}

func TestMove_carriesTheInstantOfTheMovementIntoTheWallet(t *testing.T) {
	t.Parallel()
	opened := openedWith(t, 10000)
	spec := moveSpec(t, "2", brl(t, 2500))
	if _, err := opened.Debit(spec); err != nil {
		t.Fatalf("Debit before reading the instant error = %v, want nil", err)
	}
	if !opened.UpdatedAt().Equal(spec.At) {
		t.Fatalf("updated at = %s, want the movement instant %s", opened.UpdatedAt(), spec.At)
	}
	if !opened.CreatedAt().Equal(at(t)) {
		t.Fatalf("created at moved to %s, want the opening instant %s", opened.CreatedAt(), at(t))
	}
}
