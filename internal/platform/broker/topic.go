package broker

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/junglegaming/backend-challenge-go/internal/app/relayoutbox"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// Topic publishes to the FIFO topic of wallet events. The zero value is not
// used: NewTopic is the only constructor, and the client is built by Open.
type Topic struct {
	endpoint string
	arn      string
	client   *sns.Client
}

func NewTopic(cfg config.Config) *Topic {
	return &Topic{endpoint: cfg.SNSEndpoint, arn: cfg.SNSTopicARN}
}

// Open builds the client without dialling, the same as the pool and the queue
// client do: a broker that is out must not keep the process from listening.
func (t *Topic) Open(ctx context.Context) error {
	awsCfg, err := LoadAWS(ctx)
	if err != nil {
		return fault.Wrap("load aws configuration", err)
	}
	t.client = sns.NewFromConfig(awsCfg, func(o *sns.Options) {
		o.BaseEndpoint = aws.String(t.endpoint)
	})
	return nil
}

// Publish sends one message to the topic. The group and the deduplication are
// what the FIFO topic requires, and both come off the row the relay claimed.
func (t *Topic) Publish(ctx context.Context, message relayoutbox.Message) error {
	_, err := t.client.Publish(ctx, &sns.PublishInput{
		TopicArn:               aws.String(t.arn),
		Message:                aws.String(message.Body),
		MessageGroupId:         aws.String(message.GroupID),
		MessageDeduplicationId: aws.String(message.DeduplicationID),
	})
	if err != nil {
		return fault.Wrap("publish event", err)
	}
	return nil
}
