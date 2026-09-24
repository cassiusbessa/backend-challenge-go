package probe

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

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
	awsCfg, err := loadAWS(ctx)
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	}), nil
}

// A credencial vem da cadeia padrão do SDK: ambiente no Compose e no CI, papel
// na nuvem. O IMDS fica desligado para não esperar por um endereço que só existe
// dentro da EC2.
func loadAWS(ctx context.Context) (aws.Config, error) {
	return awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithDefaultRegion("us-east-1"),
		awsconfig.WithEC2IMDSClientEnableState(imds.ClientDisabled),
	)
}

func (q *Queue) Check(ctx context.Context) error {
	_, err := q.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(q.url),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	return err
}
