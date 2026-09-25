package postgres

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// uniqueViolation is the SQLSTATE of a unique index refusing the row.
const uniqueViolation = "23505"

// The names the migration gives to the unique indexes this adapter reads back.
// The name is matched instead of the bare SQLSTATE so that one index does not
// answer as another: the three refusals below are three different answers to the
// provider.
const (
	// walletUniqueConstraint is one wallet per player and currency.
	walletUniqueConstraint = "wallets_one_per_player_and_currency"
	// keyUniqueIndex is one transaction per provider and idempotency key.
	keyUniqueIndex = "wager_transactions_one_per_provider_and_key"
	// externalUniqueIndex is one transaction per provider and external id.
	externalUniqueIndex = "wager_transactions_one_per_provider_and_external_id"

	// reversalUniqueIndex is one PROCESSED reversal per cited operation. It is
	// named here to be left out of duplicateOf on purpose, and the name is what
	// makes that decision visible instead of absent.
	//
	// What decides ALREADY_REVERSED is the query taken after the wallet is
	// locked, which the second reversal reaches only once the first has
	// committed. The index behind it stays as an invariant of the database: a
	// violation is infrastructure, the transaction rolls back whole, and the
	// resend of the provider meets that query answering the token correctly.
	// Mapping it would ask for a rejection to be written inside a transaction the
	// violation already aborted, which would need a second transaction that can
	// itself fail and leave nothing recorded.
	reversalUniqueIndex = "wager_transactions_one_processed_reversal_per_reference"
)

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
	if pgErr.Code != uniqueViolation {
		return nil
	}
	return duplicateOf(pgErr.ConstraintName)
}

// duplicateOf names what the violated index refuses.
//
// The two wager indexes are the arbiter of idempotency: the decision belongs to
// the database and not to a query two replicas could both pass at the same time,
// and each index carries its own token of the catalog.
func duplicateOf(constraint string) error {
	switch constraint {
	case walletUniqueConstraint:
		return storage.ErrWalletExists
	case keyUniqueIndex:
		return wager.NewRejection(wager.IdempotencyConflict, nil)
	case externalUniqueIndex:
		return wager.NewRejection(wager.DuplicateExternalTransaction, nil)
	}
	return nil
}

// missingWallet answers the absence of the wallet, and classifies anything else
// as a failure of the named operation.
//
// The absence keeps the operation in the chain, the same as wrap does for a
// refusal of the contract: a lock and a read look for the same row, and without
// the operation the two answer the same line with no trail.
func missingWallet(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, storage.ErrWalletNotFound)
	}
	return wrap(op, err)
}

// missingTransaction answers the absence of the transaction, with the operation
// named in the chain for the same reason as missingWallet. A transaction of
// another provider comes through here too, because the provider is part of the
// query: the caller cannot tell the two apart, and neither can the response.
func missingTransaction(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, storage.ErrTransactionNotFound)
	}
	return wrap(op, err)
}
