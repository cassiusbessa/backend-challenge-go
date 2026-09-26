//go:build integration

package scenarios

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/junglegaming/backend-challenge-go/internal/platform/probe"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// queues is the pair of FIFO queues one case works over, and the client the case
// reads them with, which reaches the broker directly and never through a proxy.
type queues struct {
	client  *sqs.Client
	ingress string
	dead    string
}

// createQueues builds the ingress queue and the dead-letter queue behind it, with
// the redrive the deployment uses, and takes both down after the case.
func createQueues(ctx context.Context, t *testing.T) *queues {
	t.Helper()
	client, err := probe.NewClient(ctx, sqsEndpoint())
	if err != nil {
		t.Fatalf("open the queue client = %v, want nil", err)
	}
	suffix := strings.ReplaceAll(suiteenv.NewID(), "-", "")
	dead := createQueue(ctx, t, client, "scenario-dlq-"+suffix+".fifo", nil)
	redrive, err := json.Marshal(map[string]any{
		"deadLetterTargetArn": queueARN(ctx, t, client, dead),
		// The count the deployment provisions, so the limit of the consumer sits
		// below it here too.
		"maxReceiveCount": 15,
	})
	if err != nil {
		t.Fatalf("marshal the redrive policy = %v, want nil", err)
	}
	ingress := createQueue(ctx, t, client, "scenario-"+suffix+".fifo", map[string]string{
		"RedrivePolicy":                 string(redrive),
		"VisibilityTimeout":             "6",
		"ReceiveMessageWaitTimeSeconds": "1",
	})
	return &queues{client: client, ingress: ingress, dead: dead}
}

func createQueue(ctx context.Context, t *testing.T, client *sqs.Client, name string, attributes map[string]string) string {
	t.Helper()
	all := map[string]string{"FifoQueue": "true"}
	for key, value := range attributes {
		all[key] = value
	}
	created, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: all})
	if err != nil {
		t.Fatalf("create %s = %v, want nil", name, err)
	}
	queueURL := aws.ToString(created.QueueUrl)
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(context.WithoutCancel(ctx), &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})
	return queueURL
}

func queueARN(ctx context.Context, t *testing.T, client *sqs.Client, queueURL string) string {
	t.Helper()
	out, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatalf("read the queue arn = %v, want nil", err)
	}
	return out.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
}

// send puts one message on the ingress queue of the case.
//
// The group is the wallet in lowercase, which is what keeps two operations of one
// wallet in order. The deduplication is fresh every time, so the five-minute
// window of the broker never swallows a send: that window is not the inbox, and
// what a case sends is meant to reach the inbox.
func (q *queues) send(ctx context.Context, t *testing.T, walletID, body string) {
	t.Helper()
	_, err := q.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(q.ingress),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(strings.ToLower(walletID)),
		MessageDeduplicationId: aws.String(suiteenv.NewID()),
	})
	if err != nil {
		t.Fatalf("send = %v, want nil", err)
	}
}

// awaitEmpty waits until the ingress queue holds nothing, visible or not, which
// is what says the message was answered for.
func (q *queues) awaitEmpty(ctx context.Context, t *testing.T) {
	t.Helper()
	until(ctx, t, "the ingress queue to hold nothing", func() bool {
		return q.depth(ctx, t, q.ingress) == 0
	})
}

// depth is every message of that queue: waiting, in flight and delayed. A count
// of the visible ones alone would read a message being decided as a queue that
// drained.
func (q *queues) depth(ctx context.Context, t *testing.T, queueURL string) int64 {
	t.Helper()
	names := []sqstypes.QueueAttributeName{
		sqstypes.QueueAttributeNameApproximateNumberOfMessages,
		sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		sqstypes.QueueAttributeNameApproximateNumberOfMessagesDelayed,
	}
	out, err := q.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: aws.String(queueURL), AttributeNames: names})
	if err != nil {
		t.Fatalf("read the depth of %s = %v, want nil", queueURL, err)
	}
	var total int64
	for _, name := range names {
		value, err := strconv.ParseInt(out.Attributes[string(name)], 10, 64)
		if err != nil {
			t.Fatalf("parse %s = %v, want nil", name, err)
		}
		total += value
	}
	return total
}

// envelope is the message of the ingress queue that carries the operation, under
// that identity and that idempotency key. On the queue the key travels in the
// data, which is where the contract puts it for that channel.
func (op operation) envelope(messageID, key string) string {
	raw, err := json.Marshal(map[string]any{"messageId": messageID, "data": op.with(map[string]any{"idempotencyKey": key})})
	if err != nil {
		panic(err)
	}
	return string(raw)
}
