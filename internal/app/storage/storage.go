// Package storage declares the persistence ports the use cases speak to.
//
// The adapter in internal/platform/postgres implements them, so a use case
// never names pgx and a unit test never needs a database.
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

var (
	// ErrWalletExists is the unique violation of one wallet per player and
	// currency. It is a refusal of the contract, not a business rejection: the
	// closed catalog of failure codes names wager operations only, and an
	// opening is not one.
	ErrWalletExists = errors.New("storage: player already holds a wallet in this currency")

	// ErrNotFound is the family of every absence: the row the caller named does
	// not exist. Each entity keeps the sentinel below, and every one of them
	// unwraps to this family, so errors.Is answers the question about the entity
	// and the question about the absence for the same error.
	ErrNotFound = errors.New("storage: row does not exist")

	// ErrWalletNotFound is the absence of the wallet the caller named.
	ErrWalletNotFound = NotFoundError{Entity: "wallet"}

	// ErrTransactionNotFound is the absence of the transaction the caller named.
	// A transaction of another provider answers this same absence, so neither the
	// body nor the status tells the two apart.
	ErrTransactionNotFound = NotFoundError{Entity: "wager transaction"}

	// ErrLostWrite is a balance update that affected no row: the version read
	// under the lock is no longer the stored one, so some write went past it.
	//
	// It is a transient infrastructure failure and carries no failureCode: the
	// SQL transaction rolls back whole and the operation is tried again.
	ErrLostWrite = errors.New("storage: balance write affected no row")
)

// NotFoundError is the absence of one named entity. It unwraps to ErrNotFound,
// which is what lets a border classify every absence in a single case while a
// caller that has to tell a wallet from a transaction still compares the
// sentinel.
//
// The zero value names no entity and is not an absence.
type NotFoundError struct {
	Entity string
}

// Error names the entity that does not exist and never the identity that was
// looked up: an absence is answered without echoing back what was asked for.
func (e NotFoundError) Error() string {
	return "storage: " + e.Entity + " does not exist"
}

// Unwrap answers the family, which is what makes errors.Is match ErrNotFound for
// every entity without this package listing them anywhere.
func (e NotFoundError) Unwrap() error {
	return ErrNotFound
}

// UnitOfWork is the transactional boundary: one use case commits once.
type UnitOfWork interface {
	Within(ctx context.Context, work func(Tx) error) error
}

// Tx hands out the repositories of one open transaction. They are reachable
// only from here, so a write outside the commit is not expressible.
type Tx interface {
	Wallets() Wallets
	Transactions() Transactions
	Entries() Entries
	Outbox() Outbox
}

// Wallets reads the wallet row for writing and writes it.
type Wallets interface {
	Insert(ctx context.Context, opened *wallet.Wallet) error

	// GetForUpdate locks the wallet row for writing and answers the state to
	// rehydrate from. Every operation that may move the balance goes through it,
	// because the decision must read a balance nobody else can change until the
	// commit. An absent wallet answers ErrWalletNotFound.
	GetForUpdate(ctx context.Context, id identity.WalletID) (wallet.State, error)

	// UpdateBalance writes the moved balance conditioned on the version that was
	// read under the lock, and answers ErrLostWrite when no row matched.
	//
	// An operation that moves nothing does not come through here at all: the
	// version rises only when the balance changes.
	UpdateBalance(ctx context.Context, moved *wallet.Wallet, readVersion int64) error
}

// Transactions reads and writes the wager transaction row.
type Transactions interface {
	Insert(ctx context.Context, recorded *wager.Transaction) error

	// ByKey answers the transaction of that provider and idempotency key, or
	// ErrTransactionNotFound. The pair is the scope of the key: the same key from
	// another provider is another operation.
	ByKey(ctx context.Context, provider identity.ProviderID, key identity.IdempotencyKey) (wager.State, error)

	// ByExternalID answers the operation that provider recorded under that
	// external identifier, which is how a citing operation finds the one it
	// cites, or ErrTransactionNotFound.
	//
	// The provider is part of the query and not of a check afterwards: an
	// operation of somebody else answers the same absence as one that was never
	// sent, so no branch can tell the two apart.
	ByExternalID(ctx context.Context, provider identity.ProviderID, external identity.ExternalTransactionID) (wager.State, error)

	// HasProcessedReversal reports whether the cited operation already carries a
	// reversal that reached PROCESSED. A reversal that ended REJECTED or FAILED
	// does not take the place, which is what the partial unique index of the
	// schema says too.
	//
	// It is asked after the wallet is locked, so the reversal that arrives second
	// only reaches it once the first has committed.
	HasProcessedReversal(ctx context.Context, provider identity.ProviderID, cited identity.ExternalTransactionID) (bool, error)

	// ClaimWait locks the row of one wait whose next attempt is due at that
	// instant, so that a single replica decides it, and answers it re-read under
	// that lock.
	//
	// A row another replica already holds is skipped rather than waited on, and
	// answers ErrTransactionNotFound, the same as a row that is no longer there
	// and as one whose next attempt has been moved past that instant: all three
	// mean this replica does not decide this wait now, and it comes back as a
	// candidate on the next scan.
	ClaimWait(ctx context.Context, id identity.TransactionID, due time.Time) (Wait, error)

	// EndWait writes the terminal decision over a row that was waiting, and
	// answers ErrTransactionNotFound when no row in the wait matched.
	//
	// It never writes the deadline, which is written once on entry, and it never
	// clears a field the destination status does not carry: a wait that ends
	// PROCESSED carries a balance and no token, and one that ends REJECTED
	// carries a token and no balance.
	EndWait(ctx context.Context, decided *wager.Transaction) error

	// RescheduleWait moves the next attempt of a wait that this attempt did not
	// end, and answers ErrTransactionNotFound when no row in the wait matched.
	//
	// It writes the schedule and nothing else: the status is already the one it
	// stays in, and the deadline was written on entry. Rescheduling is the second
	// destination of one attempt, which is why the attempt is counted here too.
	RescheduleWait(ctx context.Context, id identity.TransactionID, nextAttemptAt, at time.Time) error
}

// Wait is the row of one reference wait as the worker claims it.
//
// Attempts is the number of attempts already made on it. It is a metric and the
// window of the backoff, never what ends the wait, and it is kept beside the
// state instead of inside the aggregate for that reason.
type Wait struct {
	State    wager.State
	Attempts int64
}

// WaitCandidate is one row the scan of the queue chose.
//
// The wallet comes from the scan because the decision locks the wallet before
// the row of the wait, and that order cannot be taken from a row this replica
// has not read yet. The wallet of a transaction never changes, so reading it
// outside any lock answers the same identity the locked row carries.
type WaitCandidate struct {
	TransactionID identity.TransactionID
	WalletID      identity.WalletID
}

// Entries writes the ledger row, which is only ever inserted.
type Entries interface {
	Insert(ctx context.Context, entry ledger.Entry) error
}

// Outbox writes the event rows of the commit.
//
// Only the insert is here. The claim, the confirmation and the death of a row
// belong to no business transaction at all: the relay takes each of them in a
// short transaction of its own, so they are ports beside this one and not
// methods of it.
type Outbox interface {
	// Insert records the event in the transaction that decided it, so the
	// balance and the event live or die together. It publishes nothing: the
	// broker is called by whoever reads a row that is already committed.
	Insert(ctx context.Context, envelope event.Envelope) error
}

// WalletView is the read model of a wallet. A query answers it without
// rehydrating the aggregate, because a read moves no money.
type WalletView struct {
	ID        identity.WalletID
	PlayerID  identity.PlayerID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TransactionView is the read model of a wager transaction: the outcome as it
// was recorded.
//
// ObservedBalance keeps its zero value when the row carries none, which is what
// tells an absent balance from a balance of zero. FailureCode is set only on a
// transaction closed by a rule.
type TransactionView struct {
	ID              identity.TransactionID
	Kind            wager.Kind
	Status          wager.Status
	ProviderID      identity.ProviderID
	ExternalID      identity.ExternalTransactionID
	PlayerID        identity.PlayerID
	WalletID        identity.WalletID
	RoundID         identity.RoundID
	GameID          identity.GameID
	Amount          money.Money
	ObservedBalance money.Money
	FailureCode     wager.FailureCode
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Reads answers read models outside any transaction.
type Reads interface {
	Wallet(ctx context.Context, id identity.WalletID) (WalletView, error)

	// Transaction answers the recorded outcome of one transaction of that
	// provider. The provider is part of the query and not of a check afterwards,
	// so a transaction of another provider and one that does not exist leave by
	// the same path.
	Transaction(ctx context.Context, id identity.TransactionID, provider identity.ProviderID) (TransactionView, error)

	// TransactionByKey answers the transaction of that provider and key from
	// outside a transaction, which is what the loser of the unique constraint
	// needs: the violation aborts its SQL transaction, so the winning row can
	// only be read after the rollback.
	TransactionByKey(ctx context.Context, provider identity.ProviderID, key identity.IdempotencyKey) (wager.State, error)

	// DueWaits answers the waits whose scheduled instant has come, the earliest
	// schedule first, up to the limit asked.
	//
	// It takes no lock and opens no transaction: it only chooses candidates, so a
	// scan never stands between a submission and the wallet it moves. What the
	// decision uses is the row re-read under the lock, not this one.
	DueWaits(ctx context.Context, now time.Time, limit int) ([]WaitCandidate, error)
}
