package postgres

import (
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func TestBalanceOf_rebuildsTheStoredMoneyFromTheCentsAndTheCode(t *testing.T) {
	t.Parallel()
	rebuilt, err := balanceOf("BRL", 2500)
	if err != nil {
		t.Fatalf("balanceOf = %v, want nil", err)
	}
	if rebuilt.Amount() != "25.00" {
		t.Fatalf("amount = %s, want 25.00 from 2500 cents", rebuilt.Amount())
	}
	if rebuilt.Currency().Code() != "BRL" {
		t.Fatalf("currency = %s, want BRL from the stored code", rebuilt.Currency().Code())
	}
}

// Zero is a balance a wallet is allowed to hold, so it rebuilds like any other.
func TestBalanceOf_rebuildsABalanceOfZero(t *testing.T) {
	t.Parallel()
	rebuilt, err := balanceOf("BRL", 0)
	if err != nil {
		t.Fatalf("balanceOf of zero cents = %v, want nil", err)
	}
	if !rebuilt.IsZero() {
		t.Fatalf("IsZero() = %t, want true for a balance of zero cents", rebuilt.IsZero())
	}
}

// A code the domain does not know is a row this context did not write, and the
// refusal comes back instead of a Money with no currency.
func TestBalanceOf_refusesACodeOutsideTheVocabulary(t *testing.T) {
	t.Parallel()
	rebuilt, err := balanceOf("BRLL", 2500)
	if err == nil {
		t.Fatalf("balanceOf of an unknown code = %s with no error, want a refusal", rebuilt)
	}
	if rebuilt != (money.Money{}) {
		t.Fatalf("money = %v, want the zero value beside the refusal", rebuilt)
	}
}
