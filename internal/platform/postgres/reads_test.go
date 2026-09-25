package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

func TestView_takesTheRowBackIntoDomainTypes(t *testing.T) {
	t.Parallel()
	view, err := rowOf().view()
	if err != nil {
		t.Fatalf("view = %v, want nil", err)
	}
	if view.Balance.Amount() != "1000.00" {
		t.Fatalf("balance = %s, want 1000.00", view.Balance.Amount())
	}
	if view.Balance.Currency().Code() != "BRL" {
		t.Fatalf("currency = %s, want BRL", view.Balance.Currency().Code())
	}
	if view.Version != 3 {
		t.Fatalf("version = %d, want 3", view.Version)
	}
	if view.ID.String() != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("wallet = %s, want the stored identity", view.ID)
	}
}

func TestView_refusesARowTheDomainCannotAccept(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mutil func(*walletRow)
	}{
		{name: "a wallet out of format is refused", mutil: func(r *walletRow) { r.id = "not-a-uuid" }},
		{name: "a player out of format is refused", mutil: func(r *walletRow) { r.playerID = "not-a-uuid" }},
		{name: "a currency outside ISO 4217 is refused", mutil: func(r *walletRow) { r.currency = "BRLL" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := rowOf()
			tc.mutil(&row)
			if _, err := row.view(); err == nil {
				t.Fatalf("view = nil, want an error for %s", tc.name)
			}
		})
	}
}

func TestWallet_refusesWhileTheSharedPoolIsClosed(t *testing.T) {
	t.Parallel()
	id, err := identity.ParseWalletID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	reads := NewReads(NewPool(config.Config{DatabaseURL: unreachable}))
	_, err = reads.Wallet(context.Background(), id)
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Wallet = %v, want %v", err, ErrPoolClosed)
	}
}

func rowOf() walletRow {
	at := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	return walletRow{
		id:        "11111111-1111-4111-8111-111111111111",
		playerID:  "22222222-2222-4222-8222-222222222222",
		currency:  "BRL",
		cents:     100000,
		version:   3,
		createdAt: at,
		updatedAt: at,
	}
}

// Every read asks the pool for a querier first, and a closed pool is the one
// refusal a read can answer without a database behind it.
func TestTransaction_refusesWhileTheSharedPoolIsClosed(t *testing.T) {
	t.Parallel()
	reads := NewReads(NewPool(config.Config{DatabaseURL: unreachable}))
	_, err := reads.Transaction(context.Background(), transactionIdentity(t), providerIdentity(t))
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Transaction = %v, want %v", err, ErrPoolClosed)
	}
}

func TestTransactionByKey_refusesWhileTheSharedPoolIsClosed(t *testing.T) {
	t.Parallel()
	reads := NewReads(NewPool(config.Config{DatabaseURL: unreachable}))
	_, err := reads.TransactionByKey(context.Background(), providerIdentity(t), keyIdentity(t))
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("TransactionByKey = %v, want %v", err, ErrPoolClosed)
	}
}

func transactionIdentity(t *testing.T) identity.TransactionID {
	t.Helper()
	parsed, err := identity.ParseTransactionID(rowTransaction)
	if err != nil {
		t.Fatalf("ParseTransactionID = %v, want nil", err)
	}
	return parsed
}

func providerIdentity(t *testing.T) identity.ProviderID {
	t.Helper()
	parsed, err := identity.ParseProviderID(rowProvider)
	if err != nil {
		t.Fatalf("ParseProviderID = %v, want nil", err)
	}
	return parsed
}

func keyIdentity(t *testing.T) identity.IdempotencyKey {
	t.Helper()
	parsed, err := identity.ParseIdempotencyKey("key-1")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey = %v, want nil", err)
	}
	return parsed
}
