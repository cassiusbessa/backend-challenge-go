// Package postgres is the PostgreSQL adapter: the single pool of the process,
// the transactional boundary and the repositories it hands out.
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// ErrPoolClosed is the pool being asked for work before the process opened it
// or after it closed.
var ErrPoolClosed = errors.New("postgres: pool is not open")

// setAppRole makes the session operate as the role the migration created, which
// holds SELECT and INSERT on the ledger and nothing else. Whoever connects may
// be a superuser, and a superuser bypasses every GRANT; this role does not, so
// an UPDATE on a ledger row is refused by the privilege check as well as by the
// trigger.
const setAppRole = "SET ROLE wager_app"

// Pool is the single connection pool of the process: opened on startup, closed
// once on shutdown. The zero value is not used: NewPool is the only
// constructor.
type Pool struct {
	url  string
	pool *pgxpool.Pool
}

func NewPool(cfg config.Config) *Pool {
	return &Pool{url: cfg.DatabaseURL}
}

// Open builds the pool without dialling: pgxpool connects on the first acquire,
// so a process pointed at an unreachable database still listens and answers
// live while readiness answers 503.
func (p *Pool) Open(ctx context.Context) error {
	settings, err := pgxpool.ParseConfig(p.url)
	if err != nil {
		return fault.Wrap("parse database url", err)
	}
	settings.AfterConnect = assumeAppRole
	pool, err := pgxpool.NewWithConfig(ctx, settings)
	if err != nil {
		return fault.Wrap("open pool", err)
	}
	p.pool = pool
	return nil
}

func assumeAppRole(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, setAppRole)
	if err != nil {
		return fault.Wrap("assume application role", err)
	}
	return nil
}

// Close releases the pool. Closing twice is a no-op, so the lifecycle hook and
// an early failure cannot disagree.
func (p *Pool) Close(context.Context) error {
	if p.pool == nil {
		return nil
	}
	p.pool.Close()
	p.pool = nil
	return nil
}

// Querier answers the open pool, or ErrPoolClosed when the process has not
// opened it.
func (p *Pool) Querier() (*pgxpool.Pool, error) {
	if p.pool == nil {
		return nil, ErrPoolClosed
	}
	return p.pool, nil
}
