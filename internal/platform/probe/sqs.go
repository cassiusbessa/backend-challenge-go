package probe

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

// Queue confirma que a URL configurada existe no broker.
type Queue struct {
	endpoint string
	url      string
	client   *sqs.Client
}

// NewQueue guarda endpoint e URL. O cliente abre no lifecycle.
func NewQueue(cfg config.Config) *Queue {
	return &Queue{endpoint: cfg.SQSEndpoint, url: cfg.SQSQueueURL}
}

// Open monta o cliente SQS sem chamar o broker.
func (q *Queue) Open(ctx context.Context) error {
	client, err := NewClient(ctx, q.endpoint)
	if err != nil {
		return err
	}
	q.client = client
	return nil
}

// NewClient cria um cliente apontando para o endpoint informado.
func NewClient(ctx context.Context, endpoint string) (*sqs.Client, error) {
	awsCfg, err := loadAWS(ctx)
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	}), nil
}

func loadAWS(ctx context.Context) (aws.Config, error) {
	return awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
		awsconfig.WithEC2IMDSClientEnableState(imds.ClientDisabled),
	)
}

// Check lê um atributo da fila. Fila ausente devolve erro.
func (q *Queue) Check(ctx context.Context) error {
	_, err := q.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(q.url),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	return err
}
