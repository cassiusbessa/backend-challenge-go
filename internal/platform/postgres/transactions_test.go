package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
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

// The cited operation is found the way the provider names it, and the absence is
// its ordinary answer: the operation being cited has not arrived yet.
func TestByExternalID_answersTheAbsenceWithTheOperationThatLookedForIt(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{row: refusingRow{}}}
	state, err := repository.ByExternalID(context.Background(), providerIdentity(t), externalIdentity(t))
	assertAbsence(t, err, "read cited transaction", storage.ErrTransactionNotFound)
	if !state.ID.IsZero() {
		t.Fatalf("identity beside the absent cited operation = %s, want the zero value", state.ID)
	}
}

func TestByExternalID_leavesAnythingElseAsInfrastructure(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{row: scanError{err: errors.New("connection reset by peer")}}}
	_, err := repository.ByExternalID(context.Background(), providerIdentity(t), externalIdentity(t))
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a reset connection on the cited operation = %v, want it apart from an absence", err)
	}
	if frames := fault.Stack(err); len(frames) == 0 {
		t.Fatalf("frames of a reset connection on the cited operation = %d, want the stack", len(frames))
	}
}

// The question answers what the row said, and a failure of it is never read as
// "no reversal": a false taken from a broken query would let a second reversal
// through.
func TestHasProcessedReversal_answersTheRowAndKeepsAFailureApartFromAFalse(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{row: existsRow{exists: true}}}
	reversed, err := repository.HasProcessedReversal(context.Background(), providerIdentity(t), externalIdentity(t))
	if err != nil || !reversed {
		t.Fatalf("HasProcessedReversal = %t with %v, want true with nil", reversed, err)
	}
	broken := transactions{tx: stubTx{row: scanError{err: errors.New("connection reset by peer")}}}
	answered, err := broken.HasProcessedReversal(context.Background(), providerIdentity(t), externalIdentity(t))
	if err == nil {
		t.Fatalf("HasProcessedReversal over a broken query = %t with nil, want the failure", answered)
	}
	if answered {
		t.Fatalf("answer beside the failure = %t, want false", answered)
	}
}

// A row another replica holds is skipped rather than waited on, and it answers
// the absence: this replica is not the one deciding that wait now.
func TestClaimWait_answersTheAbsenceForARowItDidNotGet(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{row: refusingRow{}}}
	claimed, err := repository.ClaimWait(context.Background(), transactionIdentity(t), waitStamp())
	assertAbsence(t, err, "claim reference wait", storage.ErrTransactionNotFound)
	if claimed.Attempts != 0 || !claimed.State.ID.IsZero() {
		t.Fatalf("wait beside the absence = %+v, want the zero value", claimed)
	}
}

func TestClaimWait_leavesAnythingElseAsInfrastructure(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{row: scanError{err: errors.New("connection reset by peer")}}}
	_, err := repository.ClaimWait(context.Background(), transactionIdentity(t), waitStamp())
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a reset connection on the claim = %v, want it apart from an absence", err)
	}
}

// No row affected is the row having left the wait between the claim and the
// write, which is an absence and not a failure of the database.
func TestEndWait_answersTheAbsenceWhenNoRowInTheWaitMatched(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{tag: pgconn.NewCommandTag("UPDATE 0")}}
	err := repository.EndWait(context.Background(), closedWait(t))
	assertAbsence(t, err, "end reference wait", storage.ErrTransactionNotFound)
}

func TestEndWait_answersNilWhenTheRowWasWritten(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{tag: pgconn.NewCommandTag("UPDATE 1")}}
	if err := repository.EndWait(context.Background(), closedWait(t)); err != nil {
		t.Fatalf("EndWait over a row in the wait = %v, want nil", err)
	}
}

func TestRescheduleWait_answersTheAbsenceWhenNoRowInTheWaitMatched(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{tag: pgconn.NewCommandTag("UPDATE 0")}}
	err := repository.RescheduleWait(context.Background(), transactionIdentity(t), waitStamp(), waitStamp())
	assertAbsence(t, err, "reschedule reference wait", storage.ErrTransactionNotFound)
}

func TestRescheduleWait_answersNilWhenTheScheduleWasWritten(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{tag: pgconn.NewCommandTag("UPDATE 1")}}
	err := repository.RescheduleWait(context.Background(), transactionIdentity(t), waitStamp(), waitStamp())
	if err != nil {
		t.Fatalf("RescheduleWait over a row in the wait = %v, want nil", err)
	}
}

// A database that refused the statement is infrastructure, never the absence of
// the row: the two send the caller down different paths.
func TestWriteWait_keepsAFailureOfTheStatementApartFromAnAbsence(t *testing.T) {
	t.Parallel()
	repository := transactions{tx: stubTx{err: errors.New("connection reset by peer")}}
	err := repository.writeWait(context.Background(), "end reference wait", endWaitRow)
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a refused statement = %v, want it apart from an absence", err)
	}
	if frames := fault.Stack(err); len(frames) == 0 {
		t.Fatalf("frames of a refused statement = %d, want the stack of an infrastructure failure", len(frames))
	}
}

// existsRow is what the question of the reversal reads back: one boolean and
// nothing else.
type existsRow struct{ exists bool }

func (r existsRow) Scan(into ...any) error {
	*(into[0].(*bool)) = r.exists
	return nil
}

func waitStamp() time.Time {
	return time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
}

// closedWait is a wait the worker decided: the write over it names the terminal
// status and the token, and never the deadline.
func closedWait(t *testing.T) *wager.Transaction {
	t.Helper()
	waiting, err := wager.Rehydrate(wager.State{
		ID:                  transactionIdentity(t),
		Kind:                wager.KindWin,
		PlayerID:            playerIdentity(t),
		WalletID:            walletIdentity(t),
		Status:              wager.PendingReference,
		NextAttemptAt:       waitStamp(),
		ReferenceDeadlineAt: waitStamp().Add(15 * time.Minute),
		CreatedAt:           waitStamp(),
		UpdatedAt:           waitStamp(),
	})
	if err != nil {
		t.Fatalf("wager.Rehydrate = %v, want nil", err)
	}
	if err := waiting.Reject(wager.ReferenceNotFound, waitStamp()); err != nil {
		t.Fatalf("Reject = %v, want nil", err)
	}
	return waiting
}

func externalIdentity(t *testing.T) identity.ExternalTransactionID {
	t.Helper()
	id, err := identity.ParseExternalTransactionID("external-1")
	if err != nil {
		t.Fatalf("ParseExternalTransactionID = %v, want nil", err)
	}
	return id
}

func playerIdentity(t *testing.T) identity.PlayerID {
	t.Helper()
	id, err := identity.ParsePlayerID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParsePlayerID = %v, want nil", err)
	}
	return id
}
