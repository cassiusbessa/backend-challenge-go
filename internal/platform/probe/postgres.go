// Package probe consulta PostgreSQL e a fila SQS no ready.
package probe

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

// Postgres executa SELECT 1 na carteira de conexões.
type Postgres struct {
	url  string
	pool *pgxpool.Pool
}

// NewPostgres guarda a URL. A conexão abre no lifecycle.
func NewPostgres(cfg config.Config) *Postgres {
	return &Postgres{url: cfg.DatabaseURL}
}

// Open cria o pool sem consultar o banco.
func (p *Postgres) Open(ctx context.Context) error {
	pool, err := pgxpool.New(ctx, p.url)
	if err != nil {
		return err
	}
	p.pool = pool
	return nil
}

// Close devolve as conexões do pool.
func (p *Postgres) Close(context.Context) error {
	if p.pool == nil {
		return nil
	}
	p.pool.Close()
	return nil
}

// Check confirma que o PostgreSQL responde.
func (p *Postgres) Check(ctx context.Context) error {
	var one int
	return p.pool.QueryRow(ctx, "SELECT 1").Scan(&one)
}
