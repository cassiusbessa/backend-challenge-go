package readwager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestTransaction_answersTheRecordedOutcome(t *testing.T) {
	t.Parallel()
	stored := viewOf(t, wager.Processed, "975.00")
	found, err := New(&rows{view: stored}).Transaction(context.Background(), stored.ID, stored.ProviderID)
	if err != nil {
		t.Fatalf("Transaction of the recorded outcome = %v, want nil", err)
	}
	if found.Status != wager.Processed {
		t.Fatalf("status = %s, want PROCESSED", found.Status)
	}
	if found.ObservedBalance.Amount() != "975.00" {
		t.Fatalf("observed balance = %s, want 975.00", found.ObservedBalance.Amount())
	}
}

func TestTransaction_answersTheTokenOfARejectedOutcome(t *testing.T) {
	t.Parallel()
	stored := viewOf(t, wager.Rejected, "")
	stored.FailureCode = wager.InsufficientFunds
	found, err := New(&rows{view: stored}).Transaction(context.Background(), stored.ID, stored.ProviderID)
	if err != nil {
		t.Fatalf("Transaction of a rejected outcome = %v, want nil", err)
	}
	if found.FailureCode != wager.InsufficientFunds {
		t.Fatalf("failureCode = %s, want INSUFFICIENT_FUNDS", found.FailureCode)
	}
}

// A transaction of another provider and one that does not exist answer the same
// sentinel, so the border has no way to tell the reader that the record exists.
func TestTransaction_answersTheSameAbsenceForAnotherProviderAndForNothing(t *testing.T) {
	t.Parallel()
	service := New(&rows{err: storage.ErrTransactionNotFound})
	_, alien := service.Transaction(context.Background(), transactionOf(t), providerOf(t, "provider-b"))
	_, absent := service.Transaction(context.Background(), transactionOf(t), providerOf(t, "provider-a"))
	if !errors.Is(alien, storage.ErrTransactionNotFound) {
		t.Fatalf("transaction of another provider = %v, want %v", alien, storage.ErrTransactionNotFound)
	}
	if !errors.Is(absent, storage.ErrTransactionNotFound) {
		t.Fatalf("transaction that does not exist = %v, want %v", absent, storage.ErrTransactionNotFound)
	}
	if alien.Error() != absent.Error() {
		t.Fatalf("messages = %q and %q, want them the same", alien.Error(), absent.Error())
	}
}

func TestTransaction_asksTheReadModelForTheIdentityAndTheProvider(t *testing.T) {
	t.Parallel()
	asked := &rows{view: viewOf(t, wager.Processed, "975.00")}
	wanted := transactionOf(t)
	provider := providerOf(t, "provider-a")
	if _, err := New(asked).Transaction(context.Background(), wanted, provider); err != nil {
		t.Fatalf("Transaction asked with the identity and the provider = %v, want nil", err)
	}
	if asked.id != wanted || asked.provider != provider {
		t.Fatalf("asked for %s of %s, want %s of %s", asked.id, asked.provider, wanted, provider)
	}
}

// rows is the read port in memory. Only the transaction read belongs to this use
// case: the two others are part of the same port and are never reached from here.
type rows struct {
	view     storage.TransactionView
	err      error
	id       identity.TransactionID
	provider identity.ProviderID
}

func (r *rows) Transaction(_ context.Context, id identity.TransactionID, provider identity.ProviderID) (storage.TransactionView, error) {
	r.id = id
	r.provider = provider
	if r.err != nil {
		return storage.TransactionView{}, r.err
	}
	return r.view, nil
}

func (r *rows) Wallet(context.Context, identity.WalletID) (storage.WalletView, error) {
	return storage.WalletView{}, storage.ErrWalletNotFound
}

// The queue of the waits is part of the same port and is never reached from a
// read of one transaction.
func (r *rows) DueWaits(context.Context, time.Time, int) ([]storage.WaitCandidate, error) {
	return nil, nil
}

func (r *rows) OldestWait(context.Context, time.Time) (time.Duration, error) {
	return 0, nil
}

func (r *rows) WalletIDsAfter(context.Context, identity.WalletID, int) ([]identity.WalletID, error) {
	return nil, nil
}

func (r *rows) TransactionByKey(context.Context, identity.ProviderID, identity.IdempotencyKey) (wager.State, error) {
	return wager.State{}, storage.ErrTransactionNotFound
}

// The two ledger reads are part of the same port and are never reached from a
// read of one transaction.
func (r *rows) Ledger(context.Context, identity.WalletID, storage.EntryPosition, int) ([]storage.EntryView, error) {
	return nil, storage.ErrWalletNotFound
}

func (r *rows) Summary(context.Context, identity.WalletID) (storage.LedgerSummary, error) {
	return storage.LedgerSummary{}, storage.ErrWalletNotFound
}

func viewOf(t *testing.T, status wager.Status, observed string) storage.TransactionView {
	t.Helper()
	view := storage.TransactionView{
		ID:         transactionOf(t),
		Kind:       wager.KindBet,
		Status:     status,
		ProviderID: providerOf(t, "provider-a"),
		Amount:     moneyOf(t, "25.00"),
	}
	if observed != "" {
		view.ObservedBalance = moneyOf(t, observed)
	}
	return view
}

func moneyOf(t *testing.T, amount string) money.Money {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	return parsed
}

func transactionOf(t *testing.T) identity.TransactionID {
	t.Helper()
	id, err := identity.ParseTransactionID("33333333-3333-4333-8333-333333333333")
	if err != nil {
		t.Fatalf("ParseTransactionID = %v, want nil", err)
	}
	return id
}

func providerOf(t *testing.T, text string) identity.ProviderID {
	t.Helper()
	id, err := identity.ParseProviderID(text)
	if err != nil {
		t.Fatalf("ParseProviderID = %v, want nil", err)
	}
	return id
}
