package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

// A write of the turn that matched no row is the lease having moved on: the row
// belongs to another replica now, and what this one had in flight applies to
// nothing.
func TestLostLease_answersTheLeaseIsGoneWhenNoRowMatched(t *testing.T) {
	t.Parallel()
	err := lostLease("confirm outbox event", pgconn.NewCommandTag("UPDATE 0"))
	if !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("lostLease over 0 rows = %v, want ErrLeaseLost", err)
	}
	if got := lostLease("confirm outbox event", pgconn.NewCommandTag("UPDATE 1")); got != nil {
		t.Fatalf("lostLease over 1 row = %v, want nil", got)
	}
}

// A pool the process has not opened is a failure of infrastructure and not a
// panic: the relay comes back on the next tick and the database that comes back
// needs no restart.
func TestOutboxQueue_answersTheClosedPoolOnEveryPortOfTheTurn(t *testing.T) {
	t.Parallel()
	queue := NewOutboxQueue(&Pool{})
	ctx := context.Background()
	if _, err := queue.Due(ctx, 10); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Due over a closed pool = %v, want ErrPoolClosed", err)
	}
	if _, err := queue.Claim(ctx, eventIdentity(t), time.Minute); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Claim over a closed pool = %v, want ErrPoolClosed", err)
	}
	if err := queue.Confirm(ctx, eventIdentity(t), "token", time.Time{}); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Confirm over a closed pool = %v, want ErrPoolClosed", err)
	}
}

// A row whose identifiers are not the ones this context writes is a defect of
// the database and not a candidate: it leaves as a failure rather than as an
// identity the rest of the turn would carry.
func TestOutboxCandidateOf_refusesIdentifiersThatAreNotCanonical(t *testing.T) {
	t.Parallel()
	if _, err := outboxCandidateOf("not-a-uuid", walletIdentity(t).String()); !errors.Is(err, identity.ErrInvalidUUID) {
		t.Fatalf("candidate of a malformed event id = %v, want ErrInvalidUUID", err)
	}
	if _, err := outboxCandidateOf(eventIdentity(t).String(), "not-a-uuid"); !errors.Is(err, identity.ErrInvalidUUID) {
		t.Fatalf("candidate of a malformed wallet id = %v, want ErrInvalidUUID", err)
	}
}

func TestRow_refusesAClaimedRowWhoseIdentifiersAreNotCanonical(t *testing.T) {
	t.Parallel()
	_, err := claimedOutboxRow{eventID: "not-a-uuid"}.row()
	if !errors.Is(err, identity.ErrInvalidUUID) {
		t.Fatalf("row of a malformed claim = %v, want ErrInvalidUUID", err)
	}
	if !strings.Contains(err.Error(), "read the claimed outbox row") {
		t.Fatalf("failure = %v, want the operation that read it named in the chain", err)
	}
}
