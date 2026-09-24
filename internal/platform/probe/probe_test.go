package probe

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

func TestCloseWithoutOpenDoesNothing(t *testing.T) {
	t.Parallel()
	postgres := NewPostgres(config.Config{DatabaseURL: "postgres://junglegaming@127.0.0.1:1/junglegaming"})
	err := postgres.Close(context.Background())
	if err != nil {
		t.Fatalf("close = %v, want nil", err)
	}
}

func TestNewQueueKeepsEndpointAndURL(t *testing.T) {
	t.Parallel()
	endpoint := "http://localstack:4566"
	url := endpoint + "/000000000000/wager-transactions.fifo"
	queue := NewQueue(config.Config{SQSEndpoint: endpoint, SQSQueueURL: url})
	if queue.endpoint != endpoint {
		t.Fatalf("endpoint = %s, want %s", queue.endpoint, endpoint)
	}
	if queue.url != url {
		t.Fatalf("url = %s, want %s", queue.url, url)
	}
}

func TestNewClientPointsAtTheGivenEndpoint(t *testing.T) {
	t.Parallel()
	endpoint := "http://localstack:4566"
	client, err := NewClient(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("erro = %v, want nil", err)
	}
	got := aws.ToString(client.Options().BaseEndpoint)
	if got != endpoint {
		t.Fatalf("endpoint = %s, want %s", got, endpoint)
	}
}

func TestNewClientFallsBackToADefaultRegion(t *testing.T) {
	t.Parallel()
	client, err := NewClient(context.Background(), "http://localstack:4566")
	if err != nil {
		t.Fatalf("erro = %v, want nil", err)
	}
	if client.Options().Region == "" {
		t.Fatal("region = vazia, want a região padrão")
	}
}
