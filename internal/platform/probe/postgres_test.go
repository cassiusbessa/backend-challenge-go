package probe

import (
	"context"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

func TestNewPostgresKeepsTheConfiguredURL(t *testing.T) {
	t.Parallel()
	url := "postgres://junglegaming@127.0.0.1:1/junglegaming"
	postgres := NewPostgres(config.Config{DatabaseURL: url})
	if postgres.url != url {
		t.Fatalf("pool url = %s, want %s", postgres.url, url)
	}
}

func TestCloseWithoutOpenDoesNothing(t *testing.T) {
	t.Parallel()
	postgres := NewPostgres(config.Config{DatabaseURL: "postgres://junglegaming@127.0.0.1:1/junglegaming"})
	err := postgres.Close(context.Background())
	if err != nil {
		t.Fatalf("close without open = %v, want nil", err)
	}
}
