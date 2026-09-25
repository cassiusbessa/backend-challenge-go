package probe

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/junglegaming/backend-challenge-go/internal/platform/broker"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

type Queue struct {
	endpoint string
	url      string
	client   *sqs.Client
}

func NewQueue(cfg config.Config) *Queue {
	return &Queue{endpoint: cfg.SQSEndpoint, url: cfg.SQSQueueURL}
}

func (q *Queue) Open(ctx context.Context) error {
	client, err := NewClient(ctx, q.endpoint)
	if err != nil {
		return err
	}
	q.client = client
	return nil
}

func NewClient(ctx context.Context, endpoint string) (*sqs.Client, error) {
	awsCfg, err := broker.LoadAWS(ctx)
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	}), nil
}

func (q *Queue) Check(ctx context.Context) error {
	_, err := q.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(q.url),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	return err
}
