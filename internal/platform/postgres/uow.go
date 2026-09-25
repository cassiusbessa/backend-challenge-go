package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
)

// UnitOfWork is the only writer of the financial tables. The zero value is not
// used: NewUnitOfWork is the only constructor.
type UnitOfWork struct {
	source *Pool
}

func NewUnitOfWork(source *Pool) *UnitOfWork {
	return &UnitOfWork{source: source}
}

// Within opens READ COMMITTED, commits when the work returns nil and rolls the
// whole set back on any error. A use case that fails halfway leaves no wallet,
// no transaction and no entry behind.
func (u *UnitOfWork) Within(ctx context.Context, work func(storage.Tx) error) error {
	pool, err := u.source.Querier()
	if err != nil {
		return wrap("acquire pool", err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return wrap("begin transaction", err)
	}
	if err := work(&transaction{tx: tx}); err != nil {
		// The rollback of a doomed transaction adds nothing to the failure the
		// work already named, and joining the two would break the log line.
		_ = tx.Rollback(ctx)
		return err
	}
	return u.commit(ctx, tx)
}

func (u *UnitOfWork) commit(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return wrap("commit transaction", err)
	}
	return nil
}

// transaction hands out the repositories of one open transaction.
type transaction struct {
	tx pgx.Tx
}

func (t *transaction) Wallets() storage.Wallets {
	return wallets{tx: t.tx}
}

func (t *transaction) Transactions() storage.Transactions {
	return transactions{tx: t.tx}
}

func (t *transaction) Entries() storage.Entries {
	return entries{tx: t.tx}
}

func (t *transaction) Outbox() storage.Outbox {
	return outbox{tx: t.tx}
}

func (t *transaction) Inbox() storage.Inbox {
	return inbox{tx: t.tx}
}
