package wagerqueue

import (
	"errors"
	"fmt"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/authz"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
)

func TestAnswerOf_readsEachOutcomeAsTheAnswerTheBrokerIsTold(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		answer Answer
		reason string
	}{
		{
			// The committed outcome and the redelivery that reapplied nothing leave
			// by the same path: both answer no failure at all, because both are a
			// decision already durable.
			name:   "a committed outcome removes the message",
			err:    nil,
			answer: Remove,
		},
		{
			// A rule refusing the operation wrote the row with its token. The
			// decision is durable and redelivering the same bytes would reach the
			// same rule, so the message leaves the queue.
			name:   "a business rejection removes the message",
			err:    fmt.Errorf("submit wager: %w", wager.NewRejection(wager.InsufficientFunds, nil)),
			answer: Remove,
		},
		{
			name:   "an invalid body is abandoned",
			err:    invalid("money"),
			answer: Abandon,
			reason: metrics.AbandonInvalidBody,
		},
		{
			name:   "an unmapped sender is abandoned",
			err:    fmt.Errorf("receive wager: %w", authz.ErrUnmappedSender),
			answer: Abandon,
			reason: metrics.AbandonRefusedSender,
		},
		{
			name:   "a body claiming another provider is abandoned",
			err:    fmt.Errorf("receive wager: %w", authz.ErrProviderNotAllowed),
			answer: Abandon,
			reason: metrics.AbandonRefusedSender,
		},
		{
			name:   "a recorded identifier with another body is abandoned",
			err:    fmt.Errorf("receive wager: %w", submitwager.ErrMessageBodyDiffers),
			answer: Abandon,
			reason: metrics.AbandonBodyDiffers,
		},
		{
			// The commit rolled back whole, so there is nothing recorded and the
			// same bytes may well go through on the next attempt.
			name:   "an infrastructure failure returns the message",
			err:    fault.Wrap("acquire connection", errors.New("connection refused")),
			answer: Return,
		},
		{
			name:   "a condition that resolves itself returns the message",
			err:    submitwager.ErrRaceUnresolved,
			answer: Return,
		},
		{
			// A defect of ours is not one of the abandonment reasons: the attempt
			// that follows answers it, and the delivery count is what ends a message
			// no attempt can settle.
			name:   "a defect returns the message",
			err:    submitwager.ErrKindNotAccepted,
			answer: Return,
		},
		{
			// The unicity of the inbox reaching the border unclassified would be a
			// defect of the use case, and it takes the side that can be undone.
			name:   "a bare refusal of the inbox returns the message",
			err:    storage.ErrMessageRecorded,
			answer: Return,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answer, reason := answerOf(tc.err)
			if answer != tc.answer {
				t.Fatalf("answer = %d, want %d", answer, tc.answer)
			}
			if reason != tc.reason {
				t.Fatalf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}
}
