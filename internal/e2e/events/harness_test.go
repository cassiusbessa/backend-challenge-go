//go:build integration

// The scaffolding of the suite: the real stack, a subscriber of the topic that
// exists only for the test, and the helpers that write an event and read the
// rows back.
package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/junglegaming/backend-challenge-go/internal/app/relayoutbox"
	"github.com/junglegaming/backend-challenge-go/internal/platform/broker"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/postgres"
	"github.com/junglegaming/backend-challenge-go/internal/platform/probe"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// TestMain falls back to the LocalStack credential when it does not come from
// the environment, so the local gate does not depend on a prepared shell.
//
// The suites that spawn the binary hand it these three; this one builds its own
// SNS and SQS clients in the process of the test, so it is this process that has
// to carry them.
func TestMain(m *testing.M) {
	for key, value := range localAWS {
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				panic(err)
			}
		}
	}
	os.Exit(m.Run())
}

var localAWS = map[string]string{
	"AWS_ACCESS_KEY_ID":     "test",
	"AWS_SECRET_ACCESS_KEY": "test",
	"AWS_REGION":            "us-east-1",
}

// delivered is one message as it reached the subscriber: the body the topic
// carried, and the two fields the FIFO topic required of the send.
type delivered struct {
	Body            string
	GroupID         string
	DeduplicationID string
}

// subscriber is a FIFO queue of this test alone, subscribed to the topic so the
// suite can read what was published.
//
// The deployment provisions no subscription on purpose — there is no consumer
// of these events yet — so the only way to assert what reached the topic is to
// attach one for the length of a case and take it down after.
type subscriber struct {
	client *sqs.Client
	url    string
}

func subscribe(ctx context.Context, t *testing.T) *subscriber {
	t.Helper()
	queues, err := probe.NewClient(ctx, endpoint())
	if err != nil {
		t.Fatalf("open the queue client = %v, want nil", err)
	}
	name := "events-suite-" + strings.ReplaceAll(uuid.NewV7().String(), "-", "") + ".fifo"
	created, err := queues.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String(name),
		Attributes: map[string]string{"FifoQueue": "true"},
	})
	if err != nil {
		t.Fatalf("create the subscriber queue = %v, want nil", err)
	}
	url := aws.ToString(created.QueueUrl)
	t.Cleanup(func() {
		closing := context.WithoutCancel(ctx)
		_, _ = queues.DeleteQueue(closing, &sqs.DeleteQueueInput{QueueUrl: aws.String(url)})
	})
	attach(ctx, t, queues, url)
	return &subscriber{client: queues, url: url}
}

// attach points the topic at the queue with raw delivery, so the body the
// subscriber reads is the payload of the row and not an envelope of the broker
// wrapped around it.
func attach(ctx context.Context, t *testing.T, queues *sqs.Client, url string) {
	t.Helper()
	arn := queueARN(ctx, t, queues, url)
	topics, err := topicClient(ctx)
	if err != nil {
		t.Fatalf("open the topic client = %v, want nil", err)
	}
	created, err := topics.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn:   aws.String(topicARN()),
		Protocol:   aws.String("sqs"),
		Endpoint:   aws.String(arn),
		Attributes: map[string]string{"RawMessageDelivery": "true"},
	})
	if err != nil {
		t.Fatalf("subscribe to the topic = %v, want nil", err)
	}
	t.Cleanup(func() {
		closing := context.WithoutCancel(ctx)
		_, _ = topics.Unsubscribe(closing, &sns.UnsubscribeInput{SubscriptionArn: created.SubscriptionArn})
	})
}

func queueARN(ctx context.Context, t *testing.T, queues *sqs.Client, url string) string {
	t.Helper()
	attributes, err := queues.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(url),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatalf("read the queue arn = %v, want nil", err)
	}
	return attributes.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
}

func topicClient(ctx context.Context) (*sns.Client, error) {
	awsCfg, err := broker.LoadAWS(ctx)
	if err != nil {
		return nil, err
	}
	return sns.NewFromConfig(awsCfg, func(o *sns.Options) {
		o.BaseEndpoint = aws.String(endpoint())
	}), nil
}

// receive polls the subscriber until it has as many messages as the case asked
// for, or the deadline comes. The topic is asynchronous, so a single read would
// be a race with it.
func (s *subscriber) receive(ctx context.Context, t *testing.T, want int) []delivered {
	t.Helper()
	deadline := time.Now().Add(receiveWait)
	var got []delivered
	for time.Now().Before(deadline) && len(got) < want {
		got = append(got, s.poll(ctx, t)...)
	}
	if len(got) != want {
		t.Fatalf("messages on the topic = %d, want %d", len(got), want)
	}
	return got
}

func (s *subscriber) poll(ctx context.Context, t *testing.T) []delivered {
	t.Helper()
	out, err := s.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(s.url),
		MaxNumberOfMessages: 10,
		WaitTimeSeconds:     1,
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{
			sqstypes.MessageSystemAttributeNameMessageGroupId,
			sqstypes.MessageSystemAttributeNameMessageDeduplicationId,
		},
	})
	if err != nil {
		t.Fatalf("receive from the subscriber = %v, want nil", err)
	}
	var read []delivered
	for _, message := range out.Messages {
		read = append(read, delivered{
			Body:            aws.ToString(message.Body),
			GroupID:         message.Attributes[string(sqstypes.MessageSystemAttributeNameMessageGroupId)],
			DeduplicationID: message.Attributes[string(sqstypes.MessageSystemAttributeNameMessageDeduplicationId)],
		})
		_, _ = s.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
			QueueUrl:      aws.String(s.url),
			ReceiptHandle: message.ReceiptHandle,
		})
	}
	return read
}

// receiveWait bounds the poll of the subscriber. The topic and the queue are
// local, so a message that has not arrived by then is one that was not sent.
const receiveWait = 20 * time.Second

func open(t *testing.T) (context.Context, *postgres.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	pool := postgres.NewPool(config.Config{DatabaseURL: suiteenv.DatabaseURL()})
	if err := pool.Open(ctx); err != nil {
		t.Fatalf("open pool = %v, want nil", err)
	}
	t.Cleanup(func() { _ = pool.Close(context.Background()) })
	return ctx, pool
}

func endpoint() string {
	return suiteenv.Or("SNS_ENDPOINT", "http://localhost:4566")
}

func topicARN() string {
	return suiteenv.Or("SNS_TOPIC_ARN", "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo")
}

// noSpan is the report of a send nothing is watching. The link between the send
// and the commit has cases of its own beside the tracer; here it would only add
// an exporter to every case.
func noSpan(ctx context.Context, _, _ string) (context.Context, func(error)) {
	return ctx, func(error) {}
}

// noCount is the count of a send nothing is measuring. The series the relay
// moves have cases of their own beside the runner; here they would only add a
// registry to every case.
func noCount(string) {}

func quiet() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// sender publishes through the real topic and records what it sent, so a case
// can tell a send that happened from one that did not.
type sender struct {
	topic *broker.Topic
	sent  []string
}

func publisher(ctx context.Context, t *testing.T) *sender {
	t.Helper()
	topic := broker.NewTopic(config.Config{SNSEndpoint: endpoint(), SNSTopicARN: topicARN()})
	if err := topic.Open(ctx); err != nil {
		t.Fatalf("Open the topic = %v, want nil", err)
	}
	return &sender{topic: topic}
}

func (s *sender) Publish(ctx context.Context, message relayoutbox.Message) error {
	s.sent = append(s.sent, message.DeduplicationID)
	return s.topic.Publish(ctx, message)
}

func (s *sender) Permanent(err error) bool {
	return s.topic.Permanent(err)
}

// refusing is a broker that answers the same refusal to every send, which is
// how a case drives the backoff and the death of a row without an outage.
type refusing struct {
	permanent bool
	sends     int
}

func (r *refusing) Publish(context.Context, relayoutbox.Message) error {
	r.sends++
	return errors.New("the broker refused the send")
}

func (r *refusing) Permanent(error) bool { return r.permanent }
