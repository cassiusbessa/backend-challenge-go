// Package receivewager takes one message of the ingress queue and hands the
// operation it carries to the use case that decides it.
//
// What it adds to that use case is the pair the queue needs and HTTP does not:
// the observed sender authorizes the operation, and the message is remembered in
// the commit that settles it. It decides no business of its own.
package receivewager

import (
	"context"
	"fmt"

	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

// Consumer is the name this consumer records in the inbox. The unicity of the
// inbox is the pair of consumer and message, so the name is what keeps two
// consumers of one envelope from hiding each other.
const Consumer = "wager-ingress"

// Senders answers whether an observed sender may send for a provider.
type Senders interface {
	Allow(sender string, provider identity.ProviderID) error
}

// Submitter is the use case that settles the operation, as this one needs it. The
// message travels with the command because the row of the inbox belongs in the
// commit that moves the balance.
type Submitter interface {
	SubmitCaused(ctx context.Context, cmd submitwager.Command, caused submitwager.Caused) (submitwager.Result, error)
}

// Delivery is one decoded message as the consumer hands it over.
//
// Sender is the identity the broker registered on the message, and it is opaque:
// nothing here interprets it, compares part of it or derives anything from it. In
// production it is the identifier of the sending principal and on the local broker
// it is the identifier of the account, and this code is the same for both.
//
// BodyHash is of the body that arrived, which is what tells a redelivery of the
// same bytes from the same identifier re-presented with another content.
type Delivery struct {
	MessageID string
	Sender    string
	BodyHash  string
	Command   submitwager.Command
}

func (d Delivery) caused() submitwager.Caused {
	return submitwager.Caused{Consumer: Consumer, MessageID: d.MessageID, BodyHash: d.BodyHash}
}

// Service coordinates the reception. The zero value is not used: New is the only
// constructor.
type Service struct {
	senders   Senders
	submitter Submitter
}

func New(senders Senders, submitter Submitter) *Service {
	return &Service{senders: senders, submitter: submitter}
}

// Receive authorizes the message by its sender and answers what the operation
// settled to.
//
// The sender is checked before the submission, so a message this process may not
// take never reaches a wallet: the refusal leaves no row of any kind, financial or
// inbox. What the body declares as its provider is checked against what that
// sender may send, and never taken as the authorization itself.
func (s *Service) Receive(ctx context.Context, delivery Delivery) (submitwager.Result, error) {
	if err := s.senders.Allow(delivery.Sender, delivery.Command.ProviderID); err != nil {
		return submitwager.Result{}, fmt.Errorf("receive wager: %w", err)
	}
	result, err := s.submitter.SubmitCaused(ctx, delivery.Command, delivery.caused())
	if err != nil {
		// The result travels with the refusal: a rule that refused wrote a row of
		// its own, and the border names it. Only the sender refusal above answers
		// nothing, because it leaves no row of any kind.
		return result, fmt.Errorf("receive wager: %w", err)
	}
	return result, nil
}
