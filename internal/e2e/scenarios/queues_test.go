//go:build integration

package scenarios

import (
	"context"
	"encoding/json"
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
