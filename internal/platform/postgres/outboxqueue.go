package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

// The candidates of the publication queue: per wallet, the oldest row nobody
// holds, and only when no older row of that wallet is still pending.
//
// The NOT EXISTS is what serializes one wallet without serializing the others,
// and a dead row leaves it along with a published one — which is how the tenth
// permanent refusal releases the wallet instead of stopping everything behind
// it.
//
// The order is the sequence the database assigns at the insert, which happens
// under the lock of that wallet: it is therefore the order of the commits, and
// it needs no tie-break. The instant the event happened is not that order —
// it is read before the lock, so two operations of one wallet can commit in
// the opposite order of their instants. go-reads says the same of the ledger.
//
// The partial index of the fourth migration serves exactly this predicate, so
// the published rows — the overwhelming majority of a table the relay keeps up
// with — are not walked.
const selectDueOutboxEvents = `
SELECT event_id, wallet_id
  FROM outbox_events o
 WHERE published_at IS NULL AND dead_at IS NULL
   AND next_attempt_at <= now()
   AND (lease_until IS NULL OR lease_until < now())
   AND NOT EXISTS (
         SELECT 1 FROM outbox_events older
          WHERE older.wallet_id = o.wallet_id
            AND older.published_at IS NULL AND older.dead_at IS NULL
            AND older.publish_seq < o.publish_seq)
 ORDER BY o.publish_seq
 LIMIT $1`

// The next row of one wallet: the oldest one still pending, and only when it can
// be claimed now — due, and held by no live lease. It is the scan narrowed to one
// wallet and walked by the partial index in the order of that wallet, so a chain
// of sends asks for the next row without scanning the whole queue.
//
// The head is taken before the two conditions and not after: filtering first
// would skip a head that is set back on the backoff or held by another replica,
// and answer the row behind it out of order.
const selectNextOutboxEvent = `
SELECT event_id, wallet_id
  FROM (SELECT event_id, wallet_id, next_attempt_at, lease_until
          FROM outbox_events
         WHERE wallet_id = $1 AND published_at IS NULL AND dead_at IS NULL
         ORDER BY publish_seq
         LIMIT 1) head
 WHERE next_attempt_at <= now()
   AND (lease_until IS NULL OR lease_until < now())`

// The claim. The token is new on every turn and minted by the database, so it
// never has to travel from a process that may be about to lose the row, and the
// deadline is measured against the clock of the database for the same reason.
//
// FOR UPDATE SKIP LOCKED is what makes a second replica move on to another
// wallet instead of queueing behind this row.
const claimOutboxEvent = `
UPDATE outbox_events
   SET lease_token = gen_random_uuid(), lease_until = now() + $2::interval
 WHERE event_id = (
         SELECT event_id FROM outbox_events
          WHERE event_id = $1
            AND published_at IS NULL AND dead_at IS NULL
            AND next_attempt_at <= now()
            AND (lease_until IS NULL OR lease_until < now())
          FOR UPDATE SKIP LOCKED)
 RETURNING event_id, wallet_id, event_type, payload,
           coalesce(trace_id, ''), coalesce(span_id, ''),
           attempt_count, refusal_count, lease_token`

// The three ends of one turn. Each is guarded by the token of the claim, so a
// replica whose lease was taken over writes nothing at all.
const (
	confirmOutboxEvent = `
UPDATE outbox_events SET published_at = $3, lease_token = NULL, lease_until = NULL
 WHERE event_id = $1 AND lease_token = $2 AND published_at IS NULL AND dead_at IS NULL`

	rescheduleOutboxEvent = `
UPDATE outbox_events
   SET next_attempt_at = $3, attempt_count = attempt_count + 1,
       lease_token = NULL, lease_until = NULL
 WHERE event_id = $1 AND lease_token = $2 AND published_at IS NULL AND dead_at IS NULL`

	// The same reschedule, and the refusal counted apart. Only the permanent one
	// counts here, because it is the permanent one the limit of ten is about.
	refuseOutboxEvent = `
UPDATE outbox_events
   SET next_attempt_at = $3, attempt_count = attempt_count + 1,
       refusal_count = refusal_count + 1,
       lease_token = NULL, lease_until = NULL
 WHERE event_id = $1 AND lease_token = $2 AND published_at IS NULL AND dead_at IS NULL`

	killOutboxEvent = `
UPDATE outbox_events
   SET dead_at = $3, attempt_count = attempt_count + 1,
       refusal_count = refusal_count + 1,
       lease_token = NULL, lease_until = NULL
 WHERE event_id = $1 AND lease_token = $2 AND published_at IS NULL AND dead_at IS NULL`
)

// The backlog, measured in one statement over the predicate of the partial
// index: the pending rows, and the clock of the database beside the entry of
// the oldest, so the age is measured by the clock the leases are measured by.
const selectOutboxBacklog = `
SELECT count(*), now(), min(created_at)
  FROM outbox_events
 WHERE published_at IS NULL AND dead_at IS NULL`

// OutboxQueue works the publication queue from the pool, in short transactions
// of its own. The zero value is not used: NewOutboxQueue is the only
// constructor.
type OutboxQueue struct {
	source *Pool
}

func NewOutboxQueue(source *Pool) *OutboxQueue {
	return &OutboxQueue{source: source}
}

func (q *OutboxQueue) Due(ctx context.Context, limit int) ([]storage.OutboxCandidate, error) {
	pool, err := q.source.Querier()
	if err != nil {
		return nil, wrap("acquire pool", err)
	}
	rows, err := pool.Query(ctx, selectDueOutboxEvents, limit)
	if err != nil {
		return nil, wrap("scan the outbox queue", err)
	}
	defer rows.Close()
	return outboxCandidates(rows)
}

// NextOf answers the next row of the wallet that can be claimed now, and
// reports whether there is one.
func (q *OutboxQueue) NextOf(ctx context.Context, wallet identity.WalletID) (storage.OutboxCandidate, bool, error) {
	pool, err := q.source.Querier()
	if err != nil {
		return storage.OutboxCandidate{}, false, wrap("acquire pool", err)
	}
	rows, err := pool.Query(ctx, selectNextOutboxEvent, wallet.String())
	if err != nil {
		return storage.OutboxCandidate{}, false, wrap("read the next outbox event", err)
	}
	defer rows.Close()
	found, err := outboxCandidates(rows)
	if err != nil || len(found) == 0 {
		return storage.OutboxCandidate{}, false, err
	}
	return found[0], true, nil
}

func outboxCandidates(rows pgx.Rows) ([]storage.OutboxCandidate, error) {
	var due []storage.OutboxCandidate
	for rows.Next() {
		var eventID, walletID string
		if err := rows.Scan(&eventID, &walletID); err != nil {
			return nil, wrap("read an outbox candidate", err)
		}
		candidate, err := outboxCandidateOf(eventID, walletID)
		if err != nil {
			return nil, wrap("read an outbox candidate", err)
		}
		due = append(due, candidate)
	}
	return due, wrap("scan the outbox queue", rows.Err())
}

func outboxCandidateOf(eventID, walletID string) (storage.OutboxCandidate, error) {
	event, err := identity.ParseEventID(eventID)
	if err != nil {
		return storage.OutboxCandidate{}, err
	}
	wallet, err := identity.ParseWalletID(walletID)
	if err != nil {
		return storage.OutboxCandidate{}, err
	}
	return storage.OutboxCandidate{EventID: event, WalletID: wallet}, nil
}

func (q *OutboxQueue) Claim(ctx context.Context, id identity.EventID, lease time.Duration) (storage.OutboxRow, error) {
	pool, err := q.source.Querier()
	if err != nil {
		return storage.OutboxRow{}, wrap("acquire pool", err)
	}
	var row claimedOutboxRow
	err = pool.QueryRow(ctx, claimOutboxEvent, id.String(), interval(lease)).Scan(
		&row.eventID, &row.walletID, &row.eventType, &row.payload,
		&row.traceID, &row.spanID, &row.attempts, &row.refusals, &row.leaseToken,
	)
	if err != nil {
		return storage.OutboxRow{}, missingOutboxEvent("claim outbox event", err)
	}
	return row.row()
}

// interval renders the lease for the database, which is what measures it. The
// duration crosses as text because an interval has no pgx type of its own.
func interval(lease time.Duration) string {
	return lease.String()
}

// claimedOutboxRow is the row exactly as the claim returned it, before the
// identifiers are parsed back into the types of the domain.
type claimedOutboxRow struct {
	eventID    string
	walletID   string
	eventType  string
	payload    []byte
	traceID    string
	spanID     string
	attempts   int64
	refusals   int64
	leaseToken string
}

func (r claimedOutboxRow) row() (storage.OutboxRow, error) {
	candidate, err := outboxCandidateOf(r.eventID, r.walletID)
	if err != nil {
		return storage.OutboxRow{}, wrap("read the claimed outbox row", err)
	}
	return storage.OutboxRow{
		EventID:    candidate.EventID,
		WalletID:   candidate.WalletID,
		EventType:  r.eventType,
		Payload:    r.payload,
		TraceID:    r.traceID,
		SpanID:     r.spanID,
		Attempts:   r.attempts,
		Refusals:   r.refusals,
		LeaseToken: r.leaseToken,
	}, nil
}

// Backlog answers how many rows are pending and how old the oldest one is, by
// the clock of the database, and zero for both when nothing is pending.
func (q *OutboxQueue) Backlog(ctx context.Context) (storage.Backlog, error) {
	pool, err := q.source.Querier()
	if err != nil {
		return storage.Backlog{}, wrap("acquire pool", err)
	}
	var pending int64
	var now time.Time
	var oldest *time.Time
	if err := pool.QueryRow(ctx, selectOutboxBacklog).Scan(&pending, &now, &oldest); err != nil {
		return storage.Backlog{}, wrap("measure the outbox backlog", err)
	}
	return storage.Backlog{Pending: pending, OldestAge: ageOf(now, oldest)}, nil
}

func (q *OutboxQueue) Confirm(ctx context.Context, id identity.EventID, token string, at time.Time) error {
	return q.guarded(ctx, "confirm outbox event", confirmOutboxEvent, id, token, at)
}

func (q *OutboxQueue) Reschedule(ctx context.Context, id identity.EventID, token string, next time.Time) error {
	return q.guarded(ctx, "reschedule outbox event", rescheduleOutboxEvent, id, token, next)
}

func (q *OutboxQueue) Refuse(ctx context.Context, id identity.EventID, token string, next time.Time) error {
	return q.guarded(ctx, "refuse outbox event", refuseOutboxEvent, id, token, next)
}

func (q *OutboxQueue) Kill(ctx context.Context, id identity.EventID, token string, at time.Time) error {
	return q.guarded(ctx, "kill outbox event", killOutboxEvent, id, token, at)
}

// guarded runs one write of the turn under the token of the claim and answers
// ErrLeaseLost when no row matched: the row moved on to another replica, and
// nothing this one had in flight applies to it any more.
func (q *OutboxQueue) guarded(ctx context.Context, op, statement string, id identity.EventID, token string, at time.Time) error {
	pool, err := q.source.Querier()
	if err != nil {
		return wrap("acquire pool", err)
	}
	tag, err := pool.Exec(ctx, statement, id.String(), token, at)
	if err != nil {
		return wrap(op, err)
	}
	return lostLease(op, tag)
}

func lostLease(op string, tag pgconn.CommandTag) error {
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s: %w", op, storage.ErrLeaseLost)
	}
	return nil
}
