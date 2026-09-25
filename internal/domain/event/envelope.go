// Package event is the outbound vocabulary of the settlement: the four events
// it publishes and the envelope every one of them travels in.
//
// Nothing here knows the outbox, the broker or SQL. A use case asks this package
// what one outcome emits and writes back whatever it answers.
package event

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

var ErrIncompleteEnvelope = errors.New("event: envelope has no identity, aggregate, instant or data")

// envelopeVersion is the shape of the envelope, fixed by the constructor. A
// consumer reads it to know which fields to expect, so it is the one number no
// caller is allowed to choose.
const envelopeVersion = 1

// Type names one of the four events. The catalog is closed: nothing outside this
// package can add a fifth, because Data cannot be implemented from outside.
type Type string

const (
	TypeProcessed        Type = "WagerTransactionProcessed"
	TypeRejected         Type = "WagerTransactionRejected"
	TypeBalanceChanged   Type = "WalletBalanceChanged"
	TypePendingReference Type = "WagerTransactionPendingReference"
)

// Data is what one event carries beyond the envelope. The four payloads of this
// package are its only implementations, and the unexported method is what keeps
// the catalog closed.
//
// It also answers the type of the event it belongs to, so the constructor reads
// the type off the payload instead of taking it from the caller.
type Data interface {
	eventType() Type
}

// Spec is what every event carries whatever its type: the identity minted for
// it, the wallet that orders its publication, and the instant it happened.
type Spec struct {
	ID          identity.EventID
	AggregateID identity.WalletID
	At          time.Time
}

// Origin is where one event came from: the correlation of the request that
// produced it and, when there is one, the message that caused it.
//
// It is apart from the envelope because neither value belongs to the
// settlement: both are decided at the border and read again at the instant the
// row is written. The zero value carries neither.
type Origin struct {
	CorrelationID string
	CausationID   string
}

// Envelope is one event ready to be recorded. The zero value is invalid: New is
// the only constructor, and it is the one that fixes the type and the version.
type Envelope struct {
	id          identity.EventID
	eventType   Type
	version     int
	aggregateID identity.WalletID
	occurredAt  time.Time
	data        Data
}

// New builds the envelope of one event, taking its type from the payload and
// fixing the version. The instant is kept in UTC, which is the only form the
// envelope crosses in.
func New(spec Spec, data Data) (Envelope, error) {
	if spec.ID.IsZero() || spec.AggregateID.IsZero() || spec.At.IsZero() || data == nil {
		return Envelope{}, ErrIncompleteEnvelope
	}
	return Envelope{
		id:          spec.ID,
		eventType:   data.eventType(),
		version:     envelopeVersion,
		aggregateID: spec.AggregateID,
		occurredAt:  spec.At.UTC(),
		data:        data,
	}, nil
}

func (e Envelope) ID() identity.EventID {
	return e.id
}

func (e Envelope) Type() Type {
	return e.eventType
}

func (e Envelope) Version() int {
	return e.version
}

// AggregateID is the wallet, which is what orders the publication and what the
// topic takes as the group of the message.
func (e Envelope) AggregateID() identity.WalletID {
	return e.aggregateID
}

func (e Envelope) OccurredAt() time.Time {
	return e.occurredAt
}

// wire is the published form. It is a type of its own so that the field names
// and the omission of an absent cause live in one place instead of in four
// payload structs.
type wire struct {
	EventID       string    `json:"eventId"`
	EventType     Type      `json:"eventType"`
	Version       int       `json:"version"`
	AggregateID   string    `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	Data          Data      `json:"data"`
}

// Marshal writes the event as it is recorded and published, with the origin of
// the operation filled in. An origin naming no cause omits causationId rather
// than sending it empty.
//
// The bytes are what the outbox row keeps and what the broker receives, so a
// republication that marshals the same envelope and the same origin again sends
// the very same payload.
func (e Envelope) Marshal(origin Origin) ([]byte, error) {
	if e.data == nil {
		return nil, ErrIncompleteEnvelope
	}
	return json.Marshal(wire{
		EventID:       e.id.String(),
		EventType:     e.eventType,
		Version:       e.version,
		AggregateID:   e.aggregateID.String(),
		CorrelationID: origin.CorrelationID,
		CausationID:   origin.CausationID,
		OccurredAt:    e.occurredAt,
		Data:          e.data,
	})
}
