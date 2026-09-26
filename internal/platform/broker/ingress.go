package broker

import (
	"context"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/wagerqueue"
)

// batch is how many messages one fetch takes. It is the most the API grants, and
// the decision is per message either way.
const batch = 10

// traceAttribute is where the sender puts the W3C propagation. It is a message
// attribute and not a field of the body, so it stays out of the hash of the
// business the same way a header does over HTTP.
const traceAttribute = "traceparent"

// Ingress is the queue of incoming wagers and the dead-letter queue beside it.
// The zero value is not used: NewIngress is the only constructor, and the client
// is built by Open.
type Ingress struct {
	endpoint string
	url      string
	deadURL  string
	client   messenger
}

// messenger is the part of the queue API this adapter calls.
//
// It is an interface so a case can read what reaches the broker — the two system
// attributes the ingress cannot work without, the window of a release, the queue a
// copy lands on — without a broker being up. The concrete client is built by Open.
type messenger interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	GetQueueAttributes(ctx context.Context, in *sqs.GetQueueAttributesInput, opts ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

func NewIngress(cfg config.Config) *Ingress {
	return &Ingress{endpoint: cfg.SQSEndpoint, url: cfg.SQSQueueURL, deadURL: cfg.SQSDeadLetterURL}
}

// Open builds the client without dialling, the same as the pool and the topic do:
// a broker that is out must not keep the process from listening.
func (i *Ingress) Open(ctx context.Context) error {
	awsCfg, err := LoadAWS(ctx)
	if err != nil {
		return fault.Wrap("load aws configuration", err)
	}
	i.client = sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(i.endpoint)
	})
	return nil
}

// Receive waits for messages in one long poll, asking for the sender identity and
// the delivery count beside each body.
//
// Both are system attributes rather than payload, and they are what the ingress
// cannot work without: the first authorizes the operation and the second ends a
// message no attempt can settle. A second call per message to fetch them would be
// a round trip for something this one answers.
func (i *Ingress) Receive(ctx context.Context, wait, visibility time.Duration) ([]wagerqueue.Delivery, error) {
	out, err := i.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(i.url),
		MaxNumberOfMessages: batch,
		WaitTimeSeconds:     seconds(wait),
		VisibilityTimeout:   seconds(visibility),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameSenderId,
			types.MessageSystemAttributeNameApproximateReceiveCount,
			types.MessageSystemAttributeNameMessageGroupId,
			types.MessageSystemAttributeNameMessageDeduplicationId,
		},
		MessageAttributeNames: []string{traceAttribute},
	})
	if err != nil {
		return nil, fault.Wrap("receive wager messages", err)
	}
	return deliveriesOf(out.Messages), nil
}

func deliveriesOf(messages []types.Message) []wagerqueue.Delivery {
	out := make([]wagerqueue.Delivery, 0, len(messages))
	for _, message := range messages {
		out = append(out, deliveryOf(message))
	}
	return out
}

func deliveryOf(message types.Message) wagerqueue.Delivery {
	return wagerqueue.Delivery{
		Body:          []byte(aws.ToString(message.Body)),
		Receipt:       aws.ToString(message.ReceiptHandle),
		Sender:        message.Attributes[string(types.MessageSystemAttributeNameSenderId)],
		Deliveries:    count(message.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)]),
		Group:         message.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
		Deduplication: message.Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)],
		Trace:         traceOf(message.MessageAttributes),
	}
}

// count reads the delivery count the broker registered. A value that is not a
// number answers one delivery: the count only ever moves the give-up forward, and
// reading it as the highest would abandon a message on its first arrival.
func count(raw string) int64 {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 1
	}
	return max(value, 1)
}

// traceOf answers the propagation the sender carried, or nothing for a message
// that carried none, which is a message the consumer opens its own trace for.
func traceOf(attributes map[string]types.MessageAttributeValue) map[string]string {
	carried, ok := attributes[traceAttribute]
	if !ok || carried.StringValue == nil {
		return nil
	}
	return map[string]string{traceAttribute: *carried.StringValue}
}

// Delete takes the message out of the queue.
func (i *Ingress) Delete(ctx context.Context, receipt string) error {
	_, err := i.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(i.url),
		ReceiptHandle: aws.String(receipt),
	})
	if err != nil {
		return fault.Wrap("delete wager message", err)
	}
	return nil
}

// Release makes the message visible again after that window. A window of zero
// hands it back at once.
func (i *Ingress) Release(ctx context.Context, receipt string, after time.Duration) error {
	_, err := i.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(i.url),
		ReceiptHandle:     aws.String(receipt),
		VisibilityTimeout: seconds(after),
	})
	if err != nil {
		return fault.Wrap("release wager message", err)
	}
	return nil
}

// DeadLetter copies the message to the dead-letter queue, under the group and the
// deduplication it arrived with.
//
// The group is kept so the order of a wallet is the same on both queues, and the
// deduplication is kept so a message copied twice — a delete that failed after the
// copy went through — lands there once.
func (i *Ingress) DeadLetter(ctx context.Context, delivery wagerqueue.Delivery) error {
	_, err := i.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(i.deadURL),
		MessageBody:            aws.String(string(delivery.Body)),
		MessageGroupId:         aws.String(delivery.Group),
		MessageDeduplicationId: aws.String(delivery.Deduplication),
	})
	if err != nil {
		return fault.Wrap("copy wager message to the dead-letter queue", err)
	}
	return nil
}

// Depth answers how many messages the ingress queue is holding.
func (i *Ingress) Depth(ctx context.Context) (int64, error) {
	return i.depthOf(ctx, i.url, "read the depth of the ingress queue")
}

// DeadLetterDepth answers how many messages the dead-letter queue is holding.
func (i *Ingress) DeadLetterDepth(ctx context.Context) (int64, error) {
	return i.depthOf(ctx, i.deadURL, "read the depth of the dead-letter queue")
}

func (i *Ingress) depthOf(ctx context.Context, queueURL, op string) (int64, error) {
	out, err := i.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages},
	})
	if err != nil {
		return 0, fault.Wrap(op, err)
	}
	waiting, err := strconv.ParseInt(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)], 10, 64)
	if err != nil {
		return 0, fault.Wrap(op, err)
	}
	return waiting, nil
}

// seconds is the unit the API takes for both windows. A duration below one second
// rounds to zero, which for the poll means no wait at all and for the visibility
// means the message comes straight back — both of them what the caller asked for.
//
// The value is bounded by what the API accepts rather than by what the type holds:
// the twelve hours of the visibility is the widest either window takes, and a
// configuration past it would be refused by the broker with the whole call.
func seconds(window time.Duration) int32 {
	return int32(min(max(window/time.Second, 0), maxWindowSeconds))
}

// maxWindowSeconds is the widest window the API takes, which is the twelve hours
// of the visibility timeout.
const maxWindowSeconds = 12 * 60 * 60
