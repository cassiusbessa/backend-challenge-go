package ledger

import (
	"errors"
	"testing"
)

func TestParseDirection_readsTheTwoStoredTokensBack(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]Direction{"DEBIT": Debit, "CREDIT": Credit} {
		t.Run(text+" is read back", func(t *testing.T) {
			got, err := ParseDirection(text)
			if err != nil {
				t.Fatalf("ParseDirection(%q) = %v, want nil", text, err)
			}
			if got != want {
				t.Fatalf("direction of %q = %s, want %s", text, got, want)
			}
		})
	}
}

func TestParseDirection_refusesAWordOutsideTheTwoTokens(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "debit", "TRANSFER"} {
		t.Run("the word "+text+" is refused", func(t *testing.T) {
			got, err := ParseDirection(text)
			if !errors.Is(err, ErrInvalidDirection) {
				t.Fatalf("ParseDirection(%q) error = %v, want ErrInvalidDirection", text, err)
			}
			if !got.IsZero() {
				t.Fatalf("direction of the refused word = %s, want the zero direction", got)
			}
		})
	}
}
