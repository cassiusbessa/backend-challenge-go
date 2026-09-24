package readwallet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func TestWallet_answersTheStoredBalanceAndVersion(t *testing.T) {
	t.Parallel()
	stored := viewOf(t, "1000.00", 3)
	found, err := New(&rows{view: stored}).Wallet(context.Background(), stored.ID)
	if err != nil {
		t.Fatalf("Wallet = %v, want nil", err)
	}
	if found.Balance.Amount() != "1000.00" {
		t.Fatalf("balance = %s, want 1000.00", found.Balance.Amount())
	}
	if found.Version != 3 {
		t.Fatalf("version = %d, want 3", found.Version)
	}
}

func TestWallet_passesTheAbsenceThrough(t *testing.T) {
	t.Parallel()
	_, err := New(&rows{err: storage.ErrWalletNotFound}).Wallet(context.Background(), walletOf(t))
	if !errors.Is(err, storage.ErrWalletNotFound) {
		t.Fatalf("Wallet = %v, want %v", err, storage.ErrWalletNotFound)
	}
}

func TestWallet_asksTheReadModelForTheIdentityInTheURL(t *testing.T) {
	t.Parallel()
	asked := &rows{view: viewOf(t, "0.00", 1)}
	wanted := walletOf(t)
	if _, err := New(asked).Wallet(context.Background(), wanted); err != nil {
		t.Fatalf("Wallet = %v, want nil", err)
	}
	if asked.asked != wanted {
		t.Fatalf("asked for = %s, want %s", asked.asked, wanted)
	}
}

type rows struct {
	view  storage.WalletView
	err   error
	asked identity.WalletID
}

func (r *rows) Wallet(_ context.Context, id identity.WalletID) (storage.WalletView, error) {
	r.asked = id
	if r.err != nil {
		return storage.WalletView{}, r.err
	}
	return r.view, nil
}

func walletOf(t *testing.T) identity.WalletID {
	t.Helper()
	id, err := identity.ParseWalletID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	return id
}

func viewOf(t *testing.T, amount string, version int64) storage.WalletView {
	t.Helper()
	balance, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	playerID, err := identity.ParsePlayerID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParsePlayerID = %v, want nil", err)
	}
	at := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	return storage.WalletView{
		ID:        walletOf(t),
		PlayerID:  playerID,
		Balance:   balance,
		Version:   version,
		CreatedAt: at,
		UpdatedAt: at,
	}
}
