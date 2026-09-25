//go:build integration

// The publication queue against a real PostgreSQL: which row is a candidate,
// what the claim of one replica does to the other, and what the three ends of a
// turn write when the token has moved on.
package uow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/postgres"
)

func TestDue_offersTheOldestRowOfTheWalletAndHidesTheOnesBehindIt(t *testing.T) {
	ctx, pool, unit := open(t)
	queue := postgres.NewOutboxQueue(pool)
	host := walletWithEvents(ctx, t, unit, 2)
	offered := candidatesOf(ctx, t, queue, host.wallet)
	if len(offered) != 1 || offered[0].EventID != host.events[0] {
		t.Fatalf("candidates = %v, want only the oldest event %s", offered, host.events[0])
	}
}

func TestDue_offersTheNextRowOnceTheOneAheadIsDead(t *testing.T) {
	ctx, pool, unit := open(t)
	queue := postgres.NewOutboxQueue(pool)
	host := walletWithEvents(ctx, t, unit, 2)
	kill(ctx, t, queue, host.events[0])
	offered := candidatesOf(ctx, t, queue, host.wallet)
	if len(offered) != 1 || offered[0].EventID != host.events[1] {
		t.Fatalf("candidates after the death = %v, want the second event %s", offered, host.events[1])
	}
}

func TestClaim_isSkippedByTheSecondReplicaAndComesBackWhenTheLeaseExpires(t *testing.T) {
	ctx, pool, unit := open(t)
	queue := postgres.NewOutboxQueue(pool)
	host := walletWithEvents(ctx, t, unit, 1)
	first := claim(ctx, t, queue, host.events[0])
	if first.LeaseToken == "" {
		t.Fatalf("lease token = %q, want a new one", first.LeaseToken)
	}
	_, err := queue.Claim(ctx, host.events[0], time.Minute)
	if !errors.Is(err, storage.ErrOutboxEventNotFound) {
		t.Fatalf("second claim under a live lease = %v, want ErrOutboxEventNotFound", err)
	}
	expire(ctx, t, host.events[0])
	second := claim(ctx, t, queue, host.events[0])
	if second.LeaseToken == first.LeaseToken {
		t.Fatalf("token after the expiry = %s, want one other than %s", second.LeaseToken, first.LeaseToken)
	}
}

func TestConfirm_marksNothingWhenTheTokenHasMovedOn(t *testing.T) {
	ctx, pool, unit := open(t)
	queue := postgres.NewOutboxQueue(pool)
	host := walletWithEvents(ctx, t, unit, 1)
	stale := claim(ctx, t, queue, host.events[0])
	expire(ctx, t, host.events[0])
	fresh := claim(ctx, t, queue, host.events[0])
	if err := queue.Confirm(ctx, host.events[0], stale.LeaseToken, time.Now()); !errors.Is(err, storage.ErrLeaseLost) {
		t.Fatalf("Confirm under the stale token = %v, want ErrLeaseLost", err)
	}
	assertNotPublished(ctx, t, host.events[0])
	if err := queue.Confirm(ctx, host.events[0], fresh.LeaseToken, time.Now()); err != nil {
		t.Fatalf("Confirm under the fresh token = %v, want nil", err)
	}
}

func TestReschedule_countsTheAttemptAndReleasesTheRowWithoutPublishingIt(t *testing.T) {
	ctx, pool, unit := open(t)
	queue := postgres.NewOutboxQueue(pool)
	host := walletWithEvents(ctx, t, unit, 1)
	claimed := claim(ctx, t, queue, host.events[0])
	if err := queue.Reschedule(ctx, host.events[0], claimed.LeaseToken, time.Now().Add(-time.Second)); err != nil {
		t.Fatalf("Reschedule = %v, want nil", err)
	}
	assertNotPublished(ctx, t, host.events[0])
	again := claim(ctx, t, queue, host.events[0])
	if again.Attempts != 1 {
		t.Fatalf("attempts on the second claim = %d, want 1", again.Attempts)
	}
}

// The database is the one that refuses a row that is both published and given
// up on, whatever the relay asks for.
func TestKill_leavesTheRowUnpublishedAndTheDatabaseRefusesBothAtOnce(t *testing.T) {
	ctx, pool, unit := open(t)
	queue := postgres.NewOutboxQueue(pool)
	host := walletWithEvents(ctx, t, unit, 1)
	kill(ctx, t, queue, host.events[0])
	assertNotPublished(ctx, t, host.events[0])
	refused := `UPDATE outbox_events SET published_at = now() WHERE event_id = $1`
	_, err := connect(ctx, t).Exec(ctx, refused, host.events[0].String())
	assertRefused(t, err, "outbox_events_published_and_dead_do_not_coexist")
}

// outboxHost is one wallet and the events written for it, oldest first.
type outboxHost struct {
	wallet identity.WalletID
	events []identity.EventID
}

func walletWithEvents(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, count int) outboxHost {
	t.Helper()
	opened := opening(t)
	host := outboxHost{wallet: opened.wallet.ID()}
	err := unit.Within(ctx, func(tx storage.Tx) error {
		if err := writeSet(ctx, tx, opened); err != nil {
			return err
		}
		for range count {
			recorded := processedEvent(t, opened)
			if err := tx.Outbox().Insert(ctx, recorded); err != nil {
				return err
			}
			host.events = append(host.events, recorded.ID())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("write the outbox rows = %v, want nil", err)
	}
	return host
}

func candidatesOf(ctx context.Context, t *testing.T, queue *postgres.OutboxQueue, host identity.WalletID) []storage.OutboxCandidate {
	t.Helper()
	// The suite shares the database with every other case, so the scan is read
	// back filtered to the wallet this case opened.
	due, err := queue.Due(ctx, 500)
	if err != nil {
		t.Fatalf("Due = %v, want nil", err)
	}
	var mine []storage.OutboxCandidate
	for _, candidate := range due {
		if candidate.WalletID == host {
			mine = append(mine, candidate)
		}
	}
	return mine
}

func claim(ctx context.Context, t *testing.T, queue *postgres.OutboxQueue, id identity.EventID) storage.OutboxRow {
	t.Helper()
	claimed, err := queue.Claim(ctx, id, time.Minute)
	if err != nil {
		t.Fatalf("Claim = %v, want nil", err)
	}
	return claimed
}

// kill claims the row at the head and gives up on it, which is what the tenth
// permanent refusal of the broker leaves behind.
func kill(ctx context.Context, t *testing.T, queue *postgres.OutboxQueue, id identity.EventID) {
	t.Helper()
	claimed := claim(ctx, t, queue, id)
	if err := queue.Kill(ctx, id, claimed.LeaseToken, time.Now()); err != nil {
		t.Fatalf("Kill = %v, want nil", err)
	}
}

// expire moves the lease of the row into the past, which is what a replica that
// stopped without confirming leaves behind.
func expire(ctx context.Context, t *testing.T, id identity.EventID) {
	t.Helper()
	statement := `UPDATE outbox_events SET lease_until = now() - interval '1 second' WHERE event_id = $1`
	if _, err := connect(ctx, t).Exec(ctx, statement, id.String()); err != nil {
		t.Fatalf("expire the lease = %v, want nil", err)
	}
}

func assertNotPublished(ctx context.Context, t *testing.T, id identity.EventID) {
	t.Helper()
	var published *time.Time
	query := `SELECT published_at FROM outbox_events WHERE event_id = $1`
	if err := connect(ctx, t).QueryRow(ctx, query, id.String()).Scan(&published); err != nil {
		t.Fatalf("read published_at = %v, want nil", err)
	}
	if published != nil {
		t.Fatalf("published_at = %v, want the row still unpublished", published)
	}
}

// assertRefused names the constraint that must have refused the write, so a row
// that fails for another reason does not pass as the case under test.
func assertRefused(t *testing.T, err error, constraint string) {
	t.Helper()
	var refusal *pgconn.PgError
	if !errors.As(err, &refusal) || refusal.ConstraintName != constraint {
		t.Fatalf("write = %v, want a refusal by %s", err, constraint)
	}
}
