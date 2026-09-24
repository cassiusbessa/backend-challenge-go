package probe

import (
	"context"

	"github.com/junglegaming/backend-challenge-go/internal/platform/postgres"
)

// Postgres answers readiness through the pool the process already shares. It
// does not own the connection: opening a second one would give the same
// database two owners, and shutdown would depend on the order between them.
type Postgres struct {
	source *postgres.Pool
}

func NewPostgres(source *postgres.Pool) *Postgres {
	return &Postgres{source: source}
}

func (p *Postgres) Check(ctx context.Context) error {
	pool, err := p.source.Querier()
	if err != nil {
		return err
	}
	var one int
	return pool.QueryRow(ctx, "SELECT 1").Scan(&one)
}
