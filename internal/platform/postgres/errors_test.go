package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

func TestWrap_turnsTheWalletUniqueViolationIntoARefusal(t *testing.T) {
	t.Parallel()
	err := wrap("insert wallet", &pgconn.PgError{Code: uniqueViolation, ConstraintName: walletUniqueConstraint})
	if !errors.Is(err, storage.ErrWalletExists) {
		t.Fatalf("wrap = %v, want %v", err, storage.ErrWalletExists)
	}
	if frames := fault.Stack(err); len(frames) != 0 {
		t.Fatalf("frames = %d, want 0: a refusal of the contract carries no stack", len(frames))
	}
}

func TestWrap_turnsTheKeyIndexIntoTheIdempotencyConflict(t *testing.T) {
	t.Parallel()
	err := wrap("insert transaction", &pgconn.PgError{Code: uniqueViolation, ConstraintName: keyUniqueIndex})
	assertRejection(t, err, wager.IdempotencyConflict)
	if errors.Is(err, storage.ErrWalletExists) {
		t.Fatalf("wrap = %v, want it not to answer as a duplicate wallet", err)
	}
	if frames := fault.Stack(err); len(frames) != 0 {
		t.Fatalf("frames = %d, want 0: a business rejection carries no stack", len(frames))
	}
}

func TestWrap_turnsTheExternalIndexIntoTheDuplicateTransaction(t *testing.T) {
	t.Parallel()
	err := wrap("insert transaction", &pgconn.PgError{Code: uniqueViolation, ConstraintName: externalUniqueIndex})
	assertRejection(t, err, wager.DuplicateExternalTransaction)
}

// The opening index is not one of the three this adapter reads back, so it stays
// infrastructure: a token invented for it would reach the provider with no rule
// behind it.
func TestWrap_leavesAnUnmappedUniqueViolationAsInfrastructure(t *testing.T) {
	t.Parallel()
	err := wrap("insert transaction", &pgconn.PgError{Code: uniqueViolation, ConstraintName: "wager_transactions_one_opening_per_wallet"})
	var rejection wager.Rejection
	if errors.As(err, &rejection) {
		t.Fatalf("wrap = %v, want no business rejection for an index outside the three", err)
	}
	if frames := fault.Stack(err); len(frames) == 0 {
		t.Fatalf("frames of an index outside the three = %d, want the stack of an infrastructure failure", len(frames))
	}
}

func TestWrap_leavesAnotherSQLStateAsInfrastructure(t *testing.T) {
	t.Parallel()
	err := wrap("insert transaction", &pgconn.PgError{Code: "23514", ConstraintName: keyUniqueIndex})
	var rejection wager.Rejection
	if errors.As(err, &rejection) {
		t.Fatalf("wrap = %v, want a check violation to stay infrastructure", err)
	}
}

func assertRejection(t *testing.T, err error, want wager.FailureCode) {
	t.Helper()
	var rejection wager.Rejection
	if !errors.As(err, &rejection) {
		t.Fatalf("wrap = %v, want a business rejection", err)
	}
	if rejection.Code() != want {
		t.Fatalf("failure code = %s, want %s", rejection.Code(), want)
	}
}

func TestWrap_capturesTheStackOfATransientFailure(t *testing.T) {
	t.Parallel()
	err := wrap("acquire connection", errors.New("connection reset by peer"))
	if errors.Is(err, storage.ErrWalletExists) {
		t.Fatalf("wrap = %v, want it classified as infrastructure", err)
	}
	if frames := fault.Stack(err); len(frames) == 0 {
		t.Fatalf("frames of a reset connection = %d, want the stack of an infrastructure failure", len(frames))
	}
}

func TestWrap_namesTheOperation(t *testing.T) {
	t.Parallel()
	err := wrap("insert wallet", &pgconn.PgError{Code: uniqueViolation, ConstraintName: walletUniqueConstraint})
	want := "insert wallet: " + storage.ErrWalletExists.Error()
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}

func TestWrap_answersNilForNoFailure(t *testing.T) {
	t.Parallel()
	if err := wrap("insert wallet", nil); err != nil {
		t.Fatalf("wrap of nil = %v, want nil", err)
	}
}

// assertAbsence checks the whole answer an absence owes: the operation that
// looked for the row, the sentinel of that entity, and the family every absence
// belongs to.
func assertAbsence(t *testing.T, err error, op string, sentinel error) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("absence = %v, want %v", err, sentinel)
	}
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("absence = %v, want the family %v", err, storage.ErrNotFound)
	}
	if want := op + ": " + sentinel.Error(); err.Error() != want {
		t.Fatalf("message of the absence = %q, want %q", err.Error(), want)
	}
}

// A lock and a read look for the same row, so the operation is the only thing
// that tells the two absences apart once they reach the log.
func TestMissingWallet_namesTheOperationThatLookedForTheRow(t *testing.T) {
	t.Parallel()
	locked := missingWallet("lock wallet", pgx.ErrNoRows)
	read := missingWallet("read wallet", pgx.ErrNoRows)
	assertAbsence(t, locked, "lock wallet", storage.ErrWalletNotFound)
	assertAbsence(t, read, "read wallet", storage.ErrWalletNotFound)
	if locked.Error() == read.Error() {
		t.Fatalf("both queries answered %q, want the operation to tell them apart", locked.Error())
	}
}

func TestMissingWallet_leavesAnythingElseAsInfrastructure(t *testing.T) {
	t.Parallel()
	err := missingWallet("read wallet", errors.New("connection reset by peer"))
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a reset connection = %v, want it classified as infrastructure", err)
	}
	if frames := fault.Stack(err); len(frames) == 0 {
		t.Fatalf("frames of a reset connection under missingWallet = %d, want the stack it captured", len(frames))
	}
}

func TestMissingTransaction_namesTheOperationThatLookedForTheRow(t *testing.T) {
	t.Parallel()
	byKey := missingTransaction("read transaction by key", pgx.ErrNoRows)
	byID := missingTransaction("read transaction", pgx.ErrNoRows)
	assertAbsence(t, byKey, "read transaction by key", storage.ErrTransactionNotFound)
	assertAbsence(t, byID, "read transaction", storage.ErrTransactionNotFound)
	if byKey.Error() == byID.Error() {
		t.Fatalf("both lookups = %q, want the operation to tell them apart", byKey.Error())
	}
}

func TestMissingTransaction_leavesAnythingElseAsInfrastructure(t *testing.T) {
	t.Parallel()
	err := missingTransaction("read transaction", errors.New("connection reset by peer"))
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a reset connection on a transaction = %v, want it classified as infrastructure", err)
	}
}

// Each index carries its own answer, and an index outside the three carries none:
// a token invented for it would reach the provider with no rule behind it.
func TestDuplicateOf_answersOnlyForTheThreeIndexesThisAdapterReadsBack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		constraint string
		sentinel   error
		code       wager.FailureCode
	}{
		{name: "the wallet index", constraint: walletUniqueConstraint, sentinel: storage.ErrWalletExists},
		{name: "the key index", constraint: keyUniqueIndex, code: wager.IdempotencyConflict},
		{name: "the external id index", constraint: externalUniqueIndex, code: wager.DuplicateExternalTransaction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refusal := duplicateOf(tc.constraint)
			if tc.sentinel != nil && !errors.Is(refusal, tc.sentinel) {
				t.Fatalf("refusal of %s = %v, want %v", tc.name, refusal, tc.sentinel)
			}
			if tc.code.String() != "" {
				assertRejection(t, refusal, tc.code)
			}
		})
	}
	if refusal := duplicateOf("wager_transactions_one_opening_per_wallet"); refusal != nil {
		t.Fatalf("refusal of an index outside the three = %v, want nil", refusal)
	}
}
