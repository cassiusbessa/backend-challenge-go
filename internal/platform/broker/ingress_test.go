package broker

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/junglegaming/backend-challenge-go/internal/platform/wagerqueue"
)

// The two queues of the ingress, as the configuration names them.
const (
	ingressURL = "http://localhost:4566/000000000000/wager-transactions.fifo"
	deadURL    = "http://localhost:4566/000000000000/wager-transactions-dlq.fifo"
)

func ingress(client messenger) *Ingress {
	return &Ingress{url: ingressURL, deadURL: deadURL, client: client}
}

// The sender identity and the delivery count are asked for together with the body.
// Neither is in the payload, and the ingress cannot work without either: the first
// authorizes the operation and the second ends a message no attempt can settle.
func TestReceive_asksForTheSenderAndTheDeliveryCountBesideTheBody(t *testing.T) {
	t.Parallel()
	fake := &fakeMessenger{}
	if _, err := ingress(fake).Receive(context.Background(), 20*time.Second, 30*time.Second); err != nil {
		t.Fatalf("Receive = %v, want nil", err)
	}
	asked := fake.received.MessageSystemAttributeNames
	for _, want := range []types.MessageSystemAttributeName{
		types.MessageSystemAttributeNameSenderId,
		types.MessageSystemAttributeNameApproximateReceiveCount,
	} {
		if !slices.Contains(asked, want) {
			t.Fatalf("attributes asked for = %v, want %s among them", asked, want)
		}
	}
	if fake.received.WaitTimeSeconds != 20 {
		t.Fatalf("wait = %d, want the 20 seconds of the long poll", fake.received.WaitTimeSeconds)
	}
	if fake.received.VisibilityTimeout != 30 {
		t.Fatalf("visibility = %d, want 30 seconds", fake.received.VisibilityTimeout)
	}
}

func TestReceive_readsTheSenderTheCountAndThePropagationOfEachMessage(t *testing.T) {
	t.Parallel()
	fake := &fakeMessenger{messages: []types.Message{{
		Body:          aws.String(`{"messageId":"message-1"}`),
		ReceiptHandle: aws.String("receipt-1"),
		Attributes: map[string]string{
			"SenderId":                "000000000000",
			"ApproximateReceiveCount": "3",
			"MessageGroupId":          "wallet-1",
			"MessageDeduplicationId":  "message-1",
		},
		MessageAttributes: map[string]types.MessageAttributeValue{
			traceAttribute: {StringValue: aws.String("00-trace-span-01")},
		},
	}}}
	got, err := ingress(fake).Receive(context.Background(), time.Second, time.Second)
	if err != nil {
		t.Fatalf("Receive of one message = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(got))
	}
	assertDelivery(t, got[0])
}

func assertDelivery(t *testing.T, delivery wagerqueue.Delivery) {
	t.Helper()
	if delivery.Sender != "000000000000" {
		t.Fatalf("sender = %q, want 000000000000", delivery.Sender)
	}
	if delivery.Deliveries != 3 {
		t.Fatalf("deliveries = %d, want 3", delivery.Deliveries)
	}
	if delivery.Group != "wallet-1" || delivery.Deduplication != "message-1" {
		t.Fatalf("group and deduplication = %q and %q, want wallet-1 and message-1", delivery.Group, delivery.Deduplication)
	}
	if delivery.Trace[traceAttribute] != "00-trace-span-01" {
		t.Fatalf("propagation = %q, want 00-trace-span-01", delivery.Trace[traceAttribute])
	}
}

// A message that carried no propagation gets no carrier at all, and the consumer
// opens a trace of its own for it.
func TestReceive_answersNoCarrierForAMessageWithNoPropagation(t *testing.T) {
	t.Parallel()
	fake := &fakeMessenger{messages: []types.Message{{Body: aws.String("{}"), Attributes: map[string]string{}}}}
	got, err := ingress(fake).Receive(context.Background(), time.Second, time.Second)
	if err != nil {
		t.Fatalf("Receive of a message with no propagation = %v, want nil", err)
	}
	if len(got[0].Trace) != 0 {
		t.Fatalf("propagation = %v, want none", got[0].Trace)
	}
	// A count the broker did not register reads as the first delivery: reading it
	// as the highest would abandon a message on arrival.
	if got[0].Deliveries != 1 {
		t.Fatalf("deliveries of a count the broker did not register = %d, want 1", got[0].Deliveries)
	}
}

// The copy lands on the dead-letter queue, under the group and the deduplication it
// arrived with: the order of a wallet is the same on both queues, and a copy taken
// twice lands there once.
func TestDeadLetter_copiesTheMessageUnderItsGroupAndDeduplication(t *testing.T) {
	t.Parallel()
	fake := &fakeMessenger{}
	delivery := wagerqueue.Delivery{Body: []byte("{}"), Group: "wallet-1", Deduplication: "message-1"}
	if err := ingress(fake).DeadLetter(context.Background(), delivery); err != nil {
		t.Fatalf("DeadLetter = %v, want nil", err)
	}
	if aws.ToString(fake.sent.QueueUrl) != deadURL {
		t.Fatalf("queue = %q, want the dead-letter one %q", aws.ToString(fake.sent.QueueUrl), deadURL)
	}
	if aws.ToString(fake.sent.MessageGroupId) != "wallet-1" {
		t.Fatalf("group = %q, want wallet-1", aws.ToString(fake.sent.MessageGroupId))
	}
	if aws.ToString(fake.sent.MessageDeduplicationId) != "message-1" {
		t.Fatalf("deduplication = %q, want message-1", aws.ToString(fake.sent.MessageDeduplicationId))
	}
}

// A release of zero hands the message back at once, which is what a shutdown that
// could not finish it answers.
func TestRelease_handsTheMessageBackAtOnceWithAWindowOfZero(t *testing.T) {
	t.Parallel()
	fake := &fakeMessenger{}
	if err := ingress(fake).Release(context.Background(), "receipt-1", 0); err != nil {
		t.Fatalf("Release = %v, want nil", err)
	}
	if fake.released.VisibilityTimeout != 0 {
		t.Fatalf("visibility = %d, want 0", fake.released.VisibilityTimeout)
	}
	if aws.ToString(fake.released.QueueUrl) != ingressURL {
		t.Fatalf("queue of the release = %q, want the ingress one", aws.ToString(fake.released.QueueUrl))
	}
}

func TestDeadLetterDepth_answersWhatThatQueueIsHolding(t *testing.T) {
	t.Parallel()
	fake := &fakeMessenger{attributes: map[string]string{"ApproximateNumberOfMessages": "4"}}
	waiting, err := ingress(fake).DeadLetterDepth(context.Background())
	if err != nil {
		t.Fatalf("DeadLetterDepth = %v, want nil", err)
	}
	if waiting != 4 {
		t.Fatalf("depth = %d, want 4", waiting)
	}
	if aws.ToString(fake.asked.QueueUrl) != deadURL {
		t.Fatalf("queue = %q, want the dead-letter one", aws.ToString(fake.asked.QueueUrl))
	}
}

// The two queues are asked apart, each by its own address: the ingress one is
// what the consumer takes from, and the dead-letter one is beside it.
func TestDepth_answersWhatTheIngressQueueIsHolding(t *testing.T) {
	t.Parallel()
	fake := &fakeMessenger{attributes: map[string]string{"ApproximateNumberOfMessages": "9"}}
	waiting, err := ingress(fake).Depth(context.Background())
	if err != nil {
		t.Fatalf("Depth = %v, want nil", err)
	}
	if waiting != 9 {
		t.Fatalf("depth of the ingress queue = %d, want 9", waiting)
	}
	if aws.ToString(fake.asked.QueueUrl) != ingressURL {
		t.Fatalf("queue = %q, want the ingress one", aws.ToString(fake.asked.QueueUrl))
	}
}

func TestDepth_answersTheFailureOfTheBrokerNamingTheIngressQueue(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection refused")
	_, err := ingress(&fakeMessenger{refuse: broken}).Depth(context.Background())
	if !errors.Is(err, broken) {
		t.Fatalf("Depth over a broker that is out = %v, want %v", err, broken)
	}
	if !strings.Contains(err.Error(), "ingress queue") {
		t.Fatalf("failure of the ingress depth = %v, want the ingress queue named in the chain", err)
	}
}

func TestDeadLetterDepth_answersTheFailureOfTheBroker(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection refused")
	fake := &fakeMessenger{refuse: broken}
	if _, err := ingress(fake).DeadLetterDepth(context.Background()); !errors.Is(err, broken) {
		t.Fatalf("DeadLetterDepth over a broker that is out = %v, want %v", err, broken)
	}
}

// Every call back to the broker answers its failure rather than swallowing it:
// the consumer reads it to decide whether the message was answered for.
func TestIngress_answersTheFailureOfEachCallBackToTheBroker(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection refused")
	cases := []struct {
		name string
		call func(*Ingress) error
	}{
		{name: "a delete that failed", call: func(i *Ingress) error {
			return i.Delete(context.Background(), "receipt-1")
		}},
		{name: "a release that failed", call: func(i *Ingress) error {
			return i.Release(context.Background(), "receipt-1", time.Second)
		}},
		{name: "a copy that failed", call: func(i *Ingress) error {
			return i.DeadLetter(context.Background(), wagerqueue.Delivery{Body: []byte("{}")})
		}},
		{name: "a fetch that failed", call: func(i *Ingress) error {
			_, err := i.Receive(context.Background(), time.Second, time.Second)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is answered", func(t *testing.T) {
			if err := tc.call(ingress(&fakeMessenger{refuse: broken})); !errors.Is(err, broken) {
				t.Fatalf("call = %v, want %v", err, broken)
			}
		})
	}
}

// The delivery count only ever moves the give-up forward, so a count the broker did
// not register reads as the first delivery rather than as the highest.
func TestCount_readsTheDeliveryTheBrokerRegistered(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want int64
	}{
		{raw: "1", want: 1},
		{raw: "5", want: 5},
		{raw: "0", want: 1},
		{raw: "-2", want: 1},
		{raw: "", want: 1},
		{raw: "many", want: 1},
	}
	for _, tc := range cases {
		t.Run("a count of "+tc.raw+" reads as "+strconv.FormatInt(tc.want, 10), func(t *testing.T) {
			if got := count(tc.raw); got != tc.want {
				t.Fatalf("count(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

// The window is bounded by what the API accepts and never by what the type holds: a
// duration past twelve hours would otherwise wrap into a negative one.
func TestSeconds_boundsTheWindowToWhatTheAPITakes(t *testing.T) {
	t.Parallel()
	if got := seconds(20 * time.Second); got != 20 {
		t.Fatalf("seconds of 20s = %d, want 20", got)
	}
	if got := seconds(500 * time.Millisecond); got != 0 {
		t.Fatalf("seconds of half a second = %d, want 0", got)
	}
	if got := seconds(-time.Second); got != 0 {
		t.Fatalf("seconds of a negative window = %d, want 0", got)
	}
	if got := seconds(30 * time.Hour); got != maxWindowSeconds {
		t.Fatalf("seconds far past the ceiling = %d, want %d", got, maxWindowSeconds)
	}
}

func TestDelete_namesTheIngressQueueAndTheReceipt(t *testing.T) {
	t.Parallel()
	fake := &fakeMessenger{}
	if err := ingress(fake).Delete(context.Background(), "receipt-1"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
	if aws.ToString(fake.deleted.ReceiptHandle) != "receipt-1" {
		t.Fatalf("receipt = %q, want receipt-1", aws.ToString(fake.deleted.ReceiptHandle))
	}
	if aws.ToString(fake.deleted.QueueUrl) != ingressURL {
		t.Fatalf("queue = %q, want the ingress one", aws.ToString(fake.deleted.QueueUrl))
	}
}

// fakeMessenger keeps what reached the broker and answers what a case set up.
type fakeMessenger struct {
	messages   []types.Message
	attributes map[string]string
	refuse     error

	received *sqs.ReceiveMessageInput
	deleted  *sqs.DeleteMessageInput
	released *sqs.ChangeMessageVisibilityInput
	sent     *sqs.SendMessageInput
	asked    *sqs.GetQueueAttributesInput
}

func (f *fakeMessenger) ReceiveMessage(_ context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.received = in
	if f.refuse != nil {
		return nil, f.refuse
	}
	return &sqs.ReceiveMessageOutput{Messages: f.messages}, nil
}

func (f *fakeMessenger) DeleteMessage(_ context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	f.deleted = in
	return &sqs.DeleteMessageOutput{}, f.refuse
}

func (f *fakeMessenger) ChangeMessageVisibility(_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	f.released = in
	return &sqs.ChangeMessageVisibilityOutput{}, f.refuse
}

func (f *fakeMessenger) SendMessage(_ context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.sent = in
	return &sqs.SendMessageOutput{}, f.refuse
}

func (f *fakeMessenger) GetQueueAttributes(_ context.Context, in *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	f.asked = in
	if f.refuse != nil {
		return nil, f.refuse
	}
	return &sqs.GetQueueAttributesOutput{Attributes: f.attributes}, nil
}
