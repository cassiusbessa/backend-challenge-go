package storage

import (
	"context"
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

var (
	// ErrOutboxEventNotFound is the absence of the row the relay named: it was
	// published, it was claimed by another replica, or it is no longer due. None
	// of the three is this replica's to publish now, and none is a failure.
	ErrOutboxEventNotFound = NotFoundError{Entity: "outbox event"}

	// ErrLeaseLost is a write over a row whose lease token is no longer the one
	// the caller holds: the lease expired and another replica claimed the row.
	//
	// It is not a failure of infrastructure and carries no failureCode. The row
	// belongs to whoever holds it now, and what this replica had in flight is
	// simply dropped.
	ErrLeaseLost = errors.New("storage: outbox row is held under another lease")
)

// OutboxCandidate is one row the scan of the publication queue chose.
//
// The wallet comes from the scan because the relay logs and groups by it, and
// the wallet of an event never changes, so reading it outside any lock answers
// the same identity the claimed row carries.
type OutboxCandidate struct {
	EventID  identity.EventID
	WalletID identity.WalletID
}

// OutboxRow is one event as the relay claims it: the bytes to publish, the
// trace to link the send to, and the two counts already on it.
//
// Attempts is every attempt that did not publish, and it is what moves the
// backoff. Refusals is only the permanent ones, and it is what gives up on the
// row: a broker that was out for a while must not bring the end of a row
// forward.
//
// LeaseToken is the token this claim wrote. Every write that follows carries it
// back, and a row whose token has moved on refuses them all.
type OutboxRow struct {
	EventID    identity.EventID
	WalletID   identity.WalletID
	EventType  string
	Payload    []byte
	TraceID    string
	SpanID     string
	Attempts   int64
	Refusals   int64
	LeaseToken string
}

// OutboxQueue is the publication queue as the relay works it.
//
// None of it belongs to a business transaction: the relay takes each of these
// in a short transaction of its own, because the send to the broker sits
// between the claim and the confirmation and must not hold a connection.
type OutboxQueue interface {
	// Due answers the candidates to publish: per wallet, the oldest row that is
	// neither published nor dead, whose next attempt has come and which no live
	// lease holds — and only when no older row of that wallet is still pending.
	//
	// It takes no lock and opens no transaction: it only chooses candidates. A
	// dead row stops hiding the rows behind it, which is what lets the events of
	// a wallet go on after one of them is given up on.
	Due(ctx context.Context, limit int) ([]OutboxCandidate, error)

	// Claim writes a new lease token and a deadline measured by the clock of the
	// database, and answers the row re-read under it.
	//
	// A row another replica already holds is skipped rather than waited on, and
	// answers ErrOutboxEventNotFound — the same as a row that has since been
	// published. The deadline is the database clock and not the process one:
	// replicas whose clocks differ would otherwise disagree on whether a lease
	// expired, and publish the same row twice.
	Claim(ctx context.Context, id identity.EventID, lease time.Duration) (OutboxRow, error)

	// Confirm marks the row published, and only if the token is still the one
	// the claim wrote. A replica whose lease was taken over answers ErrLeaseLost
	// and marks nothing.
	Confirm(ctx context.Context, id identity.EventID, token string, at time.Time) error

	// Reschedule releases the lease and moves the next attempt, counting the
	// attempt that did not publish. It marks the row neither published nor dead.
	//
	// It is the answer to a transitory failure: the broker may well take the
	// same bytes on the next turn.
	Reschedule(ctx context.Context, id identity.EventID, token string, next time.Time) error

	// Refuse is the same reschedule for a refusal the broker will not take back,
	// and it counts that refusal apart. Ten of them give up on the row; the
	// attempts in between only move the backoff.
	Refuse(ctx context.Context, id identity.EventID, token string, next time.Time) error

	// Kill marks the row dead and releases the wallet, so the events behind it
	// go on. The row stays in the database, with the same event identity and the
	// same payload, and is never published again without intervention.
	Kill(ctx context.Context, id identity.EventID, token string, at time.Time) error
}
