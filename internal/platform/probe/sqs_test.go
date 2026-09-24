package probe

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

func TestNewQueueKeepsEndpointAndURL(t *testing.T) {
	t.Parallel()
	endpoint := "http://localstack:4566"
	url := endpoint + "/000000000000/wager-transactions.fifo"
	queue := NewQueue(config.Config{SQSEndpoint: endpoint, SQSQueueURL: url})
	if queue.endpoint != endpoint {
		t.Fatalf("queue endpoint = %s, want %s", queue.endpoint, endpoint)
	}
	if queue.url != url {
		t.Fatalf("queue url = %s, want %s", queue.url, url)
	}
}

func TestNewClientPointsAtTheGivenEndpoint(t *testing.T) {
	t.Parallel()
	endpoint := "http://localstack:4566"
	client, err := NewClient(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("NewClient with endpoint = %v, want nil", err)
	}
	got := aws.ToString(client.Options().BaseEndpoint)
	if got != endpoint {
		t.Fatalf("client endpoint = %s, want %s", got, endpoint)
	}
}

func TestNewClientFallsBackToADefaultRegion(t *testing.T) {
	t.Parallel()
	client, err := NewClient(context.Background(), "http://localstack:4566")
	if err != nil {
		t.Fatalf("NewClient without a region in the environment = %v, want nil", err)
	}
	if client.Options().Region == "" {
		t.Fatalf("region = %q, want the default region", client.Options().Region)
	}
}
