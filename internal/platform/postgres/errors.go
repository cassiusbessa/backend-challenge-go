package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// uniqueViolation is the SQLSTATE of a unique index refusing the row.
const uniqueViolation = "23505"

// walletUniqueConstraint is the name the migration gives to one wallet per
// player and currency. The name is matched instead of the bare SQLSTATE so that
// another unique index does not answer as a duplicate wallet.
const walletUniqueConstraint = "wallets_one_per_player_and_currency"

// wrap is the boundary of this package: it names the operation and decides the
// class of the failure.
//
// A refusal of the contract keeps the operation chain and carries no stack; an
// infrastructure failure gets the stack captured once. Nothing here ever wraps
// one class inside the other.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	refusal := refusalOf(err)
	if refusal != nil {
		return fmt.Errorf("%s: %w", op, refusal)
	}
	return fault.Wrap(op, err)
}

// refusalOf answers the contract refusal the database decided, or nil when the
// failure is infrastructure.
func refusalOf(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return nil
	}
	if pgErr.Code == uniqueViolation && pgErr.ConstraintName == walletUniqueConstraint {
		return storage.ErrWalletExists
	}
	return nil
}
