package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// The identity is the one the caller asked for under the lock, so the column is
// not read back and the row carries the rest of the state.
func TestState_rebuildsTheWalletStateFromTheLockedRow(t *testing.T) {
	t.Parallel()
	asked := walletIdentity(t)
	state, err := lockedRow().state(asked)
	if err != nil {
		t.Fatalf("state of the locked row = %v, want nil", err)
	}
	if state.ID != asked {
		t.Fatalf("identity = %s, want the %s the caller asked for", state.ID, asked)
	}
	if state.Balance.Amount() != "75.00" {
		t.Fatalf("balance = %s, want 75.00 from the stored cents", state.Balance.Amount())
	}
	if state.Version != 3 {
		t.Fatalf("version = %d, want the 3 stored on the row", state.Version)
	}
}

func TestState_refusesALockedRowWhoseOwnerIsNotACanonicalUUID(t *testing.T) {
	t.Parallel()
	row := lockedRow()
	row.playerID = "not-a-uuid"
	state, err := row.state(walletIdentity(t))
	assertRefusedRow(t, err, state, "read player identity")
}

func TestState_refusesALockedRowWhoseCurrencyIsOutsideTheVocabulary(t *testing.T) {
	t.Parallel()
	row := lockedRow()
	row.currency = "BRLL"
	state, err := row.state(walletIdentity(t))
	assertRefusedRow(t, err, state, "read wallet balance")
}

func assertRefusedRow(t *testing.T, err error, state wallet.State, operation string) {
	t.Helper()
	if err == nil {
		t.Fatalf("state of a row the domain cannot parse = %+v with no error, want a refusal", state)
	}
	if !strings.Contains(err.Error(), operation) {
		t.Fatalf("refusal = %q, want %q named in the chain", err.Error(), operation)
	}
	if state != (wallet.State{}) {
		t.Fatalf("state beside the refusal = %+v, want the zero value", state)
	}
}

// lockedWallet is one wallet row as PostgreSQL hands it over under the lock.
func lockedRow() lockedWallet {
	at := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	return lockedWallet{
		playerID:  rowPlayer,
		currency:  "BRL",
		cents:     7500,
		version:   3,
		createdAt: at,
		updatedAt: at,
	}
}

func walletIdentity(t *testing.T) identity.WalletID {
	t.Helper()
	parsed, err := identity.ParseWalletID(rowWallet)
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	return parsed
}

// The lock answers the absence before any row of the operation exists, so the
// caller learns the wallet is not there instead of learning it from a foreign key.
func TestGetForUpdate_answersTheAbsenceWithTheOperationThatTookTheLock(t *testing.T) {
	t.Parallel()
	repository := wallets{tx: stubTx{row: refusingRow{}}}
	state, err := repository.GetForUpdate(context.Background(), walletIdentity(t))
	assertAbsence(t, err, "lock wallet", storage.ErrWalletNotFound)
	if state != (wallet.State{}) {
		t.Fatalf("state beside the absence = %+v, want the zero value", state)
	}
}

func TestGetForUpdate_leavesAnythingElseAsInfrastructure(t *testing.T) {
	t.Parallel()
	repository := wallets{tx: stubTx{row: scanError{err: errors.New("connection reset by peer")}}}
	_, err := repository.GetForUpdate(context.Background(), walletIdentity(t))
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a reset connection under the lock = %v, want it apart from an absence", err)
	}
}

// Zero rows affected is a write that went past the lock: the version read is no
// longer the stored one. It is transient infrastructure and carries no token,
// because no rule refused anything.
func TestUpdateBalance_readsZeroRowsAsAWriteThatWentPastTheLock(t *testing.T) {
	t.Parallel()
	repository := wallets{tx: stubTx{tag: pgconn.NewCommandTag("UPDATE 0")}}
	err := repository.UpdateBalance(context.Background(), movedWallet(t), 3)
	if !errors.Is(err, storage.ErrLostWrite) {
		t.Fatalf("UpdateBalance with no row matched = %v, want %v", err, storage.ErrLostWrite)
	}
	var rejection wager.Rejection
	if errors.As(err, &rejection) {
		t.Fatalf("UpdateBalance with no row matched = %v, want no business rejection", err)
	}
}

func TestUpdateBalance_answersNilWhenTheVersionStillMatched(t *testing.T) {
	t.Parallel()
	repository := wallets{tx: stubTx{tag: pgconn.NewCommandTag("UPDATE 1")}}
	if err := repository.UpdateBalance(context.Background(), movedWallet(t), 3); err != nil {
		t.Fatalf("UpdateBalance of the row it matched = %v, want nil", err)
	}
}

func TestUpdateBalance_namesTheOperationOfAFailedWrite(t *testing.T) {
	t.Parallel()
	repository := wallets{tx: stubTx{err: errors.New("connection reset by peer")}}
	err := repository.UpdateBalance(context.Background(), movedWallet(t), 3)
	if errors.Is(err, storage.ErrLostWrite) {
		t.Fatalf("a reset connection = %v, want it apart from a lost write", err)
	}
	if !strings.Contains(err.Error(), "update wallet balance") {
		t.Fatalf("message = %q, want the operation named in the chain", err.Error())
	}
}

// movedWallet is a wallet that already moved, which is the only state that reaches
// UpdateBalance: an operation that moves nothing never comes through here.
func movedWallet(t *testing.T) *wallet.Wallet {
	t.Helper()
	state, err := lockedRow().state(walletIdentity(t))
	if err != nil {
		t.Fatalf("state built for the moved wallet = %v, want nil", err)
	}
	moved, err := wallet.Rehydrate(state)
	if err != nil {
		t.Fatalf("Rehydrate of the locked state = %v, want nil", err)
	}
	return moved
}
