//go:build integration

// The suite drives the real transactional boundary against a real PostgreSQL:
// what one commit writes together, and what a failure halfway leaves behind.
package uow

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/postgres"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

func TestWithin_writesWalletTransactionAndEntryInTheSameCommit(t *testing.T) {
	ctx, _, unit := open(t)
	opened := opening(t)
	if err := record(ctx, unit, opened); err != nil {
		t.Fatalf("Within = %v, want nil", err)
	}
	assertRows(ctx, t, opened.wallet.ID().String(), 1, 1, 1)
}

func TestWithin_leavesNothingBehindWhenTheEntryFails(t *testing.T) {
	ctx, _, unit := open(t)
	opened := opening(t)
	broken := errors.New("insert ledger entry: interrupted")
	err := unit.Within(ctx, func(tx storage.Tx) error {
		if err := tx.Wallets().Insert(ctx, opened.wallet); err != nil {
			return err
		}
		if err := tx.Transactions().Insert(ctx, opened.transaction); err != nil {
			return err
		}
		return broken
	})
	if !errors.Is(err, broken) {
		t.Fatalf("Within = %v, want %v", err, broken)
	}
	assertRows(ctx, t, opened.wallet.ID().String(), 0, 0, 0)
}

func TestWithin_refusesTheSecondWalletOfThePlayerAsAContractRefusal(t *testing.T) {
	ctx, _, unit := open(t)
	first := opening(t)
	if err := record(ctx, unit, first); err != nil {
		t.Fatalf("first opening = %v, want nil", err)
	}
	second := openingFor(t, first.wallet.PlayerID())
	err := record(ctx, unit, second)
	if !errors.Is(err, storage.ErrWalletExists) {
		t.Fatalf("second opening = %v, want %v", err, storage.ErrWalletExists)
	}
	if got := problem.From(err).Status; got != http.StatusConflict {
		t.Fatalf("status = %d, want 409", got)
	}
	assertRows(ctx, t, second.wallet.ID().String(), 0, 0, 0)
	assertRows(ctx, t, first.wallet.ID().String(), 1, 1, 1)
}

// A connection that is gone mid operation is a transient failure, not a
// rejection: it rolls the SQL transaction back and reaches the border as
// unavailability, with no token.
func TestWithin_answersUnavailabilityWhenTheConnectionIsGone(t *testing.T) {
	ctx, pool, unit := open(t)
	opened := opening(t)
	if err := record(ctx, unit, opened); err != nil {
		t.Fatalf("Within = %v, want nil", err)
	}
	gone := opening(t)
	if err := pool.Close(ctx); err != nil {
		t.Fatalf("close pool = %v, want nil", err)
	}
	err := record(ctx, unit, gone)
	if err == nil {
		t.Fatalf("Within on a closed pool = nil, want a failure")
	}
	details := problem.From(err)
	if details.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", details.Status)
	}
	if details.FailureCode != "" {
		t.Fatalf("failureCode = %s, want empty for unavailability", details.FailureCode)
	}
	assertRows(ctx, t, gone.wallet.ID().String(), 0, 0, 0)
}

// set is one opening, already built by the domain, waiting to be written.
type set struct {
	wallet      *wallet.Wallet
	transaction *wager.Transaction
	entry       ledger.Entry
}

func record(ctx context.Context, unit *postgres.UnitOfWork, opened set) error {
	return unit.Within(ctx, func(tx storage.Tx) error {
		if err := tx.Wallets().Insert(ctx, opened.wallet); err != nil {
			return err
		}
		if err := tx.Transactions().Insert(ctx, opened.transaction); err != nil {
			return err
		}
		return tx.Entries().Insert(ctx, opened.entry)
	})
}

func opening(t *testing.T) set {
	t.Helper()
	return openingFor(t, playerOf(t, newID()))
}

func openingFor(t *testing.T, player identity.PlayerID) set {
	t.Helper()
	balance, err := money.Parse("1000.00", "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	at := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	opened, movement, err := wallet.Open(wallet.OpenSpec{
		ID:             walletOf(t, newID()),
		PlayerID:       player,
		InitialBalance: balance,
		EntryID:        entryOf(t, newID()),
		TransactionID:  transactionOf(t, newID()),
		At:             at,
	})
	if err != nil {
		t.Fatalf("wallet.Open = %v, want nil", err)
	}
	entry, moved := movement.Entry()
	if !moved {
		t.Fatalf("opening at 1000.00 produced no entry, want the credit of the birth")
	}
	return set{wallet: opened, transaction: processed(t, opened, entry, at), entry: entry}
}

func processed(t *testing.T, opened *wallet.Wallet, entry ledger.Entry, at time.Time) *wager.Transaction {
	t.Helper()
	recorded, err := wager.NewOpening(wager.OpeningSpec{
		ID:       entry.TransactionID(),
		PlayerID: opened.PlayerID(),
		WalletID: opened.ID(),
		Amount:   entry.Amount(),
		At:       at,
	})
	if err != nil {
		t.Fatalf("wager.NewOpening = %v, want nil", err)
	}
	if err := recorded.Process(entry.BalanceAfter(), at); err != nil {
		t.Fatalf("Process = %v, want nil", err)
	}
	return recorded
}

func open(t *testing.T) (context.Context, *postgres.Pool, *postgres.UnitOfWork) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool := postgres.NewPool(config.Config{DatabaseURL: databaseURL()})
	if err := pool.Open(ctx); err != nil {
		t.Fatalf("open pool = %v, want nil", err)
	}
	t.Cleanup(func() { _ = pool.Close(context.Background()) })
	return ctx, pool, postgres.NewUnitOfWork(pool)
}

func assertRows(ctx context.Context, t *testing.T, walletID string, wallets, transactions, entries int64) {
	t.Helper()
	conn := connect(ctx, t)
	assertCount(ctx, t, conn, "wallets", "SELECT count(*) FROM wallets WHERE id = $1", walletID, wallets)
	assertCount(ctx, t, conn, "transactions", "SELECT count(*) FROM wager_transactions WHERE wallet_id = $1", walletID, transactions)
	assertCount(ctx, t, conn, "entries", "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1", walletID, entries)
}

func assertCount(ctx context.Context, t *testing.T, conn *pgx.Conn, table, query, walletID string, want int64) {
	t.Helper()
	var total int64
	if err := conn.QueryRow(ctx, query, walletID).Scan(&total); err != nil {
		t.Fatalf("count %s = %v, want nil", table, err)
	}
	if total != want {
		t.Fatalf("%s = %d, want %d", table, total, want)
	}
}

func connect(ctx context.Context, t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, databaseURL())
	if err != nil {
		t.Fatalf("connect = %v, want nil: the suite needs the migration applied", err)
	}
	// The cleanup runs after the test context is cancelled, so the close gets a
	// context that carries its values without its deadline.
	closing := context.WithoutCancel(ctx)
	t.Cleanup(func() { _ = conn.Close(closing) })
	return conn
}

// suiteDatabaseURL is where this suite lands when DATABASE_URL is unset. It is
// never the database the running application uses: the outbox relay of that
// process scans the whole table every second and publishes the row a case here
// expects to see dead.
const suiteDatabaseURL = "postgres://junglegaming:junglegaming@localhost:5432/junglegaming_test?sslmode=disable"

func databaseURL() string {
	if value := os.Getenv("DATABASE_URL"); value != "" {
		return value
	}
	return suiteDatabaseURL
}

func newID() string {
	return uuid.NewV7().String()
}

func walletOf(t *testing.T, text string) identity.WalletID {
	t.Helper()
	id, err := identity.ParseWalletID(text)
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	return id
}

func playerOf(t *testing.T, text string) identity.PlayerID {
	t.Helper()
	id, err := identity.ParsePlayerID(text)
	if err != nil {
		t.Fatalf("ParsePlayerID = %v, want nil", err)
	}
	return id
}

func transactionOf(t *testing.T, text string) identity.TransactionID {
	t.Helper()
	id, err := identity.ParseTransactionID(text)
	if err != nil {
		t.Fatalf("ParseTransactionID = %v, want nil", err)
	}
	return id
}

func entryOf(t *testing.T, text string) identity.LedgerEntryID {
	t.Helper()
	id, err := identity.ParseLedgerEntryID(text)
	if err != nil {
		t.Fatalf("ParseLedgerEntryID = %v, want nil", err)
	}
	return id
}
