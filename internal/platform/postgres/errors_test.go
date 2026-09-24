package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
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

func TestWrap_leavesAnotherUniqueViolationAsInfrastructure(t *testing.T) {
	t.Parallel()
	err := wrap("insert transaction", &pgconn.PgError{Code: uniqueViolation, ConstraintName: "wager_transactions_one_per_provider_and_key"})
	if errors.Is(err, storage.ErrWalletExists) {
		t.Fatalf("wrap = %v, want it not to answer as a duplicate wallet", err)
	}
	if frames := fault.Stack(err); len(frames) == 0 {
		t.Fatalf("frames = 0, want the stack of an infrastructure failure")
	}
}

func TestWrap_capturesTheStackOfATransientFailure(t *testing.T) {
	t.Parallel()
	err := wrap("acquire connection", errors.New("connection reset by peer"))
	if errors.Is(err, storage.ErrWalletExists) {
		t.Fatalf("wrap = %v, want it classified as infrastructure", err)
	}
	if frames := fault.Stack(err); len(frames) == 0 {
		t.Fatalf("frames = 0, want the stack of an infrastructure failure")
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
