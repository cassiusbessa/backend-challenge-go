package probe

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

type Postgres struct {
	url  string
	pool *pgxpool.Pool
}

func NewPostgres(cfg config.Config) *Postgres {
	return &Postgres{url: cfg.DatabaseURL}
}

func (p *Postgres) Open(ctx context.Context) error {
	pool, err := pgxpool.New(ctx, p.url)
	if err != nil {
		return err
	}
	p.pool = pool
	return nil
}

func (p *Postgres) Close(context.Context) error {
	if p.pool == nil {
		return nil
	}
	p.pool.Close()
	return nil
}

func (p *Postgres) Check(ctx context.Context) error {
	var one int
	return p.pool.QueryRow(ctx, "SELECT 1").Scan(&one)
}
