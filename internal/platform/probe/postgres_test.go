package probe

import (
	"context"
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/postgres"
)

func TestCheck_refusesWhileTheSharedPoolIsClosed(t *testing.T) {
	t.Parallel()
	shared := postgres.NewPool(config.Config{DatabaseURL: "postgres://junglegaming@127.0.0.1:1/junglegaming"})
	err := NewPostgres(shared).Check(context.Background())
	if !errors.Is(err, postgres.ErrPoolClosed) {
		t.Fatalf("check before open = %v, want %v", err, postgres.ErrPoolClosed)
	}
}

func TestNewPostgres_readinessDoesNotOwnTheConnection(t *testing.T) {
	t.Parallel()
	shared := postgres.NewPool(config.Config{DatabaseURL: "postgres://junglegaming@127.0.0.1:1/junglegaming"})
	probe := NewPostgres(shared)
	if probe.source != shared {
		t.Fatalf("probe source = %p, want the shared pool %p", probe.source, shared)
	}
}
