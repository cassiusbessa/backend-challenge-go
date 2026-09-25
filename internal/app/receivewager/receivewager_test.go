package receivewager

import (
	"context"
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// errRefusedSender stands for the refusal the map answers. This use case does not
// name which of the two it is: it hands the refusal on, and the border reads it.
var errRefusedSender = errors.New("authz: sender is not in the map")

func TestReceive_answersWhatTheOperationSettledTo(t *testing.T) {
	t.Parallel()
	submitter := &recordingSubmitter{status: wager.Processed}
	result, err := New(openSenders{}, submitter).Receive(context.Background(), delivery(t))
	if err != nil {
		t.Fatalf("Receive = %v, want nil", err)
	}
	if result.Status != wager.Processed {
		t.Fatalf("status = %s, want PROCESSED", result.Status)
	}
	if submitter.calls != 1 {
		t.Fatalf("submissions = %d, want 1", submitter.calls)
	}
}

// The message travels with the command, so the row of the inbox is written in the
// commit that moves the balance and not in one of its own.
func TestReceive_handsTheMessageToTheSubmissionItCaused(t *testing.T) {
	t.Parallel()
	submitter := &recordingSubmitter{status: wager.Processed}
	if _, err := New(openSenders{}, submitter).Receive(context.Background(), delivery(t)); err != nil {
		t.Fatalf("Receive handing the message over = %v, want nil", err)
	}
	if submitter.caused.MessageID != "message-1" {
		t.Fatalf("message = %q, want message-1", submitter.caused.MessageID)
	}
	if submitter.caused.BodyHash != "hash-of-the-body" {
		t.Fatalf("hash = %q, want hash-of-the-body", submitter.caused.BodyHash)
	}
	if submitter.caused.Consumer != Consumer {
		t.Fatalf("consumer = %q, want %q", submitter.caused.Consumer, Consumer)
	}
}

// The sender is checked before the submission, so a message this process may not
// take never reaches a wallet and leaves no row of any kind.
func TestReceive_refusesTheSenderBeforeReachingTheSubmission(t *testing.T) {
	t.Parallel()
	submitter := &recordingSubmitter{status: wager.Processed}
	_, err := New(closedSenders{}, submitter).Receive(context.Background(), delivery(t))
	if !errors.Is(err, errRefusedSender) {
		t.Fatalf("Receive of a refused sender = %v, want %v", err, errRefusedSender)
	}
	if submitter.calls != 0 {
		t.Fatalf("submissions = %d, want 0: the refusal comes before the wallet", submitter.calls)
	}
}

// What the body declares as its provider is what the map is asked about. The
// declaration authorizes nothing on its own.
func TestReceive_asksTheMapAboutTheProviderTheBodyDeclares(t *testing.T) {
	t.Parallel()
	senders := &recordingSenders{}
	if _, err := New(senders, &recordingSubmitter{status: wager.Processed}).Receive(context.Background(), delivery(t)); err != nil {
		t.Fatalf("Receive over a recording map = %v, want nil", err)
	}
	if senders.sender != "000000000000" {
		t.Fatalf("sender asked about = %q, want the observed 000000000000", senders.sender)
	}
	if senders.provider.String() != "provider-a" {
		t.Fatalf("provider asked about = %s, want the provider-a of the body", senders.provider)
	}
}

func TestReceive_answersTheRefusalOfTheSubmission(t *testing.T) {
	t.Parallel()
	refused := &recordingSubmitter{refuse: submitwager.ErrMessageBodyDiffers}
	_, err := New(openSenders{}, refused).Receive(context.Background(), delivery(t))
	if !errors.Is(err, submitwager.ErrMessageBodyDiffers) {
		t.Fatalf("Receive = %v, want %v", err, submitwager.ErrMessageBodyDiffers)
	}
}

func delivery(t *testing.T) Delivery {
	t.Helper()
	provider, err := identity.ParseProviderID("provider-a")
	if err != nil {
		t.Fatalf("ParseProviderID = %v, want nil", err)
	}
	return Delivery{
		MessageID: "message-1",
		Sender:    "000000000000",
		BodyHash:  "hash-of-the-body",
		Command:   submitwager.Command{ProviderID: provider},
	}
}

// openSenders allows every sender, which is what a case about the submission
// needs.
type openSenders struct{}

func (openSenders) Allow(string, identity.ProviderID) error { return nil }

// closedSenders refuses every sender.
type closedSenders struct{}

func (closedSenders) Allow(string, identity.ProviderID) error { return errRefusedSender }

// recordingSenders keeps what it was asked about, which is what pins the observed
// sender against the provider of the body.
type recordingSenders struct {
	sender   string
	provider identity.ProviderID
}

func (r *recordingSenders) Allow(sender string, provider identity.ProviderID) error {
	r.sender, r.provider = sender, provider
	return nil
}

// recordingSubmitter stands for the use case that settles the operation, and keeps
// the message it was handed.
type recordingSubmitter struct {
	status wager.Status
	refuse error
	caused submitwager.Caused
	calls  int
}

func (r *recordingSubmitter) SubmitCaused(_ context.Context, _ submitwager.Command, caused submitwager.Caused) (submitwager.Result, error) {
	r.calls++
	r.caused = caused
	if r.refuse != nil {
		return submitwager.Result{}, r.refuse
	}
	return submitwager.Result{Status: r.status}, nil
}
