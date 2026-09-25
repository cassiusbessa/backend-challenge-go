package storage

import (
	"errors"
	"fmt"
	"testing"
)

// The adapter answers an absence wrapped in the operation that looked for the
// row, so every assertion here reads the chain and not the bare value.
func chained(absence error) error {
	return fmt.Errorf("read row: %w", absence)
}

func TestNotFoundError_matchesTheEntityAndTheFamilyForTheSameError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		entity error
		other  error
	}{
		{name: "an absent wallet", err: chained(ErrWalletNotFound), entity: ErrWalletNotFound, other: ErrTransactionNotFound},
		{name: "an absent transaction", err: chained(ErrTransactionNotFound), entity: ErrTransactionNotFound, other: ErrWalletNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errors.Is(tc.err, tc.entity); !got {
				t.Fatalf("match of %s against its own sentinel = %t, want true", tc.name, got)
			}
			if got := errors.Is(tc.err, ErrNotFound); !got {
				t.Fatalf("match of %s against the family = %t, want true", tc.name, got)
			}
			if got := errors.Is(tc.err, tc.other); got {
				t.Fatalf("match of %s against the other entity = %t, want false: the family must not collapse the two", tc.name, got)
			}
		})
	}
}

// The absence carries the entity, which is what a caller reads to tell a row that
// was never written from one it is not allowed to see.
func TestNotFoundError_carriesTheEntityOutOfTheChain(t *testing.T) {
	t.Parallel()
	var absence NotFoundError
	chain := chained(ErrTransactionNotFound)
	if got := errors.As(chain, &absence); !got {
		t.Fatalf("errors.As of %v = %t, want true", chain, got)
	}
	if got, want := absence.Entity, "wager transaction"; got != want {
		t.Fatalf("entity = %q, want %q", got, want)
	}
}

func TestError_namesTheEntityAndNotTheIdentityThatWasLookedUp(t *testing.T) {
	t.Parallel()
	if got, want := ErrWalletNotFound.Error(), "storage: wallet does not exist"; got != want {
		t.Fatalf("message of an absent wallet = %q, want %q", got, want)
	}
}

func TestUnwrap_answersTheFamilyForAnEntityThisPackageDoesNotList(t *testing.T) {
	t.Parallel()
	if got := (NotFoundError{Entity: "ledger entry"}).Unwrap(); !errors.Is(got, ErrNotFound) {
		t.Fatalf("family of an unlisted entity = %v, want %v", got, ErrNotFound)
	}
}
