package openwallet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

func TestOpen_recordsWalletOpeningAndEntryInOneCommit(t *testing.T) {
	t.Parallel()
	book := &book{}
	opened := open(t, book, "1000.00")
	assertWallet(t, opened, "1000.00")
	if book.commits != 1 {
		t.Fatalf("commits = %d, want 1", book.commits)
	}
	assertRows(t, book, 1, 1)
}

func TestOpen_recordsTheOpeningAsProcessedWithTheObservedBalance(t *testing.T) {
	t.Parallel()
	book := &book{}
	open(t, book, "1000.00")
	opening := book.transactions[0]
	if opening.Kind() != wager.KindOpening {
		t.Fatalf("kind = %s, want OPENING", opening.Kind())
	}
	if opening.Status() != wager.Processed {
		t.Fatalf("status = %s, want PROCESSED", opening.Status())
	}
	if opening.ObservedBalance().Amount() != "1000.00" {
		t.Fatalf("observed balance = %s, want 1000.00", opening.ObservedBalance().Amount())
	}
}

func TestOpen_recordsTheCreditEntryOfTheOpening(t *testing.T) {
	t.Parallel()
	book := &book{}
	open(t, book, "1000.00")
	entry := book.entries[0]
	if entry.Direction() != ledger.Credit {
		t.Fatalf("direction = %s, want CREDIT", entry.Direction())
	}
	if entry.Amount().Amount() != "1000.00" {
		t.Fatalf("entry amount = %s, want 1000.00", entry.Amount().Amount())
	}
	if entry.TransactionID() != book.transactions[0].ID() {
		t.Fatalf("entry transaction = %s, want the opening %s", entry.TransactionID(), book.transactions[0].ID())
	}
}

func TestOpen_atZeroRecordsNeitherTransactionNorEntry(t *testing.T) {
	t.Parallel()
	book := &book{}
	opened := open(t, book, "0.00")
	assertWallet(t, opened, "0.00")
	assertRows(t, book, 0, 0)
}

func open(t *testing.T, ledgerBook *book, amount string) Result {
	t.Helper()
	opened, err := New(ledgerBook, fixedMinter(t), frozenClock{}).Open(context.Background(), commandOf(t, amount))
	if err != nil {
		t.Fatalf("Open in the helper = %v, want nil", err)
	}
	return opened
}

// assertWallet checks what the border answers: the wallet is born at version 1
// already holding the initial balance.
func assertWallet(t *testing.T, opened Result, amount string) {
	t.Helper()
	if opened.Balance.Amount() != amount {
		t.Fatalf("balance = %s, want %s", opened.Balance.Amount(), amount)
	}
	if opened.Version != 1 {
		t.Fatalf("version = %d, want 1", opened.Version)
	}
}

func assertRows(t *testing.T, ledgerBook *book, transactions, entries int) {
	t.Helper()
	if len(ledgerBook.wallets) != 1 {
		t.Fatalf("wallets = %d, want 1", len(ledgerBook.wallets))
	}
	if len(ledgerBook.transactions) != transactions {
		t.Fatalf("transactions = %d, want %d", len(ledgerBook.transactions), transactions)
	}
	if len(ledgerBook.entries) != entries {
		t.Fatalf("entries = %d, want %d", len(ledgerBook.entries), entries)
	}
}

func TestOpen_leavesNothingCommittedWhenTheEntryFails(t *testing.T) {
	t.Parallel()
	broken := errors.New("insert ledger entry: connection reset")
	book := &book{entryErr: broken}
	service := New(book, fixedMinter(t), frozenClock{})
	_, err := service.Open(context.Background(), commandOf(t, "1000.00"))
	if !errors.Is(err, broken) {
		t.Fatalf("Open with a failing entry = %v, want %v", err, broken)
	}
	if book.commits != 0 {
		t.Fatalf("commits = %d, want 0", book.commits)
	}
	if book.rollbacks != 1 {
		t.Fatalf("rollbacks = %d, want 1", book.rollbacks)
	}
}

func TestOpen_passesTheDuplicateWalletRefusalThrough(t *testing.T) {
	t.Parallel()
	book := &book{walletErr: storage.ErrWalletExists}
	service := New(book, fixedMinter(t), frozenClock{})
	_, err := service.Open(context.Background(), commandOf(t, "1000.00"))
	if !errors.Is(err, storage.ErrWalletExists) {
		t.Fatalf("Open of a duplicate wallet = %v, want %v", err, storage.ErrWalletExists)
	}
	if book.transactionAttempts != 0 {
		t.Fatalf("transaction writes attempted = %d, want none after the duplicate", book.transactionAttempts)
	}
}

func TestOpen_refusesWhenAnIdentityCannotBeMinted(t *testing.T) {
	t.Parallel()
	broken := errors.New("mint: entropy exhausted")
	book := &book{}
	service := New(book, brokenMinter{err: broken}, frozenClock{})
	_, err := service.Open(context.Background(), commandOf(t, "1000.00"))
	if !errors.Is(err, broken) {
		t.Fatalf("Open with an unmintable identity = %v, want %v", err, broken)
	}
	if book.commits != 0 {
		t.Fatalf("commits = %d, want 0 without an identity", book.commits)
	}
}

func TestOpen_refusesAnInitialBalanceBelowZero(t *testing.T) {
	t.Parallel()
	currency, err := money.ParseCurrency("BRL")
	if err != nil {
		t.Fatalf("ParseCurrency = %v, want nil", err)
	}
	negative, err := money.FromCents(-1, currency)
	if err != nil {
		t.Fatalf("FromCents = %v, want nil", err)
	}
	book := &book{}
	service := New(book, fixedMinter(t), frozenClock{})
	_, err = service.Open(context.Background(), Command{PlayerID: playerOf(t), InitialBalance: negative})
	if !errors.Is(err, wallet.ErrNegativeBalance) {
		t.Fatalf("Open with a balance below zero = %v, want %v", err, wallet.ErrNegativeBalance)
	}
	if book.commits != 0 {
		t.Fatalf("commits = %d, want 0 for a negative balance", book.commits)
	}
}

func TestOpen_stampsTheInjectedInstant(t *testing.T) {
	t.Parallel()
	book := &book{}
	service := New(book, fixedMinter(t), frozenClock{})
	if _, err := service.Open(context.Background(), commandOf(t, "1000.00")); err != nil {
		t.Fatalf("Open with the injected instant = %v, want nil", err)
	}
	if !book.wallets[0].CreatedAt().Equal(frozen) {
		t.Fatalf("created at = %s, want %s", book.wallets[0].CreatedAt(), frozen)
	}
	if !book.entries[0].CreatedAt().Equal(frozen) {
		t.Fatalf("entry created at = %s, want %s", book.entries[0].CreatedAt(), frozen)
	}
}

// book is the in-memory unit of work: it records what each repository was asked
// to write and whether the set was committed.
type book struct {
	wallets      []*wallet.Wallet
	transactions []*wager.Transaction
	entries      []ledger.Entry
	commits      int
	rollbacks    int
	walletErr    error
	entryErr     error
	// transactionAttempts survives the rollback, so a test can tell a row that
	// was written and undone from one that was never attempted.
	transactionAttempts int
}

func (b *book) Within(_ context.Context, work func(storage.Tx) error) error {
	if err := work(b); err != nil {
		b.rollbacks++
		b.wallets = nil
		b.transactions = nil
		b.entries = nil
		return err
	}
	b.commits++
	return nil
}

// Each repository is its own small type, because one type cannot carry three
// methods named Insert.
func (b *book) Wallets() storage.Wallets {
	return walletRows{book: b}
}

func (b *book) Transactions() storage.Transactions {
	return transactionRows{book: b}
}

func (b *book) Entries() storage.Entries {
	return entryRows{book: b}
}

type walletRows struct {
	book *book
}

func (r walletRows) Insert(_ context.Context, opened *wallet.Wallet) error {
	if r.book.walletErr != nil {
		return r.book.walletErr
	}
	r.book.wallets = append(r.book.wallets, opened)
	return nil
}

// GetForUpdate and UpdateBalance belong to the same repository and are not part
// of the opening: the wallet is born here, so there is no row to lock and no
// balance to move.
func (r walletRows) GetForUpdate(context.Context, identity.WalletID) (wallet.State, error) {
	return wallet.State{}, storage.ErrWalletNotFound
}

func (r walletRows) UpdateBalance(context.Context, *wallet.Wallet, int64) error {
	return storage.ErrWalletNotFound
}

type transactionRows struct {
	book *book
}

// ByKey answers no row: an opening carries no idempotency key, so nothing in this
// use case looks one up.
// The three ports of the reference wait are part of the same repository and are
// never reached from an opening: an OPENING cites no operation.
func (r transactionRows) ByExternalID(context.Context, identity.ProviderID, identity.ExternalTransactionID) (wager.State, error) {
	return wager.State{}, storage.ErrTransactionNotFound
}

func (r transactionRows) HasProcessedReversal(context.Context, identity.ProviderID, identity.ExternalTransactionID) (bool, error) {
	return false, nil
}

func (r transactionRows) ClaimWait(context.Context, identity.TransactionID, time.Time) (storage.Wait, error) {
	return storage.Wait{}, storage.ErrTransactionNotFound
}

func (r transactionRows) EndWait(context.Context, *wager.Transaction) error {
	return storage.ErrTransactionNotFound
}

func (r transactionRows) RescheduleWait(context.Context, identity.TransactionID, time.Time, time.Time) error {
	return storage.ErrTransactionNotFound
}

func (r transactionRows) ByKey(context.Context, identity.ProviderID, identity.IdempotencyKey) (wager.State, error) {
	return wager.State{}, storage.ErrTransactionNotFound
}

func (r transactionRows) Insert(_ context.Context, recorded *wager.Transaction) error {
	r.book.transactionAttempts++
	r.book.transactions = append(r.book.transactions, recorded)
	return nil
}

type entryRows struct {
	book *book
}

func (r entryRows) Insert(_ context.Context, entry ledger.Entry) error {
	if r.book.entryErr != nil {
		return r.book.entryErr
	}
	r.book.entries = append(r.book.entries, entry)
	return nil
}

var frozen = time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)

type frozenClock struct{}

func (frozenClock) Now() time.Time {
	return frozen
}

type minter struct {
	wallet      identity.WalletID
	transaction identity.TransactionID
	entry       identity.LedgerEntryID
}

func (m minter) WalletID() (identity.WalletID, error) {
	return m.wallet, nil
}

func (m minter) TransactionID() (identity.TransactionID, error) {
	return m.transaction, nil
}

func (m minter) EntryID() (identity.LedgerEntryID, error) {
	return m.entry, nil
}

type brokenMinter struct {
	err error
}

func (b brokenMinter) WalletID() (identity.WalletID, error) {
	return identity.WalletID{}, b.err
}

func (b brokenMinter) TransactionID() (identity.TransactionID, error) {
	return identity.TransactionID{}, b.err
}

func (b brokenMinter) EntryID() (identity.LedgerEntryID, error) {
	return identity.LedgerEntryID{}, b.err
}

func fixedMinter(t *testing.T) minter {
	t.Helper()
	walletID, err := identity.ParseWalletID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	transactionID, err := identity.ParseTransactionID("33333333-3333-4333-8333-333333333333")
	if err != nil {
		t.Fatalf("ParseTransactionID = %v, want nil", err)
	}
	entryID, err := identity.ParseLedgerEntryID("44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatalf("ParseLedgerEntryID = %v, want nil", err)
	}
	return minter{wallet: walletID, transaction: transactionID, entry: entryID}
}

func playerOf(t *testing.T) identity.PlayerID {
	t.Helper()
	playerID, err := identity.ParsePlayerID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParsePlayerID = %v, want nil", err)
	}
	return playerID
}

func commandOf(t *testing.T, amount string) Command {
	t.Helper()
	balance, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	return Command{PlayerID: playerOf(t), InitialBalance: balance}
}
