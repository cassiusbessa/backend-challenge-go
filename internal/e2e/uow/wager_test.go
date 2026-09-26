//go:build integration

// The repository side of one wager operation against a real PostgreSQL: the lock
// of the wallet row, the version guard of the balance write, the lookup by key,
// the two unique indexes that decide idempotency, and the provider inside the
// query of a read.
package uow

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/platform/postgres"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

func TestGetForUpdate_answersTheLockedStateOfTheWallet(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	var state wallet.State
	err := unit.Within(ctx, func(tx storage.Tx) error {
		var err error
		state, err = tx.Wallets().GetForUpdate(ctx, opened.wallet.ID())
		return err
	})
	if err != nil {
		t.Fatalf("GetForUpdate = %v, want nil", err)
	}
	if state.Balance.Amount() != "1000.00" || state.Version != 1 {
		t.Fatalf("locked state = %s at version %d, want 1000.00 at version 1", state.Balance.Amount(), state.Version)
	}
	if state.PlayerID != opened.wallet.PlayerID() {
		t.Fatalf("player = %s, want %s", state.PlayerID, opened.wallet.PlayerID())
	}
}

func TestGetForUpdate_answersTheAbsenceOfTheWallet(t *testing.T) {
	ctx, _, unit := open(t)
	err := unit.Within(ctx, func(tx storage.Tx) error {
		_, err := tx.Wallets().GetForUpdate(ctx, walletOf(t, suiteenv.NewID()))
		return err
	})
	if !errors.Is(err, storage.ErrWalletNotFound) {
		t.Fatalf("GetForUpdate = %v, want %v", err, storage.ErrWalletNotFound)
	}
}

func TestUpdateBalance_writesTheMovedBalanceAtTheNextVersion(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	if err := debit(ctx, unit, t, opened.wallet.ID(), "250.00", noStale); err != nil {
		t.Fatalf("debit = %v, want nil", err)
	}
	assertWallet(ctx, t, opened.wallet.ID().String(), 75000, 2)
	assertLastEntry(ctx, t, opened.wallet.ID().String(), 2, 75000)
}

// A version that is no longer the stored one is a write that went past the lock.
// It is transient failure and not rejection: the row stays where it was, the
// border answers unavailability, and there is no token for the provider to read.
func TestUpdateBalance_refusesAStaleVersionAsATransientFailure(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	err := debit(ctx, unit, t, opened.wallet.ID(), "250.00", stale)
	if !errors.Is(err, storage.ErrLostWrite) {
		t.Fatalf("debit with a stale version = %v, want %v", err, storage.ErrLostWrite)
	}
	details := problem.From(err)
	if details.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", details.Status)
	}
	if details.FailureCode != "" {
		t.Fatalf("failureCode = %s, want empty: no rule refused the operation", details.FailureCode)
	}
	assertWallet(ctx, t, opened.wallet.ID().String(), 100000, 1)
	assertRows(ctx, t, opened.wallet.ID().String(), 1, 1, 1)
}

func TestByKey_answersTheTransactionOfThatProviderAndKey(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	recorded := rejection(t, opened.wallet, "key-"+suiteenv.NewID(), "external-"+suiteenv.NewID())
	insert(ctx, t, unit, recorded)
	state := byKey(ctx, t, unit, recorded.ProviderID(), recorded.IdempotencyKey())
	if state.ID != recorded.ID() || state.BodyHash != recorded.BodyHash() {
		t.Fatalf("read back %s with hash %s, want %s with %s", state.ID, state.BodyHash, recorded.ID(), recorded.BodyHash())
	}
	if state.Status != wager.Rejected || state.FailureCode != wager.InsufficientFunds {
		t.Fatalf("read back %s with %s, want REJECTED with INSUFFICIENT_FUNDS", state.Status, state.FailureCode)
	}
}

// The key is scoped to the provider: the same key from another provider is
// another operation, and the lookup must not find it.
func TestByKey_answersTheAbsenceForAnotherProvider(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	key := "key-" + suiteenv.NewID()
	insert(ctx, t, unit, rejection(t, opened.wallet, key, "external-"+suiteenv.NewID()))
	err := unit.Within(ctx, func(tx storage.Tx) error {
		_, err := tx.Transactions().ByKey(ctx, providerOf(t, "provider-b"), keyOf(t, key))
		return err
	})
	if !errors.Is(err, storage.ErrTransactionNotFound) {
		t.Fatalf("ByKey of another provider = %v, want %v", err, storage.ErrTransactionNotFound)
	}
}

func TestInsert_refusesASecondTransactionUnderTheSameKey(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	key := "key-" + suiteenv.NewID()
	insert(ctx, t, unit, rejection(t, opened.wallet, key, "external-"+suiteenv.NewID()))
	second := rejection(t, opened.wallet, key, "external-"+suiteenv.NewID())
	err := unit.Within(ctx, func(tx storage.Tx) error { return tx.Transactions().Insert(ctx, second) })
	assertRejected(t, err, wager.IdempotencyConflict)
	assertCountOf(ctx, t, "SELECT count(*) FROM wager_transactions WHERE idempotency_key = $1", key, 1)
}

func TestInsert_refusesTheSameExternalTransactionUnderAnotherKey(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	external := "external-" + suiteenv.NewID()
	insert(ctx, t, unit, rejection(t, opened.wallet, "key-"+suiteenv.NewID(), external))
	second := rejection(t, opened.wallet, "key-"+suiteenv.NewID(), external)
	err := unit.Within(ctx, func(tx storage.Tx) error { return tx.Transactions().Insert(ctx, second) })
	assertRejected(t, err, wager.DuplicateExternalTransaction)
	assertCountOf(ctx, t, "SELECT count(*) FROM wager_transactions WHERE external_id = $1", external, 1)
}

// A transaction of another provider and one that does not exist leave the query
// by the same path, so the answer cannot tell the reader that the record exists.
func TestTransaction_answersTheSameAbsenceForAnotherProviderAndForNothing(t *testing.T) {
	ctx, pool, unit := open(t)
	opened := stored(ctx, t, unit)
	recorded := rejection(t, opened.wallet, "key-"+suiteenv.NewID(), "external-"+suiteenv.NewID())
	insert(ctx, t, unit, recorded)
	reads := postgres.NewReads(pool)
	found, err := reads.Transaction(ctx, recorded.ID(), recorded.ProviderID())
	if err != nil {
		t.Fatalf("Transaction of the owner = %v, want nil", err)
	}
	if found.Status != wager.Rejected || found.FailureCode != wager.InsufficientFunds {
		t.Fatalf("read = %s with %s, want REJECTED with INSUFFICIENT_FUNDS", found.Status, found.FailureCode)
	}
	_, alien := reads.Transaction(ctx, recorded.ID(), providerOf(t, "provider-b"))
	_, absent := reads.Transaction(ctx, transactionOf(t, suiteenv.NewID()), recorded.ProviderID())
	if !errors.Is(alien, storage.ErrTransactionNotFound) || !errors.Is(absent, storage.ErrTransactionNotFound) {
		t.Fatalf("alien = %v and absent = %v, want both %v", alien, absent, storage.ErrTransactionNotFound)
	}
}

func TestTransactionByKey_readsTheWinningRowOutsideAnyTransaction(t *testing.T) {
	ctx, pool, unit := open(t)
	opened := stored(ctx, t, unit)
	key := "key-" + suiteenv.NewID()
	recorded := rejection(t, opened.wallet, key, "external-"+suiteenv.NewID())
	insert(ctx, t, unit, recorded)
	state, err := postgres.NewReads(pool).TransactionByKey(ctx, recorded.ProviderID(), keyOf(t, key))
	if err != nil {
		t.Fatalf("TransactionByKey = %v, want nil", err)
	}
	if state.ID != recorded.ID() {
		t.Fatalf("read back %s, want %s", state.ID, recorded.ID())
	}
}

// stored writes one opening and answers it, so every case below starts from a
// wallet of 1000.00 at version 1.
func stored(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork) set {
	t.Helper()
	opened := opening(t)
	if err := record(ctx, unit, opened); err != nil {
		t.Fatalf("record opening = %v, want nil", err)
	}
	return opened
}

// version says which version the balance write is conditioned on: the one read
// under the lock, or the one before it, which no row carries any more.
type version bool

const (
	noStale version = false
	stale   version = true
)

// debit runs the whole protocol of one bet in a single commit: lock, decide,
// write the balance against the version read, then the transaction and the entry.
func debit(ctx context.Context, unit *postgres.UnitOfWork, t *testing.T, id identity.WalletID, amount string, condition version) error {
	t.Helper()
	return unit.Within(ctx, func(tx storage.Tx) error {
		state, err := tx.Wallets().GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		moved, op, entry := bet(t, state, amount)
		readVersion := state.Version
		if condition == stale {
			readVersion--
		}
		if err := tx.Wallets().UpdateBalance(ctx, moved, readVersion); err != nil {
			return err
		}
		if err := tx.Transactions().Insert(ctx, op); err != nil {
			return err
		}
		return tx.Entries().Insert(ctx, entry)
	})
}

func bet(t *testing.T, state wallet.State, amount string) (*wallet.Wallet, *wager.Transaction, ledger.Entry) {
	t.Helper()
	rehydrated, err := wallet.Rehydrate(state)
	if err != nil {
		t.Fatalf("wallet.Rehydrate = %v, want nil", err)
	}
	op := operation(t, rehydrated, "key-"+suiteenv.NewID(), "external-"+suiteenv.NewID(), amount)
	decision, err := wager.Bet(rehydrated, op, wager.Movement{EntryID: entryOf(t, suiteenv.NewID()), At: stamp()})
	if err != nil {
		t.Fatalf("wager.Bet = %v, want nil", err)
	}
	entry, moved := decision.Entry()
	if !moved {
		t.Fatalf("a bet of %s produced no entry, want the debit", amount)
	}
	if err := op.Process(decision.Balance(), stamp()); err != nil {
		t.Fatalf("Process = %v, want nil", err)
	}
	return rehydrated, op, entry
}

// operation builds one provider operation over that wallet.
func operation(t *testing.T, owner *wallet.Wallet, key, external, amount string) *wager.Transaction {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	op, err := wager.NewExternal(wager.ExternalSpec{
		ID:             transactionOf(t, suiteenv.NewID()),
		ProviderID:     providerOf(t, "provider-a"),
		ExternalID:     externalOf(t, external),
		IdempotencyKey: keyOf(t, key),
		BodyHash:       "hash-" + key,
		PlayerID:       owner.PlayerID(),
		WalletID:       owner.ID(),
		RoundID:        roundOf(t, "round-1"),
		GameID:         gameOf(t, "game-1"),
		Kind:           wager.KindBet,
		Amount:         parsed,
		At:             stamp(),
	})
	if err != nil {
		t.Fatalf("wager.NewExternal = %v, want nil", err)
	}
	return op
}

// rejection is one operation already closed by a rule: a REJECTED row carries no
// entry and moves no balance, which is what lets a case about the unique indexes
// stay clear of the balance trigger.
func rejection(t *testing.T, owner *wallet.Wallet, key, external string) *wager.Transaction {
	t.Helper()
	op := operation(t, owner, key, external, "25.00")
	if err := op.Reject(wager.InsufficientFunds, stamp()); err != nil {
		t.Fatalf("Reject = %v, want nil", err)
	}
	return op
}

func insert(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, op *wager.Transaction) {
	t.Helper()
	err := unit.Within(ctx, func(tx storage.Tx) error { return tx.Transactions().Insert(ctx, op) })
	if err != nil {
		t.Fatalf("insert transaction = %v, want nil", err)
	}
}

func byKey(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, provider identity.ProviderID, key identity.IdempotencyKey) wager.State {
	t.Helper()
	var state wager.State
	err := unit.Within(ctx, func(tx storage.Tx) error {
		var err error
		state, err = tx.Transactions().ByKey(ctx, provider, key)
		return err
	})
	if err != nil {
		t.Fatalf("ByKey = %v, want nil", err)
	}
	return state
}

func assertRejected(t *testing.T, err error, want wager.FailureCode) {
	t.Helper()
	var rejected wager.Rejection
	if !errors.As(err, &rejected) {
		t.Fatalf("insert = %v, want a business rejection", err)
	}
	if rejected.Code() != want {
		t.Fatalf("failureCode = %s, want %s", rejected.Code(), want)
	}
	if got := problem.From(err).Status; got != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", got)
	}
}

func assertWallet(ctx context.Context, t *testing.T, walletID string, cents, version int64) {
	t.Helper()
	var storedCents, storedVersion int64
	err := connect(ctx, t).QueryRow(ctx, "SELECT balance_cents, version FROM wallets WHERE id = $1", walletID).
		Scan(&storedCents, &storedVersion)
	if err != nil {
		t.Fatalf("read wallet = %v, want nil", err)
	}
	if storedCents != cents || storedVersion != version {
		t.Fatalf("wallet = %d cents at version %d, want %d at version %d", storedCents, storedVersion, cents, version)
	}
}

func assertLastEntry(ctx context.Context, t *testing.T, walletID string, sequence, after int64) {
	t.Helper()
	var storedSequence, storedAfter int64
	err := connect(ctx, t).QueryRow(ctx,
		"SELECT sequence_number, balance_after_cents FROM ledger_entries WHERE wallet_id = $1 ORDER BY sequence_number DESC LIMIT 1",
		walletID).Scan(&storedSequence, &storedAfter)
	if err != nil {
		t.Fatalf("read entry = %v, want nil", err)
	}
	if storedSequence != sequence || storedAfter != after {
		t.Fatalf("last entry = sequence %d leaving %d, want sequence %d leaving %d", storedSequence, storedAfter, sequence, after)
	}
}

func assertCountOf(ctx context.Context, t *testing.T, query, argument string, want int64) {
	t.Helper()
	var total int64
	if err := connect(ctx, t).QueryRow(ctx, query, argument).Scan(&total); err != nil {
		t.Fatalf("count = %v, want nil", err)
	}
	if total != want {
		t.Fatalf("rows = %d, want %d", total, want)
	}
}

func stamp() time.Time {
	return time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
}

func providerOf(t *testing.T, text string) identity.ProviderID {
	t.Helper()
	id, err := identity.ParseProviderID(text)
	if err != nil {
		t.Fatalf("ParseProviderID = %v, want nil", err)
	}
	return id
}

func externalOf(t *testing.T, text string) identity.ExternalTransactionID {
	t.Helper()
	id, err := identity.ParseExternalTransactionID(text)
	if err != nil {
		t.Fatalf("ParseExternalTransactionID = %v, want nil", err)
	}
	return id
}

func keyOf(t *testing.T, text string) identity.IdempotencyKey {
	t.Helper()
	id, err := identity.ParseIdempotencyKey(text)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey = %v, want nil", err)
	}
	return id
}

func roundOf(t *testing.T, text string) identity.RoundID {
	t.Helper()
	id, err := identity.ParseRoundID(text)
	if err != nil {
		t.Fatalf("ParseRoundID = %v, want nil", err)
	}
	return id
}

func gameOf(t *testing.T, text string) identity.GameID {
	t.Helper()
	id, err := identity.ParseGameID(text)
	if err != nil {
		t.Fatalf("ParseGameID = %v, want nil", err)
	}
	return id
}
