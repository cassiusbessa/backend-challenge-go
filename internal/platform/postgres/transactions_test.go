package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// stubTx answers the only two calls a repository of this package makes on an open
// transaction. Every other method of pgx.Tx is unreachable from here, and the nil
// embedding says so: a call to one panics instead of passing quietly.
type stubTx struct {
	pgx.Tx
	row pgx.Row
	tag pgconn.CommandTag
	err error
}

func (s stubTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return s.row
}

func (s stubTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return s.tag, s.err
}

// scanError is a row the driver could not hand over for a reason other than the
// row not being there.
type scanError struct{ err error }

func (s scanError) Scan(...any) error { return s.err }

// ByKey is the fast path of a replay, and the absence is its ordinary answer: the
// key was never written, so there is nothing to replay.
func TestByKey_answersTheAbsenceWithTheOperationThatLookedForIt(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{row: refusingRow{}}}
	state, err := repository.ByKey(context.Background(), providerIdentity(t), keyIdentity(t))
	assertAbsence(t, err, "read transaction by key", storage.ErrTransactionNotFound)
	if !state.ID.IsZero() {
		t.Fatalf("identity beside the absence = %s, want the zero value", state.ID)
	}
}

// Anything other than the absent row is infrastructure: it carries the stack and
// never answers as an absence, which is what keeps a broken connection out of a
// 404.
func TestByKey_leavesAnythingElseAsInfrastructure(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{row: scanError{err: errors.New("connection reset by peer")}}}
	_, err := repository.ByKey(context.Background(), providerIdentity(t), keyIdentity(t))
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a reset connection = %v, want it apart from an absence", err)
	}
	if frames := fault.Stack(err); len(frames) == 0 {
		t.Fatalf("frames of a reset connection = %d, want the stack of an infrastructure failure", len(frames))
	}
}

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
