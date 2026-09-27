package wagerqueue

import (
	"errors"

	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/authz"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
)

// Answer is what the consumer does with one message. The zero value is not an
// answer: answerOf is the only place that decides it.
type Answer uint8

const (
	// Remove takes the message out of the queue. It is the answer to every
	// outcome that was committed — processed, rejected by a rule, or a wait
	// recorded — and to a redelivery that reapplied nothing: in all four the
	// decision is durable, and delivering the same bytes again would not change
	// it.
	Remove Answer = iota + 1

	// Return leaves the message in the queue for another attempt, invisible for
	// the backoff. It is the answer to a transient failure, which rolled the
	// commit back whole.
	Return

	// Abandon copies the message to the dead-letter queue and takes it out of the
	// ingress one. It is the answer to what no repetition of the same bytes can
	// settle: a body this border did not take, a sender the map does not allow,
	// and an identifier already recorded with another body.
	Abandon
)

// answerOf reads the outcome of the use case as one of the four answers.
//
// Nothing is decided here: the classes are the ones the use case and the border
// already answer, and what this adds is which of them the broker is told. A
// business rejection is the case worth naming — it is a completed outcome with a
// row of its own, so it removes the message rather than returning it.
func answerOf(err error) (Answer, string) {
	if err == nil {
		return Remove, ""
	}
	if reason, abandoned := abandonedFor(err); abandoned {
		return Abandon, reason
	}
	var rejection wager.Rejection
	if errors.As(err, &rejection) {
		return Remove, ""
	}
	return Return, ""
}

// abandonedFor names the reason a message leaves for the dead-letter queue, and
// reports whether it does.
//
// Everything outside this set returns the message, including a defect of ours: a
// failure nobody classified is answered by the attempt that follows, and the
// delivery count is what ends a message no attempt can settle. Giving up is what
// cannot be undone, so the unclassified takes the side that can be.
func abandonedFor(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrInvalidMessage):
		return metrics.AbandonInvalidBody, true
	case errors.Is(err, authz.ErrUnmappedSender), errors.Is(err, authz.ErrProviderNotAllowed):
		return metrics.AbandonRefusedSender, true
	case errors.Is(err, submitwager.ErrMessageBodyDiffers):
		return metrics.AbandonBodyDiffers, true
	}
	return "", false
}
