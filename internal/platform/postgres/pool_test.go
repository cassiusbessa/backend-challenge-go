package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

const unreachable = "postgres://junglegaming:junglegaming@127.0.0.1:1/junglegaming?sslmode=disable"

func TestQuerier_refusesBeforeTheProcessOpensThePool(t *testing.T) {
	t.Parallel()
	_, err := NewPool(config.Config{DatabaseURL: unreachable}).Querier()
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Querier = %v, want %v", err, ErrPoolClosed)
	}
}

// pgxpool dials on the first acquire, so an unreachable database still lets the
// process listen: that is what keeps live at 200 while readiness answers 503.
func TestOpen_doesNotDialAnUnreachableDatabase(t *testing.T) {
	t.Parallel()
	pool := NewPool(config.Config{DatabaseURL: unreachable})
	if err := pool.Open(context.Background()); err != nil {
		t.Fatalf("Open = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := pool.Close(context.Background()); err != nil {
			t.Fatalf("Close = %v, want nil", err)
		}
	})
	if _, err := pool.Querier(); err != nil {
		t.Fatalf("Querier after open = %v, want nil", err)
	}
}

func TestOpen_refusesAnUnparseableURL(t *testing.T) {
	t.Parallel()
	err := NewPool(config.Config{DatabaseURL: "not a database url"}).Open(context.Background())
	if err == nil {
		t.Fatalf("Open with a broken URL = nil, want an error")
	}
}

func TestClose_isSafeWithoutAnOpenPool(t *testing.T) {
	t.Parallel()
	pool := NewPool(config.Config{DatabaseURL: unreachable})
	if err := pool.Close(context.Background()); err != nil {
		t.Fatalf("Close without open = %v, want nil", err)
	}
}

func TestClose_isSafeTwice(t *testing.T) {
	t.Parallel()
	pool := NewPool(config.Config{DatabaseURL: unreachable})
	if err := pool.Open(context.Background()); err != nil {
		t.Fatalf("Open = %v, want nil", err)
	}
	if err := pool.Close(context.Background()); err != nil {
		t.Fatalf("first Close = %v, want nil", err)
	}
	if err := pool.Close(context.Background()); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}
