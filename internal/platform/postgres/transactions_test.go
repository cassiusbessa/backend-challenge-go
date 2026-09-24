package postgres

import (
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func TestAbsent_answersNullForAFieldTheRowDoesNotCarry(t *testing.T) {
	t.Parallel()
	if got := absent(""); got != nil {
		t.Fatalf("absent of empty = %v, want nil", got)
	}
	if got := absent("provider-a"); got != "provider-a" {
		t.Fatalf("absent of a value = %v, want provider-a", got)
	}
}

func TestCents_tellsAnUnsetAmountFromABalanceOfZero(t *testing.T) {
	t.Parallel()
	if got := cents(money.Money{}); got != nil {
		t.Fatalf("cents of the zero value = %v, want nil", got)
	}
	zero, err := money.Parse("0.00", "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	if got := cents(zero); got != int64(0) {
		t.Fatalf("cents of a zero balance = %v, want 0", got)
	}
}

func TestInstant_answersNullForAnInstantThatWasNeverSet(t *testing.T) {
	t.Parallel()
	if got := instant(time.Time{}); got != nil {
		t.Fatalf("instant of the zero value = %v, want nil", got)
	}
	at := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	if got := instant(at); got != at {
		t.Fatalf("instant of a value = %v, want %v", got, at)
	}
}
