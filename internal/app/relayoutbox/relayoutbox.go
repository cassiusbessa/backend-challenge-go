// Package relayoutbox moves one committed event out to the broker. It is the
// RelayOutbox use case of go-tactical-ddd.
//
// The turn has three times and two short SQL transactions: claim the row under
// a lease, publish outside any transaction, and confirm under the token of that
// claim. The send is a network call, so it must not happen with a transaction
// open: a connection of the pool held for as long as a broker that is out is the
// shortest path to exhausting the pool.
//
// It is handed the identity of a single row and works that one: the ticker, the
// batch and the shutdown belong to the runner in the platform.
package relayoutbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
)

// deadAfter is how many attempts a row survives: the tenth that ends in a
// permanent refusal gives up on it and releases the wallet.
//
// What counts is the attempt column, which rises on every attempt that did not
// publish. A permanent refusal is answered the same way every time, so a row
// that meets one meets it again on the next attempt; a transient failure mixed
// in only brings the end forward, and a row nobody can publish is one nothing
// behind it should wait for.
const deadAfter = 10

// sendShare is the fraction of the lease the send is given. The deadline of the
// send must fit inside the lease, so that a replica is never still publishing a
// row it has already lost, and half of it leaves the confirmation room to land.
const sendShare = 2

// Message is one event on its way to the broker: the bytes as they were
// recorded, the wallet that groups them and the identity it deduplicates by.
type Message struct {
	Body            string
	GroupID         string
	DeduplicationID string
}

// Publisher sends one message and says what a failure of it means for the row.
//
// The two are one port because they are one decision: whoever knows how to talk
// to the broker is who knows how to read its refusals.
type Publisher interface {
	Publish(ctx context.Context, message Message) error

	// Permanent reports whether the same bytes will never complete. Repeated,
	// that is what gives up on a row; anything else comes back on the backoff.
	Permanent(err error) bool
}

// Span opens the span of one send, linked to the trace the row carries, and
// answers the context of the send and the function that closes it.
//
// It is a function and not a tracer so that this package decides the outcome
// without naming a telemetry library: the link, the attributes and the stack
// are the platform's, and are built where the span is.
type Span func(ctx context.Context, traceID, spanID string) (context.Context, func(err error))

// Clock reads the instant a write of the turn is stamped with.
type Clock interface {
	Now() time.Time
}

// Service relays one row. The zero value is not used: New is the only
// constructor.
type Service struct {
	queue     storage.OutboxQueue
	publisher Publisher
	span      Span
	clock     Clock
	log       *slog.Logger
	lease     time.Duration
}

func New(queue storage.OutboxQueue, publisher Publisher, span Span, clock Clock, log *slog.Logger, lease time.Duration) *Service {
	return &Service{queue: queue, publisher: publisher, span: span, clock: clock, log: log, lease: lease}
}

// Relay works the row the candidate names.
//
// A row another replica already holds, one already published and one no longer
// due are not this replica's to publish, and none of the three is a failure: the
// scan chose the candidate, and the claim is what decides.
func (s *Service) Relay(ctx context.Context, candidate storage.OutboxCandidate) error {
	claimed, err := s.queue.Claim(ctx, candidate.EventID, s.lease)
	if errors.Is(err, storage.ErrOutboxEventNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("relay outbox: %w", err)
	}
	if err := s.turn(ctx, claimed); err != nil {
		return fmt.Errorf("relay outbox: %w", err)
	}
	return nil
}

// turn is the send of one claimed row and the write that ends it, inside the
// span of that send.
func (s *Service) turn(ctx context.Context, claimed storage.OutboxRow) error {
	ctx, end := s.span(ctx, claimed.TraceID, claimed.SpanID)
	err := s.deliver(ctx, claimed)
	// A row that merely goes back on the backoff leaves the span ok: nothing
	// about it failed that is not expected to come back. What marks the span is
	// the turn itself failing, which is a write this replica could not make.
	end(err)
	return err
}

func (s *Service) deliver(ctx context.Context, claimed storage.OutboxRow) error {
	sending, cancel := context.WithDeadline(ctx, s.clock.Now().Add(s.lease/sendShare))
	defer cancel()
	refusal := s.publisher.Publish(sending, messageOf(claimed))
	if refusal == nil {
		return s.confirm(ctx, claimed)
	}
	return s.setBack(ctx, claimed, refusal)
}

func messageOf(claimed storage.OutboxRow) Message {
	return Message{
		Body: string(claimed.Payload),
		// The wallet orders the publication, and the event identity survives a
		// republication, so the second send of a row deduplicates against the
		// first instead of reaching the topic twice.
		GroupID:         claimed.WalletID.String(),
		DeduplicationID: claimed.EventID.String(),
	}
}

func (s *Service) confirm(ctx context.Context, claimed storage.OutboxRow) error {
	err := s.queue.Confirm(ctx, claimed.EventID, claimed.LeaseToken, s.clock.Now())
	if err != nil {
		return s.lost(ctx, claimed, "publish", err)
	}
	s.report(ctx, claimed, "published")
	return nil
}

// setBack answers the refusal of the broker: the tenth attempt that ends in a
// permanent one gives up on the row, and everything else comes back on the
// backoff.
func (s *Service) setBack(ctx context.Context, claimed storage.OutboxRow, refusal error) error {
	if s.publisher.Permanent(refusal) && claimed.Attempts+1 >= deadAfter {
		return s.kill(ctx, claimed)
	}
	next := s.clock.Now().Add(Backoff(claimed.Attempts))
	if err := s.queue.Reschedule(ctx, claimed.EventID, claimed.LeaseToken, next); err != nil {
		return s.lost(ctx, claimed, "reschedule", err)
	}
	s.report(ctx, claimed, statusOf(s.publisher.Permanent(refusal)))
	return nil
}

func statusOf(permanent bool) string {
	if permanent {
		return "refused"
	}
	return "retried"
}

func (s *Service) kill(ctx context.Context, claimed storage.OutboxRow) error {
	if err := s.queue.Kill(ctx, claimed.EventID, claimed.LeaseToken, s.clock.Now()); err != nil {
		return s.lost(ctx, claimed, "kill", err)
	}
	s.report(ctx, claimed, "dead")
	return nil
}

// lost answers a write the lease no longer allows: the row was taken over while
// this replica was sending, and what it had in flight simply does not apply to
// it any more. It is not a failure of this turn.
func (s *Service) lost(ctx context.Context, claimed storage.OutboxRow, op string, err error) error {
	if !errors.Is(err, storage.ErrLeaseLost) {
		return fmt.Errorf("%s outbox row: %w", op, err)
	}
	s.report(ctx, claimed, "lost")
	return nil
}

// report is the line of one send. It carries identifiers and the outcome only:
// the payload, the amount and the balance never reach a log line, and the
// handler of the pipeline refuses them even if one tried.
//
// The trace is the one of the commit that wrote the row, which is what ties the
// line back to the operation that produced the event. The span of the send is
// linked to it.
func (s *Service) report(ctx context.Context, claimed storage.OutboxRow, status string) {
	s.log.LogAttrs(ctx, slog.LevelInfo, "outbox event",
		slog.String("eventId", claimed.EventID.String()),
		slog.String("walletId", claimed.WalletID.String()),
		slog.String("kind", claimed.EventType),
		slog.String("status", status),
		slog.String("trace_id", claimed.TraceID),
		slog.String("span_id", claimed.SpanID),
	)
}
