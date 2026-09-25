package submitwager

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/bodyhash"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

func TestSubmit_recordsTheBetProcessedWithItsDebitEntry(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	result := submit(t, book, commandOf(t, wager.KindBet, "25.00"))
	if result.Status != wager.Processed {
		t.Fatalf("status of the bet = %s, want PROCESSED", result.Status)
	}
	if result.ObservedBalance.Amount() != "975.00" {
		t.Fatalf("observed balance = %s, want 975.00", result.ObservedBalance.Amount())
	}
	if result.IdempotentReplay {
		t.Fatalf("replay = %t, want false on the first completion", result.IdempotentReplay)
	}
	assertRows(t, book, 1, 1)
	assertEntry(t, book.entries[0], ledger.Debit, "25.00", "975.00")
	assertBalance(t, book, 97500, 2)
}

func TestSubmit_recordsTheWinProcessedWithItsCreditEntry(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	result := submit(t, book, commandOf(t, wager.KindWin, "50.00"))
	if result.ObservedBalance.Amount() != "1050.00" {
		t.Fatalf("observed balance = %s, want 1050.00", result.ObservedBalance.Amount())
	}
	assertRows(t, book, 1, 1)
	assertEntry(t, book.entries[0], ledger.Credit, "50.00", "1050.00")
	assertBalance(t, book, 105000, 2)
}

func TestSubmit_recordsTheLossWithNoEntryAndNoVersionChange(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "975.00")
	result := submit(t, book, commandOf(t, wager.KindLoss, "0.00"))
	if result.Status != wager.Processed {
		t.Fatalf("status of the loss = %s, want PROCESSED", result.Status)
	}
	if result.ObservedBalance.Amount() != "975.00" {
		t.Fatalf("observed balance = %s, want the 975.00 the wallet already had", result.ObservedBalance.Amount())
	}
	assertRows(t, book, 1, 0)
	if len(book.balances) != 0 {
		t.Fatalf("balance writes = %d, want 0: a loss moves no balance", len(book.balances))
	}
}

func TestSubmit_writesNoRowInThePendingStatus(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	submit(t, book, commandOf(t, wager.KindBet, "25.00"))
	for key, state := range book.stored {
		if state.Status == wager.Pending {
			t.Fatalf("stored %s in PENDING, want a terminal status", key)
		}
	}
}

func TestSubmit_refusesAnOpeningWithoutWritingARow(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	assertRefused(t, book, commandOf(t, wager.KindOpening, "25.00"), wager.OpeningNotAllowed)
	assertNothingWritten(t, book)
}

func TestSubmit_refusesAnAmountTheKindDoesNotAllowWithoutWritingARow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		kind wager.Kind
		text string
	}{
		{name: "a bet of zero is refused", kind: wager.KindBet, text: "0.00"},
		{name: "a loss with an amount is refused", kind: wager.KindLoss, text: "25.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := bookWith(t, "1000.00")
			assertRefused(t, book, commandOf(t, tc.kind, tc.text), wager.AmountNotAllowedForKind)
			assertNothingWritten(t, book)
		})
	}
}

// The wallet is absent, so the row of the operation could not exist: its foreign
// key names a wallet that is not there.
func TestSubmit_refusesAWalletThatDoesNotExistWithoutWritingARow(t *testing.T) {
	t.Parallel()
	book := &book{stored: map[string]wager.State{}}
	assertRefused(t, book, commandOf(t, wager.KindBet, "25.00"), wager.WalletNotFound)
	assertNothingWritten(t, book)
	if book.rollbacks != 1 {
		t.Fatalf("rollbacks for a wallet that does not exist = %d, want 1", book.rollbacks)
	}
}

func TestSubmit_recordsTheRejectionOfARuleAndMovesNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command func(*testing.T) Command
		want    wager.FailureCode
	}{
		{
			name:    "a bet past the balance is rejected",
			command: func(t *testing.T) Command { return commandOf(t, wager.KindBet, "2000.00") },
			want:    wager.InsufficientFunds,
		},
		{
			name:    "a player that does not own the wallet is rejected",
			command: otherPlayerCommand,
			want:    wager.PlayerWalletMismatch,
		},
		{
			name:    "another currency is rejected",
			command: otherCurrencyCommand,
			want:    wager.CurrencyMismatch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := bookWith(t, "1000.00")
			assertRefused(t, book, tc.command(t), tc.want)
			assertRejectedRow(t, book, tc.want)
		})
	}
}

// The token of a durable rejection is the recorded one, and the row that carries
// it is the only thing the commit wrote.
func assertRejectedRow(t *testing.T, ledgerBook *book, want wager.FailureCode) {
	t.Helper()
	assertRows(t, ledgerBook, 1, 0)
	for _, state := range ledgerBook.stored {
		if state.Status != wager.Rejected || state.FailureCode != want {
			t.Fatalf("stored %s with %s, want REJECTED with %s", state.Status, state.FailureCode, want)
		}
	}
	if len(ledgerBook.balances) != 0 {
		t.Fatalf("balance writes = %d, want 0: a rejection moves no balance", len(ledgerBook.balances))
	}
}

// The dispatch has one arm per kind the provider sends. The internal OPENING is
// the one it has none for, and it is refused before this when the transaction is
// built: reaching here means the vocabulary grew a kind with no arm.
func TestDecisionOf_refusesAKindThisUseCaseDoesNotSettle(t *testing.T) {
	t.Parallel()
	owner := walletWith(t, "1000.00")
	job := internalOpening(t, owner)
	if _, err := decisionOf(owner, job, wager.Reference{}); !errors.Is(err, ErrKindNotAccepted) {
		t.Fatalf("decisionOf = %v, want %v", err, ErrKindNotAccepted)
	}
}

// The replay answers the balance of the commit that closed the operation, and not
// the balance the wallet holds now.
func TestSubmit_replaysTheRecordedOutcomeWithTheBalanceObservedBackThen(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "500.00")
	cmd := commandOf(t, wager.KindBet, "25.00")
	book.keep(t, cmd, processedState(t, cmd))
	result := submit(t, book, cmd)
	if !result.IdempotentReplay {
		t.Fatalf("replay = %t, want true for the second arrival", result.IdempotentReplay)
	}
	if result.ObservedBalance.Amount() != "975.00" {
		t.Fatalf("observed balance = %s, want the 975.00 of the original completion", result.ObservedBalance.Amount())
	}
	assertRows(t, book, 1, 0)
	if len(book.balances) != 0 {
		t.Fatalf("balance writes = %d, want 0: a replay moves nothing", len(book.balances))
	}
}

func TestSubmit_replaysTheRecordedRejectionWithTheSameTokenAndTheMarker(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	cmd := commandOf(t, wager.KindBet, "25.00")
	book.keep(t, cmd, rejectedState(t, cmd, wager.InsufficientFunds))
	_, err := service(t, book).Submit(context.Background(), cmd)
	assertToken(t, err, wager.InsufficientFunds)
	var replayed Replayed
	if !errors.As(err, &replayed) || !replayed.IdempotentReplay() {
		t.Fatalf("Submit = %v, want it marked as a replay", err)
	}
	assertRows(t, book, 1, 0)
}

func TestSubmit_refusesAnotherBodyUnderTheSameKey(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	cmd := commandOf(t, wager.KindBet, "25.00")
	other := processedState(t, cmd)
	other.BodyHash = "another hash"
	book.keep(t, cmd, other)
	_, err := service(t, book).Submit(context.Background(), cmd)
	assertToken(t, err, wager.IdempotencyConflict)
	var replayed Replayed
	if errors.As(err, &replayed) {
		t.Fatalf("Submit = %v, want the conflict not marked as a replay", err)
	}
	assertRows(t, book, 1, 0)
}

// PENDING is the only status that is neither terminal nor a recorded wait, and
// the schema refuses to write it. The guard stays so a row in a status the switch
// does not know answers a retry instead of falling through in silence.
func TestSubmit_refusesWhileTheRecordedOperationIsNotTerminal(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	cmd := commandOf(t, wager.KindBet, "25.00")
	inFlight := processedState(t, cmd)
	inFlight.Status = wager.Pending
	book.keep(t, cmd, inFlight)
	_, err := service(t, book).Submit(context.Background(), cmd)
	if !errors.Is(err, ErrOutcomeInFlight) {
		t.Fatalf("Submit while the recorded operation is not terminal = %v, want %v", err, ErrOutcomeInFlight)
	}
}

// A wait is durable and lasts until its deadline, so the same key and the same
// body answer the wait already recorded instead of a retry that would have the
// provider hammer the route for fifteen minutes.
func TestSubmit_replaysTheRecordedWaitInsteadOfAskingForARetry(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	cmd := citingCommand(t, wager.KindWin, "50.00")
	book.keep(t, cmd, waitingState(t, cmd))
	result := submit(t, book, cmd)
	if result.Status != wager.PendingReference {
		t.Fatalf("status = %s, want PENDING_REFERENCE", result.Status)
	}
	if !result.IdempotentReplay {
		t.Fatalf("replay = %t, want true for the second arrival of a recorded wait", result.IdempotentReplay)
	}
	if !result.ObservedBalance.Currency().IsZero() {
		t.Fatalf("observed balance = %s, want none: no commit closed the wait", result.ObservedBalance.Amount())
	}
	assertRows(t, book, 1, 0)
	if len(book.balances) != 0 {
		t.Fatalf("balance writes = %d, want 0: replaying a wait moves nothing", len(book.balances))
	}
}

// The loser of the unique index undoes its own movement and answers what the
// winning row recorded.
func TestSubmit_replaysAfterLosingTheKeyIndex(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	cmd := commandOf(t, wager.KindBet, "25.00")
	book.insertErr = wager.NewRejection(wager.IdempotencyConflict, nil)
	book.outside = statePointer(processedState(t, cmd))
	result := submit(t, book, cmd)
	if !result.IdempotentReplay || result.ObservedBalance.Amount() != "975.00" {
		t.Fatalf("result = %+v, want the replay of the winning row", result)
	}
	if book.commits != 0 || book.rollbacks != 1 {
		t.Fatalf("commits = %d and rollbacks = %d, want 0 and 1", book.commits, book.rollbacks)
	}
	assertNothingWritten(t, book)
}

func TestSubmit_answersTheConflictAfterLosingTheKeyIndexToAnotherBody(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	cmd := commandOf(t, wager.KindBet, "25.00")
	winning := processedState(t, cmd)
	winning.BodyHash = "another hash"
	book.insertErr = wager.NewRejection(wager.IdempotencyConflict, nil)
	book.outside = statePointer(winning)
	_, err := service(t, book).Submit(context.Background(), cmd)
	assertToken(t, err, wager.IdempotencyConflict)
	assertNothingWritten(t, book)
}

func TestSubmit_answersTransientFailureWhenTheWinningRowIsNoLongerThere(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	book.insertErr = wager.NewRejection(wager.IdempotencyConflict, nil)
	_, err := service(t, book).Submit(context.Background(), commandOf(t, wager.KindBet, "25.00"))
	if !errors.Is(err, ErrRaceUnresolved) {
		t.Fatalf("Submit with the winning row gone = %v, want %v", err, ErrRaceUnresolved)
	}
	assertNothingWritten(t, book)
}

// The duplicate of the external identifier is answered as it is when nothing holds
// this key: the winning row was written under another one, so there is nothing to
// replay here. Telling that arrival apart from a twin of the same key is what the
// single read outside the transaction buys.
func TestSubmit_answersTheDuplicateExternalTransactionWhenNothingHoldsTheKey(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	book.insertErr = wager.NewRejection(wager.DuplicateExternalTransaction, nil)
	_, err := service(t, book).Submit(context.Background(), commandOf(t, wager.KindBet, "25.00"))
	assertToken(t, err, wager.DuplicateExternalTransaction)
	if book.outsideReads != 1 {
		t.Fatalf("reads outside the transaction = %d, want 1 to tell the two arrivals apart", book.outsideReads)
	}
	assertNothingWritten(t, book)
}

// A twin of the same operation under the same key violates the external id index
// as well as the key one, and PostgreSQL names only the first it checks. That
// token must not turn a replay into a conflict: the row under the key carries the
// same hash, so the answer is the result already recorded.
func TestSubmit_replaysAfterLosingTheExternalIndexToItsOwnKey(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	cmd := commandOf(t, wager.KindBet, "25.00")
	book.insertErr = wager.NewRejection(wager.DuplicateExternalTransaction, nil)
	book.outside = statePointer(processedState(t, cmd))
	result := submit(t, book, cmd)
	if !result.IdempotentReplay {
		t.Fatalf("idempotentReplay = %t, want true for a twin of the same key", result.IdempotentReplay)
	}
	if result.ObservedBalance.Amount() != "975.00" {
		t.Fatalf("observed balance = %s, want the 975.00 of the winning row", result.ObservedBalance.Amount())
	}
	assertNothingWritten(t, book)
}

// A write that missed the lock is transient: it undoes the whole commit, leaves no
// row and carries no token.
func TestSubmit_undoesEverythingWhenTheBalanceWriteMissesTheLock(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	book.lostWrite = true
	_, err := service(t, book).Submit(context.Background(), commandOf(t, wager.KindBet, "25.00"))
	if !errors.Is(err, storage.ErrLostWrite) {
		t.Fatalf("Submit after a write that missed the lock = %v, want %v", err, storage.ErrLostWrite)
	}
	var rejection wager.Rejection
	if errors.As(err, &rejection) {
		t.Fatalf("Submit = %v, want no business rejection for a lost write", err)
	}
	assertNothingWritten(t, book)
	if book.rollbacks != 1 {
		t.Fatalf("rollbacks for a balance write that missed the lock = %d, want 1", book.rollbacks)
	}
}

func TestSubmit_stampsTheInjectedInstant(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	submit(t, book, commandOf(t, wager.KindBet, "25.00"))
	for _, state := range book.stored {
		if !state.CreatedAt.Equal(frozen) {
			t.Fatalf("created at = %s, want %s", state.CreatedAt, frozen)
		}
	}
}

func TestSubmit_refusesWhenAnIdentityCannotBeMinted(t *testing.T) {
	t.Parallel()
	broken := errors.New("mint: entropy exhausted")
	book := bookWith(t, "1000.00")
	service := New(book, book, brokenMinter{err: broken}, frozenClock{}, schedule{})
	_, err := service.Submit(context.Background(), commandOf(t, wager.KindBet, "25.00"))
	if !errors.Is(err, broken) {
		t.Fatalf("Submit with an unmintable identity = %v, want %v", err, broken)
	}
	assertNothingWritten(t, book)
}

func submit(t *testing.T, ledgerBook *book, cmd Command) Result {
	t.Helper()
	result, err := service(t, ledgerBook).Submit(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Submit = %v, want nil", err)
	}
	return result
}

func service(t *testing.T, ledgerBook *book) *Service {
	t.Helper()
	return New(ledgerBook, ledgerBook, fixedMinter(t), frozenClock{}, schedule{})
}

// schedule is the policy of the wait with no draw in it: the deadline is the TTL
// of the rule and the first attempt is the instant of entry, so a case about the
// wait reads instants and not a range.
type schedule struct{}

func (schedule) DeadlineAt(entered time.Time) time.Time {
	return entered.Add(15 * time.Minute)
}

func (schedule) NextAttemptAt(_ int64, now, deadline time.Time) time.Time {
	if now.After(deadline) {
		return deadline
	}
	return now
}

func assertRefused(t *testing.T, ledgerBook *book, cmd Command, want wager.FailureCode) {
	t.Helper()
	_, err := service(t, ledgerBook).Submit(context.Background(), cmd)
	assertToken(t, err, want)
}

func assertToken(t *testing.T, err error, want wager.FailureCode) {
	t.Helper()
	var rejection wager.Rejection
	if !errors.As(err, &rejection) {
		t.Fatalf("Submit = %v, want a business rejection", err)
	}
	if rejection.Code() != want {
		t.Fatalf("failureCode = %s, want %s", rejection.Code(), want)
	}
}

func assertRows(t *testing.T, ledgerBook *book, transactions, entries int) {
	t.Helper()
	if len(ledgerBook.stored) != transactions {
		t.Fatalf("stored transactions = %d, want %d", len(ledgerBook.stored), transactions)
	}
	if len(ledgerBook.entries) != entries {
		t.Fatalf("entries = %d, want %d", len(ledgerBook.entries), entries)
	}
}

func assertNothingWritten(t *testing.T, ledgerBook *book) {
	t.Helper()
	assertRows(t, ledgerBook, 0, 0)
	if len(ledgerBook.balances) != 0 {
		t.Fatalf("balance writes = %d, want 0", len(ledgerBook.balances))
	}
}

func assertEntry(t *testing.T, entry ledger.Entry, direction ledger.Direction, amount, after string) {
	t.Helper()
	if entry.Direction() != direction {
		t.Fatalf("direction = %s, want %s", entry.Direction(), direction)
	}
	if entry.Amount().Amount() != amount {
		t.Fatalf("entry amount = %s, want %s", entry.Amount().Amount(), amount)
	}
	if entry.BalanceAfter().Amount() != after {
		t.Fatalf("balance after = %s, want %s", entry.BalanceAfter().Amount(), after)
	}
}

func assertBalance(t *testing.T, ledgerBook *book, cents, version int64) {
	t.Helper()
	if len(ledgerBook.balances) != 1 {
		t.Fatalf("balance writes = %d, want 1", len(ledgerBook.balances))
	}
	written := ledgerBook.balances[0]
	if written.cents != cents || written.version != version {
		t.Fatalf("balance written = %d at version %d, want %d at version %d", written.cents, written.version, cents, version)
	}
	if written.readVersion != version-1 {
		t.Fatalf("write conditioned on version %d, want the %d read under the lock", written.readVersion, version-1)
	}
}

// book is the in-memory persistence of one case: the wallet it locks, what each
// repository was asked to write, and whether the set was committed.
type book struct {
	owner    *wallet.Wallet
	stored   map[string]wager.State
	staged   []*wager.Transaction
	entries  []ledger.Entry
	balances []balanceWrite

	commits   int
	rollbacks int

	// insertErr is what the transaction insert answers, which is how a test plays
	// the unique index refusing the row.
	insertErr error
	// lostWrite makes the balance write affect no row.
	lostWrite bool
	// outside is what the read after the rollback answers, and nil is the absence
	// of the winning row.
	outside      *wager.State
	outsideReads int

	// cited is what the lookup of the operation a submission names answers, keyed
	// by provider and external identifier.
	cited map[string]wager.State
	// reversed says the cited operation already carries a PROCESSED reversal.
	reversed map[string]bool
	// calls is the order the repositories were asked in, which is what pins the
	// wallet before the cited operation and the cited operation before the
	// question of the reversal.
	calls []string
}

type balanceWrite struct {
	cents       int64
	version     int64
	readVersion int64
}

func (b *book) keep(t *testing.T, cmd Command, state wager.State) {
	t.Helper()
	b.stored[keyOf(cmd.ProviderID, cmd.IdempotencyKey)] = state
}

func keyOf(provider identity.ProviderID, key identity.IdempotencyKey) string {
	return provider.String() + "|" + key.String()
}

func (b *book) Within(_ context.Context, work func(storage.Tx) error) error {
	if err := work(b); err != nil {
		b.rollbacks++
		b.staged = nil
		b.entries = nil
		b.balances = nil
		return err
	}
	b.commits++
	b.commit()
	return nil
}

// commit moves what was staged into what a later read can find, which is what
// makes a rollback observable in a test.
func (b *book) commit() {
	for _, recorded := range b.staged {
		b.stored[keyOf(recorded.ProviderID(), recorded.IdempotencyKey())] = stateOf(recorded)
	}
	b.staged = nil
}

func stateOf(recorded *wager.Transaction) wager.State {
	reference, _ := recorded.ReferenceExternalID()
	return wager.State{
		ID:                  recorded.ID(),
		Kind:                recorded.Kind(),
		PlayerID:            recorded.PlayerID(),
		WalletID:            recorded.WalletID(),
		Amount:              recorded.Amount(),
		ProviderID:          recorded.ProviderID(),
		ExternalID:          recorded.ExternalID(),
		IdempotencyKey:      recorded.IdempotencyKey(),
		BodyHash:            recorded.BodyHash(),
		RoundID:             recorded.RoundID(),
		GameID:              recorded.GameID(),
		ReferenceExternalID: reference,
		Status:              recorded.Status(),
		FailureCode:         recorded.FailureCode(),
		ObservedBalance:     recorded.ObservedBalance(),
		NextAttemptAt:       recorded.NextAttemptAt(),
		ReferenceDeadlineAt: recorded.ReferenceDeadlineAt(),
		CreatedAt:           recorded.CreatedAt(),
		UpdatedAt:           recorded.UpdatedAt(),
	}
}

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

func (r walletRows) Insert(context.Context, *wallet.Wallet) error {
	return nil
}

func (r walletRows) GetForUpdate(_ context.Context, id identity.WalletID) (wallet.State, error) {
	r.book.calls = append(r.book.calls, "wallet")
	owner := r.book.owner
	if owner == nil || owner.ID() != id {
		return wallet.State{}, storage.ErrWalletNotFound
	}
	return wallet.State{
		ID:        owner.ID(),
		PlayerID:  owner.PlayerID(),
		Balance:   owner.Balance(),
		Version:   owner.Version(),
		CreatedAt: owner.CreatedAt(),
		UpdatedAt: owner.UpdatedAt(),
	}, nil
}

func (r walletRows) UpdateBalance(_ context.Context, moved *wallet.Wallet, readVersion int64) error {
	if r.book.lostWrite {
		return storage.ErrLostWrite
	}
	r.book.balances = append(r.book.balances, balanceWrite{
		cents:       moved.Balance().Cents(),
		version:     moved.Version(),
		readVersion: readVersion,
	})
	return nil
}

type transactionRows struct {
	book *book
}

func (r transactionRows) Insert(_ context.Context, recorded *wager.Transaction) error {
	if r.book.insertErr != nil {
		return r.book.insertErr
	}
	r.book.staged = append(r.book.staged, recorded)
	return nil
}

func (r transactionRows) ByKey(_ context.Context, provider identity.ProviderID, key identity.IdempotencyKey) (wager.State, error) {
	found, ok := r.book.stored[keyOf(provider, key)]
	if !ok {
		return wager.State{}, storage.ErrTransactionNotFound
	}
	return found, nil
}

func (r transactionRows) ByExternalID(_ context.Context, provider identity.ProviderID, external identity.ExternalTransactionID) (wager.State, error) {
	r.book.calls = append(r.book.calls, "cited")
	found, ok := r.book.cited[provider.String()+"|"+external.String()]
	if !ok {
		return wager.State{}, storage.ErrTransactionNotFound
	}
	return found, nil
}

func (r transactionRows) HasProcessedReversal(_ context.Context, provider identity.ProviderID, cited identity.ExternalTransactionID) (bool, error) {
	r.book.calls = append(r.book.calls, "reversal")
	return r.book.reversed[provider.String()+"|"+cited.String()], nil
}

// The two ports of the worker belong to the same repository and are never
// reached from a submission: the submission writes the wait, and the worker is
// the one that claims it back.
func (r transactionRows) ClaimWait(context.Context, identity.TransactionID, time.Time) (storage.Wait, error) {
	return storage.Wait{}, storage.ErrTransactionNotFound
}

func (r transactionRows) EndWait(context.Context, *wager.Transaction) error {
	return storage.ErrTransactionNotFound
}

func (r transactionRows) RescheduleWait(context.Context, identity.TransactionID, time.Time, time.Time) error {
	return storage.ErrTransactionNotFound
}

type entryRows struct {
	book *book
}

func (r entryRows) Insert(_ context.Context, entry ledger.Entry) error {
	r.book.entries = append(r.book.entries, entry)
	return nil
}

// The read side of the port. Only the lookup by key belongs to this use case: the
// two others are part of the same port and are never reached from here.
func (b *book) Wallet(context.Context, identity.WalletID) (storage.WalletView, error) {
	return storage.WalletView{}, storage.ErrWalletNotFound
}

func (b *book) Transaction(context.Context, identity.TransactionID, identity.ProviderID) (storage.TransactionView, error) {
	return storage.TransactionView{}, storage.ErrTransactionNotFound
}

// The queue of the waits is part of the same port and is never reached from a
// submission: what scans it is the worker.
func (b *book) DueWaits(context.Context, time.Time, int) ([]storage.WaitCandidate, error) {
	return nil, nil
}

func (b *book) TransactionByKey(context.Context, identity.ProviderID, identity.IdempotencyKey) (wager.State, error) {
	b.outsideReads++
	if b.outside == nil {
		return wager.State{}, storage.ErrTransactionNotFound
	}
	return *b.outside, nil
}

func bookWith(t *testing.T, balance string) *book {
	t.Helper()
	return &book{
		owner:    walletWith(t, balance),
		stored:   map[string]wager.State{},
		cited:    map[string]wager.State{},
		reversed: map[string]bool{},
	}
}

func walletWith(t *testing.T, balance string) *wallet.Wallet {
	t.Helper()
	opened, err := wallet.Rehydrate(wallet.State{
		ID:        walletOf(t),
		PlayerID:  playerOf(t),
		Balance:   moneyOf(t, balance, "BRL"),
		Version:   1,
		CreatedAt: frozen,
		UpdatedAt: frozen,
	})
	if err != nil {
		t.Fatalf("wallet.Rehydrate = %v, want nil", err)
	}
	return opened
}

func commandOf(t *testing.T, kind wager.Kind, amount string) Command {
	t.Helper()
	return command(t, kind, moneyOf(t, amount, "BRL"), playerOf(t))
}

func otherPlayerCommand(t *testing.T) Command {
	t.Helper()
	other, err := identity.ParsePlayerID("99999999-9999-4999-8999-999999999999")
	if err != nil {
		t.Fatalf("ParsePlayerID of the other player = %v, want nil", err)
	}
	return command(t, wager.KindBet, moneyOf(t, "25.00", "BRL"), other)
}

func otherCurrencyCommand(t *testing.T) Command {
	t.Helper()
	return command(t, wager.KindBet, moneyOf(t, "25.00", "USD"), playerOf(t))
}

func command(t *testing.T, kind wager.Kind, amount money.Money, player identity.PlayerID) Command {
	t.Helper()
	return Command{
		ProviderID:     providerOf(t),
		ExternalID:     externalOf(t),
		IdempotencyKey: keyValueOf(t),
		PlayerID:       player,
		WalletID:       walletOf(t),
		RoundID:        roundOf(t),
		GameID:         gameOf(t),
		Kind:           kind,
		Amount:         amount,
	}
}

// observedBackThen is the balance the original commit recorded, which is the one a
// replay answers however much the wallet has moved since.
const observedBackThen = "975.00"

// processedState is the operation as an earlier commit recorded it, with the hash
// the same arrival produces again.
func processedState(t *testing.T, cmd Command) wager.State {
	t.Helper()
	state := recordedState(t, cmd)
	state.Status = wager.Processed
	state.ObservedBalance = moneyOf(t, observedBackThen, "BRL")
	return state
}

func rejectedState(t *testing.T, cmd Command, code wager.FailureCode) wager.State {
	t.Helper()
	state := recordedState(t, cmd)
	state.Status = wager.Rejected
	state.FailureCode = code
	return state
}

func recordedState(t *testing.T, cmd Command) wager.State {
	t.Helper()
	return wager.State{
		ID:                  transactionOf(t),
		Kind:                cmd.Kind,
		PlayerID:            cmd.PlayerID,
		WalletID:            cmd.WalletID,
		Amount:              cmd.Amount,
		ProviderID:          cmd.ProviderID,
		ExternalID:          cmd.ExternalID,
		IdempotencyKey:      cmd.IdempotencyKey,
		BodyHash:            bodyhash.Of(cmd.business()),
		RoundID:             cmd.RoundID,
		GameID:              cmd.GameID,
		ReferenceExternalID: cmd.ReferenceExternalID,
		CreatedAt:           frozen,
		UpdatedAt:           frozen,
	}
}

func statePointer(state wager.State) *wager.State {
	return &state
}

// internalOpening is the one kind the dispatch has no arm for. It is built by
// rehydration and not by the constructor, because the constructor refuses an
// OPENING arriving from a provider before the dispatch ever sees it.
func internalOpening(t *testing.T, owner *wallet.Wallet) pending {
	t.Helper()
	op, err := wager.Rehydrate(wager.State{
		ID:        transactionOf(t),
		Kind:      wager.KindOpening,
		PlayerID:  owner.PlayerID(),
		WalletID:  owner.ID(),
		Amount:    moneyOf(t, "25.00", "BRL"),
		Status:    wager.Pending,
		CreatedAt: frozen,
		UpdatedAt: frozen,
	})
	if err != nil {
		t.Fatalf("wager.Rehydrate = %v, want nil", err)
	}
	return pending{op: op, move: wager.Movement{EntryID: entryOf(t), At: frozen}}
}

var frozen = time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)

type frozenClock struct{}

func (frozenClock) Now() time.Time {
	return frozen
}

type minter struct {
	transaction identity.TransactionID
	entry       identity.LedgerEntryID
}

func (m minter) TransactionID() (identity.TransactionID, error) {
	return m.transaction, nil
}

func (m minter) EntryID() (identity.LedgerEntryID, error) {
	return m.entry, nil
}

func fixedMinter(t *testing.T) minter {
	t.Helper()
	return minter{transaction: transactionOf(t), entry: entryOf(t)}
}

type brokenMinter struct {
	err error
}

func (m brokenMinter) TransactionID() (identity.TransactionID, error) {
	return identity.TransactionID{}, m.err
}

func (m brokenMinter) EntryID() (identity.LedgerEntryID, error) {
	return identity.LedgerEntryID{}, m.err
}

func moneyOf(t *testing.T, amount, currency string) money.Money {
	t.Helper()
	parsed, err := money.Parse(amount, currency)
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	return parsed
}

func walletOf(t *testing.T) identity.WalletID {
	t.Helper()
	id, err := identity.ParseWalletID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	return id
}

func playerOf(t *testing.T) identity.PlayerID {
	t.Helper()
	id, err := identity.ParsePlayerID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParsePlayerID in the helper = %v, want nil", err)
	}
	return id
}

func transactionOf(t *testing.T) identity.TransactionID {
	t.Helper()
	id, err := identity.ParseTransactionID("33333333-3333-4333-8333-333333333333")
	if err != nil {
		t.Fatalf("ParseTransactionID = %v, want nil", err)
	}
	return id
}

func entryOf(t *testing.T) identity.LedgerEntryID {
	t.Helper()
	id, err := identity.ParseLedgerEntryID("44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatalf("ParseLedgerEntryID = %v, want nil", err)
	}
	return id
}

func providerOf(t *testing.T) identity.ProviderID {
	t.Helper()
	id, err := identity.ParseProviderID("provider-a")
	if err != nil {
		t.Fatalf("ParseProviderID = %v, want nil", err)
	}
	return id
}

func externalOf(t *testing.T) identity.ExternalTransactionID {
	t.Helper()
	id, err := identity.ParseExternalTransactionID("external-1")
	if err != nil {
		t.Fatalf("ParseExternalTransactionID = %v, want nil", err)
	}
	return id
}

func keyValueOf(t *testing.T) identity.IdempotencyKey {
	t.Helper()
	id, err := identity.ParseIdempotencyKey("key-1")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey = %v, want nil", err)
	}
	return id
}

func roundOf(t *testing.T) identity.RoundID {
	t.Helper()
	id, err := identity.ParseRoundID("round-1")
	if err != nil {
		t.Fatalf("ParseRoundID = %v, want nil", err)
	}
	return id
}

func gameOf(t *testing.T) identity.GameID {
	t.Helper()
	id, err := identity.ParseGameID("game-1")
	if err != nil {
		t.Fatalf("ParseGameID = %v, want nil", err)
	}
	return id
}

// citedExternalOf is the identifier every citing case below names, told apart
// from the identifier of the operation doing the citing.
func citedExternalOf(t *testing.T) identity.ExternalTransactionID {
	t.Helper()
	id, err := identity.ParseExternalTransactionID("cited-1")
	if err != nil {
		t.Fatalf("ParseExternalTransactionID of the cited operation = %v, want nil", err)
	}
	return id
}

// citingCommand is one operation that names another, which is what a reversal
// always is and a WIN may be.
func citingCommand(t *testing.T, kind wager.Kind, amount string) Command {
	t.Helper()
	cmd := commandOf(t, kind, amount)
	cmd.ReferenceExternalID = citedExternalOf(t)
	return cmd
}

// waitingState is the wait an earlier commit recorded under that key: the row is
// durable, no balance was observed, and the deadline was written on entry.
func waitingState(t *testing.T, cmd Command) wager.State {
	t.Helper()
	state := recordedState(t, cmd)
	state.Status = wager.PendingReference
	state.NextAttemptAt = frozen
	state.ReferenceDeadlineAt = frozen.Add(15 * time.Minute)
	return state
}

// cite files the operation a submission names, as an earlier commit recorded it.
func (b *book) cite(t *testing.T, state wager.State) {
	t.Helper()
	b.cited[state.ProviderID.String()+"|"+state.ExternalID.String()] = state
}

// citedProcessed is the cited operation as a commit that went through left it:
// of the same provider, player, wallet and round as the one citing it.
func citedProcessed(t *testing.T, kind wager.Kind, amount string) wager.State {
	t.Helper()
	return wager.State{
		ID:              transactionOf(t),
		Kind:            kind,
		PlayerID:        playerOf(t),
		WalletID:        walletOf(t),
		Amount:          moneyOf(t, amount, "BRL"),
		ProviderID:      providerOf(t),
		ExternalID:      citedExternalOf(t),
		IdempotencyKey:  keyValueOf(t),
		BodyHash:        "hash of the cited operation",
		RoundID:         roundOf(t),
		GameID:          gameOf(t),
		Status:          wager.Processed,
		ObservedBalance: moneyOf(t, "1000.00", "BRL"),
		CreatedAt:       frozen,
		UpdatedAt:       frozen,
	}
}

// The hash answers the business, and the operation a submission cites is part of
// it: two arrivals alike in everything else but naming different operations are
// different business and must not replay one another.
func TestBusiness_answersAnotherHashForAnotherCitedOperation(t *testing.T) {
	t.Parallel()
	plain := commandOf(t, wager.KindWin, "50.00")
	citing := citingCommand(t, wager.KindWin, "50.00")
	other := citing
	other.ReferenceExternalID = externalOf(t)
	hashes := map[string]string{
		"citing nothing":       bodyhash.Of(plain.business()),
		"citing one operation": bodyhash.Of(citing.business()),
		"citing another":       bodyhash.Of(other.business()),
	}
	assertDistinct(t, hashes)
	repeated := bodyhash.Of(citingCommand(t, wager.KindWin, "50.00").business())
	if got := bodyhash.Of(citing.business()); got != repeated {
		t.Fatalf("hash of a second arrival citing the same operation = %s, want %s", got, repeated)
	}
}

func assertDistinct(t *testing.T, hashes map[string]string) {
	t.Helper()
	seen := map[string]string{}
	for name, hash := range hashes {
		if before, taken := seen[hash]; taken {
			t.Fatalf("%q and %q answered the same hash, want one per business", before, name)
		}
		seen[hash] = name
	}
}

// The cited operation is read under the lock already taken, and never before it:
// its status moves only under the lock of the wallet it belongs to.
func TestSubmit_readsTheWalletThenTheCitedOperationThenTheReversal(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	book.cite(t, citedProcessed(t, wager.KindBet, "25.00"))
	submit(t, book, citingCommand(t, wager.KindRefund, "25.00"))
	want := []string{"wallet", "cited", "reversal"}
	if !slices.Equal(book.calls, want) {
		t.Fatalf("repositories asked in the order %v, want %v", book.calls, want)
	}
}

// A WIN citing no operation never asks for one, so a submission that ends inside
// its own request opens no query it does not need.
func TestSubmit_readsNoCitedOperationForAnOperationThatNamesNone(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	submit(t, book, commandOf(t, wager.KindWin, "50.00"))
	want := []string{"wallet"}
	if !slices.Equal(book.calls, want) {
		t.Fatalf("repositories asked in the order %v, want %v", book.calls, want)
	}
}

// The question of the reversal belongs to a reversal. A WIN citing an operation
// does not ask it, because no rule of a WIN reads the answer.
func TestSubmit_asksAboutTheReversalOnlyForAReversal(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	book.cite(t, citedProcessed(t, wager.KindBet, "25.00"))
	submit(t, book, citingCommand(t, wager.KindWin, "50.00"))
	want := []string{"wallet", "cited"}
	if !slices.Equal(book.calls, want) {
		t.Fatalf("repositories asked in the order %v, want %v", book.calls, want)
	}
}

// The three kinds that cite another operation reach the function of the domain
// that owns them, and each one both concludes and is refused.
func TestSubmit_settlesTheThreeKindsThatCiteAnotherOperation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		cited     wager.State
		reversed  bool
		command   func(*testing.T) Command
		balance   string
		direction ledger.Direction
	}{
		{
			name:      "a refund of a processed bet credits the whole amount",
			cited:     citedProcessed(t, wager.KindBet, "25.00"),
			command:   func(t *testing.T) Command { return citingCommand(t, wager.KindRefund, "25.00") },
			balance:   "1025.00",
			direction: ledger.Credit,
		},
		{
			name:      "a rollback of a processed win debits it back",
			cited:     citedProcessed(t, wager.KindWin, "50.00"),
			command:   func(t *testing.T) Command { return citingCommand(t, wager.KindRollback, "50.00") },
			balance:   "950.00",
			direction: ledger.Debit,
		},
		{
			name:      "a win citing a processed bet credits",
			cited:     citedProcessed(t, wager.KindBet, "25.00"),
			command:   func(t *testing.T) Command { return citingCommand(t, wager.KindWin, "50.00") },
			balance:   "1050.00",
			direction: ledger.Credit,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := bookWith(t, "1000.00")
			book.cite(t, tc.cited)
			result := submit(t, book, tc.command(t))
			if result.Status != wager.Processed {
				t.Fatalf("status = %s, want PROCESSED", result.Status)
			}
			if result.ObservedBalance.Amount() != tc.balance {
				t.Fatalf("observed balance = %s, want %s", result.ObservedBalance.Amount(), tc.balance)
			}
			assertRows(t, book, 1, 1)
			assertEntry(t, book.entries[0], tc.direction, tc.command(t).Amount.Amount(), tc.balance)
		})
	}
}

func TestSubmit_refusesTheThreeKindsThatCiteAnotherOperationWithTheirTokens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		cited    wager.State
		reversed bool
		command  func(*testing.T) Command
		want     wager.FailureCode
	}{
		{
			name:    "a refund that is not the whole bet is refused",
			cited:   citedProcessed(t, wager.KindBet, "25.00"),
			command: func(t *testing.T) Command { return citingCommand(t, wager.KindRefund, "10.00") },
			want:    wager.ReversalAmountMismatch,
		},
		{
			name:     "a second reversal of the same operation is refused",
			cited:    citedProcessed(t, wager.KindBet, "25.00"),
			reversed: true,
			command:  func(t *testing.T) Command { return citingCommand(t, wager.KindRollback, "25.00") },
			want:     wager.AlreadyReversed,
		},
		{
			name:    "a win citing an operation of another round is refused",
			cited:   citedOfAnotherRound(t),
			command: func(t *testing.T) Command { return citingCommand(t, wager.KindWin, "50.00") },
			want:    wager.ReferenceMismatch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := bookWith(t, "1000.00")
			book.cite(t, tc.cited)
			if tc.reversed {
				book.reversed[providerOf(t).String()+"|"+citedExternalOf(t).String()] = true
			}
			assertRefused(t, book, tc.command(t), tc.want)
			assertRejectedRow(t, book, tc.want)
		})
	}
}

// citedOfAnotherRound is an operation that arrived and does not close with the one
// citing it, which is the refusal and not the wait.
func citedOfAnotherRound(t *testing.T) wager.State {
	t.Helper()
	state := citedProcessed(t, wager.KindBet, "25.00")
	other, err := identity.ParseRoundID("round-2")
	if err != nil {
		t.Fatalf("ParseRoundID of the other round = %v, want nil", err)
	}
	state.RoundID = other
	return state
}

// An operation whose cited one has not arrived records the wait and nothing else:
// no entry, no balance and no version, with the deadline written once.
func TestSubmit_recordsTheWaitWhenTheCitedOperationHasNotArrived(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	result := submit(t, book, citingCommand(t, wager.KindWin, "50.00"))
	if result.Status != wager.PendingReference {
		t.Fatalf("status = %s, want PENDING_REFERENCE", result.Status)
	}
	if result.IdempotentReplay {
		t.Fatalf("replay = %t, want false on the first arrival", result.IdempotentReplay)
	}
	if !result.ObservedBalance.Currency().IsZero() {
		t.Fatalf("observed balance = %s, want none: no commit closed the operation", result.ObservedBalance.Amount())
	}
	assertRows(t, book, 1, 0)
	if len(book.balances) != 0 {
		t.Fatalf("balance writes = %d, want 0: entering the wait moves no balance", len(book.balances))
	}
	assertWaitWritten(t, book)
}

// The deadline is the instant of entry plus the TTL, and the first attempt is
// scheduled inside it.
func assertWaitWritten(t *testing.T, ledgerBook *book) {
	t.Helper()
	for _, state := range ledgerBook.stored {
		if !state.ReferenceDeadlineAt.Equal(frozen.Add(15 * time.Minute)) {
			t.Fatalf("deadline = %s, want %s", state.ReferenceDeadlineAt, frozen.Add(15*time.Minute))
		}
		if state.NextAttemptAt.IsZero() || state.NextAttemptAt.After(state.ReferenceDeadlineAt) {
			t.Fatalf("next attempt = %s, want an instant inside the wait", state.NextAttemptAt)
		}
	}
}

// A reversal citing an operation that ended badly is refused at once, with no
// wait recorded: no later arrival changes what that operation already is.
func TestSubmit_refusesACitedOperationThatEndedBadlyWithoutRecordingAWait(t *testing.T) {
	t.Parallel()
	book := bookWith(t, "1000.00")
	refused := citedProcessed(t, wager.KindBet, "25.00")
	refused.Status = wager.Rejected
	refused.FailureCode = wager.InsufficientFunds
	refused.ObservedBalance = money.Money{}
	book.cite(t, refused)
	assertRefused(t, book, citingCommand(t, wager.KindRefund, "25.00"), wager.ReferenceUnsuccessful)
	assertRejectedRow(t, book, wager.ReferenceUnsuccessful)
}

// The tests below call the helpers of the use case directly, each at the branch
// that makes it its own step. Submit already proves they compose; what is asserted
// here is the decision each one owns.

// pending stamps the arrival before any row exists, which is why the refusals of
// the spec leave from here.
func TestPending_stampsTheInjectedInstantAndTheBusinessHash(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	job, err := service(t, bookWith(t, "1000.00")).pending(cmd)
	if err != nil {
		t.Fatalf("pending of an accepted bet = %v, want nil", err)
	}
	if !job.at().Equal(frozen) {
		t.Fatalf("instant = %s, want the injected %s", job.at(), frozen)
	}
	if job.op.BodyHash() != bodyhash.Of(cmd.business()) {
		t.Fatalf("hash = %s, want the one of the business body", job.op.BodyHash())
	}
}

func TestPending_refusesWhenAnIdentityCannotBeMinted(t *testing.T) {
	t.Parallel()
	broken := errors.New("mint: entropy exhausted")
	failing := New(bookWith(t, "1000.00"), bookWith(t, "1000.00"), brokenMinter{err: broken}, frozenClock{}, schedule{})
	if _, err := failing.pending(commandOf(t, wager.KindBet, "25.00")); !errors.Is(err, broken) {
		t.Fatalf("pending with an unmintable identity = %v, want %v", err, broken)
	}
}

// An OPENING is not an external operation, and the spec refuses it before the
// transaction exists.
func TestPending_refusesASpecTheDomainDoesNotAccept(t *testing.T) {
	t.Parallel()
	job, err := service(t, bookWith(t, "1000.00")).pending(commandOf(t, wager.KindOpening, "25.00"))
	if err == nil {
		t.Fatalf("pending of an opening = %+v with no error, want the spec refused", job)
	}
}

// The rejection travels beside the result because a durable rejection is committed
// and still answered as a refusal, so answerOf is what turns one into the other.
func TestAnswerOf_answersTheRejectionBesideTheResultAsAFailure(t *testing.T) {
	t.Parallel()
	refusal := wager.NewRejection(wager.InsufficientFunds, nil)
	if _, err := answerOf(settlement{result: Result{}, rejection: refusal}); !errors.Is(err, refusal) {
		t.Fatalf("answerOf a settled rejection = %v, want %v", err, refusal)
	}
	settled := Result{Status: wager.Processed}
	answered, err := answerOf(settlement{result: settled})
	if err != nil {
		t.Fatalf("answerOf a settlement with no rejection = %v, want nil", err)
	}
	if answered != settled {
		t.Fatalf("result = %+v, want %+v", answered, settled)
	}
}

// decide takes the fast path only when the read found a row, and lets the failure
// of that read out instead of applying over it.
func TestDecide_appliesOnlyWhenNothingWasRecordedUnderTheKey(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	fresh := bookWith(t, "1000.00")
	job := jobOf(t, fresh, wager.KindBet, "25.00")
	decided, err := service(t, fresh).decide(context.Background(), fresh, job)
	if err != nil {
		t.Fatalf("decide with nothing recorded = %v, want nil", err)
	}
	if decided.result.Status != wager.Processed {
		t.Fatalf("status = %s, want PROCESSED: decide applied the operation", decided.result.Status)
	}
	replaying := bookWith(t, "1000.00")
	replaying.keep(t, cmd, processedState(t, cmd))
	replayed, err := service(t, replaying).decide(context.Background(), replaying, job)
	if err != nil {
		t.Fatalf("decide with a recorded outcome = %v, want nil", err)
	}
	if !replayed.result.IdempotentReplay {
		t.Fatalf("replay marker = %t, want true: decide took the recorded outcome", replayed.result.IdempotentReplay)
	}
}

// recorded reports the absence as "nothing found" and never as a failure, because
// the absence is the ordinary answer for a key arriving for the first time.
func TestRecorded_readsTheAbsenceAsNothingFoundAndNotAsAFailure(t *testing.T) {
	t.Parallel()
	fresh := bookWith(t, "1000.00")
	_, found, err := service(t, fresh).recorded(context.Background(), fresh, jobOf(t, fresh, wager.KindBet, "25.00"))
	if err != nil {
		t.Fatalf("recorded with nothing under the key = %v, want nil", err)
	}
	if found {
		t.Fatalf("found = %t, want false with nothing under the key", found)
	}
}

func TestRecorded_answersTheOutcomeAlreadyWrittenUnderTheKey(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	stored := bookWith(t, "1000.00")
	stored.keep(t, cmd, processedState(t, cmd))
	decided, found, err := service(t, stored).recorded(context.Background(), stored, jobOf(t, stored, wager.KindBet, "25.00"))
	if err != nil || !found {
		t.Fatalf("recorded of a stored row = (%v, %t), want (nil, true)", err, found)
	}
	if !decided.result.IdempotentReplay {
		t.Fatalf("replay marker of the row recorded reads = %t, want true", decided.result.IdempotentReplay)
	}
}

// outcomeOf answers what a row already under that key means for the arrival being
// decided. None of its arms moves money or writes a row.
func TestOutcomeOf_answersTheConflictForAnotherBodyUnderTheSameKey(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	conflict, err := outcomeOf(processedState(t, cmd), "another hash")
	if err != nil {
		t.Fatalf("outcomeOf another body = %v, want the conflict as the settlement", err)
	}
	assertToken(t, conflict.rejection, wager.IdempotencyConflict)
}

func TestOutcomeOf_replaysTheRecordedOutcomeForTheSameBody(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	replay, err := outcomeOf(processedState(t, cmd), bodyhash.Of(cmd.business()))
	if err != nil {
		t.Fatalf("outcomeOf the same body = %v, want nil", err)
	}
	if !replay.result.IdempotentReplay || replay.rejection != nil {
		t.Fatalf("settlement of a processed row = %+v, want a replay with no rejection", replay)
	}
}

// A row that is neither terminal nor a wait has no outcome to answer, so the
// arrival is told to come back instead of being answered from a decision nobody
// made yet.
func TestOutcomeOf_answersTransientWhileTheRecordedOperationIsNotTerminal(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	inFlight := processedState(t, cmd)
	inFlight.Status = wager.Pending
	if _, err := outcomeOf(inFlight, bodyhash.Of(cmd.business())); !errors.Is(err, ErrOutcomeInFlight) {
		t.Fatalf("outcomeOf a row that is not terminal = %v, want %v", err, ErrOutcomeInFlight)
	}
}

// An incomplete row is neither an absence nor a conflict: it is a row the aggregate
// refuses to be rebuilt from, and that refusal leaves as the failure.
func TestOutcomeOf_answersTheRefusalOfARowTheDomainCannotTakeBack(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	broken := processedState(t, cmd)
	broken.PlayerID = identity.PlayerID{}
	if _, err := outcomeOf(broken, bodyhash.Of(cmd.business())); !errors.Is(err, wager.ErrIncompleteTransaction) {
		t.Fatalf("outcomeOf an incomplete row = %v, want %v", err, wager.ErrIncompleteTransaction)
	}
}

// refusalOf answers the recorded refusal marked as a replay, and nothing at all
// when the recorded outcome was not a refusal.
func TestRefusalOf_marksOnlyARecordedRejection(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	rejected, err := wager.Rehydrate(rejectedState(t, cmd, wager.InsufficientFunds))
	if err != nil {
		t.Fatalf("Rehydrate of the rejected row = %v, want nil", err)
	}
	outcome, _ := rejected.Replay()
	refusal := refusalOf(outcome)
	assertToken(t, refusal, wager.InsufficientFunds)
	var marked Replayed
	if !errors.As(refusal, &marked) {
		t.Fatalf("refusal = %v, want it marked as a replay", refusal)
	}
	processed, err := wager.Rehydrate(processedState(t, cmd))
	if err != nil {
		t.Fatalf("Rehydrate of the processed row = %v, want nil", err)
	}
	settled, _ := processed.Replay()
	if got := refusalOf(settled); got != nil {
		t.Fatalf("refusalOf a processed outcome = %v, want nil", got)
	}
}

// apply settles over the locked wallet: the accepted operation reaches the arm that
// records the movement.
func TestApply_recordsTheMovementOfAnAcceptedOperation(t *testing.T) {
	t.Parallel()
	settled := bookWith(t, "1000.00")
	decided, err := service(t, settled).apply(context.Background(), settled, jobOf(t, settled, wager.KindBet, "25.00"))
	if err != nil {
		t.Fatalf("apply of an accepted bet = %v, want nil", err)
	}
	if decided.result.Status != wager.Processed || decided.rejection != nil {
		t.Fatalf("settlement of apply = %+v, want PROCESSED with no rejection", decided)
	}
}

// A rule that refuses sends it to the other arm, which records the refusal and
// answers it as the settlement rather than as the failure of the work.
func TestApply_recordsTheRefusalOfARule(t *testing.T) {
	t.Parallel()
	refused := bookWith(t, "10.00")
	rejected, err := service(t, refused).apply(context.Background(), refused, jobOf(t, refused, wager.KindBet, "25.00"))
	if err != nil {
		t.Fatalf("apply of a bet past the balance = %v, want the rejection as the settlement", err)
	}
	assertToken(t, rejected.rejection, wager.InsufficientFunds)
}

// The absence of the wallet becomes the token of the catalog. It happens before
// the transaction exists, and the row could not exist anyway: its foreign key
// names a wallet that is not there.
func TestAbsentWallet_turnsOnlyTheAbsenceIntoTheTokenOfTheCatalog(t *testing.T) {
	t.Parallel()
	absence := fmt.Errorf("lock wallet: %w", storage.ErrWalletNotFound)
	assertToken(t, absentWallet(absence), wager.WalletNotFound)
	broken := errors.New("acquire connection: refused")
	if got := absentWallet(broken); !errors.Is(got, broken) {
		t.Fatalf("absentWallet of a failure = %v, want %v untouched", got, broken)
	}
}

// reject records the refusal of a rule. Every rejection that reaches it comes from
// the action over a wallet already locked, so the row is writable.
func TestReject_writesTheRowOfARejectionOfARule(t *testing.T) {
	t.Parallel()
	recording := bookWith(t, "1000.00")
	refusal := wager.NewRejection(wager.InsufficientFunds, nil)
	decided, err := service(t, recording).reject(context.Background(), recording, jobOf(t, recording, wager.KindBet, "25.00"), refusal)
	if err != nil {
		t.Fatalf("reject of a rule refusal = %v, want the rejection as the settlement", err)
	}
	if decided.result.Status != wager.Rejected {
		t.Fatalf("status = %s, want REJECTED", decided.result.Status)
	}
	// reject runs inside the unit of work, so the row is staged and becomes
	// findable at the commit the caller owns.
	assertStaged(t, recording, 1, 0, 0)
}

// Anything that is not a rejection is not its business, and it leaves without a
// row: a broken connection is not a refusal anybody can record.
func TestReject_writesNoRowForAFailureItDoesNotOwn(t *testing.T) {
	t.Parallel()
	passing := bookWith(t, "1000.00")
	broken := errors.New("acquire connection: refused")
	_, err := service(t, passing).reject(context.Background(), passing, jobOf(t, passing, wager.KindBet, "25.00"), broken)
	if !errors.Is(err, broken) {
		t.Fatalf("reject of a failure = %v, want %v", err, broken)
	}
	assertStaged(t, passing, 0, 0, 0)
}

// record puts the movement in the commit, and a refused write leaves no settlement
// behind for the caller to answer with.
func TestRecord_answersNoSettlementWhenTheWriteIsRefused(t *testing.T) {
	t.Parallel()
	settling := bookWith(t, "1000.00")
	job, m := movedBy(t, settling, wager.KindBet, "25.00")
	decided, err := service(t, settling).record(context.Background(), settling, job, m)
	if err != nil {
		t.Fatalf("record of an accepted bet = %v, want nil", err)
	}
	if decided.result.Status != wager.Processed || decided.rejection != nil {
		t.Fatalf("settlement = %+v, want PROCESSED with no rejection", decided)
	}

	refusing := bookWith(t, "1000.00")
	job, m = movedBy(t, refusing, wager.KindBet, "25.00")
	refusing.insertErr = errors.New("insert transaction: refused")
	refused, err := service(t, refusing).record(context.Background(), refusing, job, m)
	if !errors.Is(err, refusing.insertErr) {
		t.Fatalf("record with the insert refused = %v, want %v", err, refusing.insertErr)
	}
	if refused != (settlement{}) {
		t.Fatalf("settlement beside the failure = %+v, want the zero value", refused)
	}
}

// write has one branch and it is the LOSS: no balance moves, so the version stays
// where it was and the transaction row is all there is to write.
func TestWrite_writesOnlyTheTransactionRowWhenNoBalanceMoves(t *testing.T) {
	t.Parallel()
	loss := bookWith(t, "1000.00")
	job, m := processedBy(t, loss, wager.KindLoss, "0.00")
	if err := write(context.Background(), loss, job, m); err != nil {
		t.Fatalf("write of a LOSS = %v, want nil", err)
	}
	assertStaged(t, loss, 1, 0, 0)

	bet := bookWith(t, "1000.00")
	job, m = processedBy(t, bet, wager.KindBet, "25.00")
	if err := write(context.Background(), bet, job, m); err != nil {
		t.Fatalf("write of a BET = %v, want nil", err)
	}
	assertStaged(t, bet, 1, 1, 1)
}

func assertStaged(t *testing.T, ledgerBook *book, transactions, entries, balances int) {
	t.Helper()
	got := []int{len(ledgerBook.staged), len(ledgerBook.entries), len(ledgerBook.balances)}
	want := []int{transactions, entries, balances}
	for at, label := range []string{"transactions", "entries", "balance writes"} {
		if got[at] != want[at] {
			t.Fatalf("staged %s = %d, want %d", label, got[at], want[at])
		}
	}
}

// jobOf is the transaction built for that command, which is what every helper of
// the use case below the build step is given.
func jobOf(t *testing.T, ledgerBook *book, kind wager.Kind, amount string) pending {
	t.Helper()
	job, err := service(t, ledgerBook).pending(commandOf(t, kind, amount))
	if err != nil {
		t.Fatalf("pending = %v, want nil", err)
	}
	return job
}

// movedBy is the operation together with what the action decided over the locked
// wallet, which is the state record and write are given.
func movedBy(t *testing.T, ledgerBook *book, kind wager.Kind, amount string) (pending, moved) {
	t.Helper()
	job := jobOf(t, ledgerBook, kind, amount)
	state, err := ledgerBook.Wallets().GetForUpdate(context.Background(), job.op.WalletID())
	if err != nil {
		t.Fatalf("GetForUpdate = %v, want nil", err)
	}
	owner, err := wallet.Rehydrate(state)
	if err != nil {
		t.Fatalf("Rehydrate of the locked wallet = %v, want nil", err)
	}
	decision, err := decisionOf(owner, job, wager.Reference{})
	if err != nil {
		t.Fatalf("decisionOf = %v, want nil", err)
	}
	return job, moved{owner: owner, decision: decision, readVersion: state.Version}
}

// processedBy is the same, with the transaction already moved to its terminal
// status: PENDING is not a writable status, so write is only ever given a row that
// record already processed.
func processedBy(t *testing.T, ledgerBook *book, kind wager.Kind, amount string) (pending, moved) {
	t.Helper()
	job, m := movedBy(t, ledgerBook, kind, amount)
	if err := job.op.Process(m.decision.Balance(), job.at()); err != nil {
		t.Fatalf("Process = %v, want nil", err)
	}
	return job, m
}

// raced reports whether a winning row can answer the violation. Only the two
// tokens of the race qualify, and a failure that is not a rejection at all never
// does.
func TestRaced_answersOnlyForTheTwoTokensOfTheRace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		expect bool
	}{
		{name: "the key index", err: wager.NewRejection(wager.IdempotencyConflict, nil), expect: true},
		{name: "the external id index", err: wager.NewRejection(wager.DuplicateExternalTransaction, nil), expect: true},
		{name: "another token", err: wager.NewRejection(wager.InsufficientFunds, nil)},
		{name: "a failure that is no rejection", err: errors.New("acquire connection: refused")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := raced(fmt.Errorf("insert transaction: %w", tc.err)); got != tc.expect {
				t.Fatalf("raced of %s = %t, want %t", tc.name, got, tc.expect)
			}
		})
	}
}

// vanished answers what an absent winning row means for each violation: the
// duplicate external id stands on its own, and under the key index the absence has
// no explanation and is transient.
func TestVanished_readsTheAbsentWinnerAgainstTheViolationThatSentItThere(t *testing.T) {
	t.Parallel()
	absence := fmt.Errorf("read transaction by key: %w", storage.ErrTransactionNotFound)
	duplicate := wager.NewRejection(wager.DuplicateExternalTransaction, nil)
	if got := vanished(absence, duplicate); !errors.Is(got, duplicate) {
		t.Fatalf("vanished under the external id index = %v, want %v", got, duplicate)
	}
	conflict := wager.NewRejection(wager.IdempotencyConflict, nil)
	if got := vanished(absence, conflict); !errors.Is(got, ErrRaceUnresolved) {
		t.Fatalf("vanished under the key index = %v, want %v", got, ErrRaceUnresolved)
	}
	broken := errors.New("acquire connection: refused")
	if got := vanished(broken, duplicate); !errors.Is(got, broken) {
		t.Fatalf("vanished of a failed read = %v, want %v untouched", got, broken)
	}
}

// afterRace hands the failure straight back when no winning row can answer it, and
// otherwise answers from the row the read found outside the rolled-back
// transaction.
func TestAfterRace_answersTheFailureItselfWhenNoWinningRowCanExplainIt(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	ledgerBook := bookWith(t, "1000.00")
	job := jobOf(t, ledgerBook, wager.KindBet, "25.00")
	broken := errors.New("acquire connection: refused")
	if _, err := service(t, ledgerBook).afterRace(context.Background(), job, broken); !errors.Is(err, broken) {
		t.Fatalf("afterRace of a failure no row explains = %v, want %v", err, broken)
	}
	if ledgerBook.outsideReads != 0 {
		t.Fatalf("reads outside the transaction = %d, want 0: nothing was raced", ledgerBook.outsideReads)
	}
	winner := processedState(t, cmd)
	ledgerBook.outside = &winner
	answered, err := service(t, ledgerBook).afterRace(context.Background(), job, wager.NewRejection(wager.IdempotencyConflict, nil))
	if err != nil {
		t.Fatalf("afterRace of the loser of the key index = %v, want the replay", err)
	}
	if !answered.IdempotentReplay {
		t.Fatalf("replay marker = %t, want true", answered.IdempotentReplay)
	}
}

// The six helpers below carry no branch, so no mutant of theirs can be made to
// survive. What can go wrong in them is a field landing in the wrong place, and
// what catches that is comparing the whole value they build.

// spec is the only place the command becomes what the aggregate accepts. The
// comparison is on the whole struct so that a field swapped for its neighbour
// fails here instead of surfacing as a wrong row much later.
func TestSpec_carriesEveryFieldOfTheCommandIntoTheSpec(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	got := cmd.spec(transactionOf(t), "the-hash", frozen)
	want := wager.ExternalSpec{
		ID:             transactionOf(t),
		ProviderID:     cmd.ProviderID,
		ExternalID:     cmd.ExternalID,
		IdempotencyKey: cmd.IdempotencyKey,
		BodyHash:       "the-hash",
		PlayerID:       cmd.PlayerID,
		WalletID:       cmd.WalletID,
		RoundID:        cmd.RoundID,
		GameID:         cmd.GameID,
		Kind:           cmd.Kind,
		Amount:         cmd.Amount,
		At:             frozen,
	}
	if got != want {
		t.Fatalf("spec = %+v, want %+v", got, want)
	}
}

// This delivery settles nothing that cites another operation, so the spec it
// builds names no reference. The wait is what would fill it.
func TestSpec_namesNoCitedOperation(t *testing.T) {
	t.Parallel()
	got := commandOf(t, wager.KindBet, "25.00").spec(transactionOf(t), "the-hash", frozen)
	if !got.ReferenceExternalID.IsZero() {
		t.Fatalf("reference = %s, want none: this delivery cites nothing", got.ReferenceExternalID)
	}
}

// The hash covers the business and nothing else. The key is scoped to the
// provider and kept out on purpose: HTTP and the queue carry it differently and
// must still produce one hash.
func TestBusiness_leavesTheKeyAndTheReferenceOutOfTheHashInput(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	got := cmd.business()
	want := bodyhash.Business{
		ProviderID: cmd.ProviderID,
		ExternalID: cmd.ExternalID,
		PlayerID:   cmd.PlayerID,
		WalletID:   cmd.WalletID,
		RoundID:    cmd.RoundID,
		GameID:     cmd.GameID,
		Kind:       cmd.Kind,
		Amount:     cmd.Amount,
	}
	if got != want {
		t.Fatalf("business = %+v, want %+v", got, want)
	}
}

// Two commands that differ only by the key are one business, and the hash says
// so. This is what makes the same operation arriving twice a replay instead of a
// conflict.
func TestBusiness_answersTheSameValueForAnotherKey(t *testing.T) {
	t.Parallel()
	first := commandOf(t, wager.KindBet, "25.00")
	second := commandOf(t, wager.KindBet, "25.00")
	second.IdempotencyKey = keyNamed(t, "another-key")
	if first.business() != second.business() {
		t.Fatalf("business of another key = %+v, want the %+v of the same operation", second.business(), first.business())
	}
}

// resultOf is what the border answers, read off the transaction that was just
// recorded. It never says replay: only replayOf does.
func TestResultOf_carriesTheRecordedOutcomeOfTheTransaction(t *testing.T) {
	t.Parallel()
	cmd := commandOf(t, wager.KindBet, "25.00")
	recorded := rehydrated(t, processedState(t, cmd))
	got := resultOf(recorded)
	want := Result{
		TransactionID:   transactionOf(t),
		Kind:            cmd.Kind,
		Status:          wager.Processed,
		ExternalID:      cmd.ExternalID,
		Amount:          cmd.Amount,
		ObservedBalance: moneyOf(t, observedBackThen, "BRL"),
	}
	if got != want {
		t.Fatalf("resultOf = %+v, want %+v", got, want)
	}
}

// The replay answers the outcome that was recorded and the balance observed back
// then, and the marker is the single thing that tells it from the first
// completion.
func TestReplayOf_marksTheSameOutcomeAsAReplay(t *testing.T) {
	t.Parallel()
	recorded := rehydrated(t, processedState(t, commandOf(t, wager.KindBet, "25.00")))
	want := resultOf(recorded)
	want.IdempotentReplay = true
	if got := replayOf(recorded); got != want {
		t.Fatalf("replayOf = %+v, want %+v", got, want)
	}
}

// The instant comes from the injected clock and is read once, so the transaction
// and its entry are stamped with the same one.
func TestAt_answersTheInstantOfTheInjectedClock(t *testing.T) {
	t.Parallel()
	job := jobOf(t, bookWith(t, "1000.00"), wager.KindBet, "25.00")
	if got := job.at(); !got.Equal(frozen) {
		t.Fatalf("instant = %s, want the injected %s", got, frozen)
	}
}

// settle owns the transactional boundary, and the two outcomes it has to keep
// apart share no branch: a rule that refused is durable and commits, while a
// failure undoes the whole SQL transaction.
func TestSettle_commitsADurableRejectionAndUndoesAFailure(t *testing.T) {
	t.Parallel()
	refusing := bookWith(t, "10.00")
	decided, err := service(t, refusing).settle(context.Background(), jobOf(t, refusing, wager.KindBet, "25.00"))
	if err != nil {
		t.Fatalf("settle of a rule refusal = %v, want the rejection in the settlement", err)
	}
	assertToken(t, decided.rejection, wager.InsufficientFunds)
	assertCommits(t, refusing, 1, 0)

	failing := bookWith(t, "1000.00")
	failing.lostWrite = true
	_, err = service(t, failing).settle(context.Background(), jobOf(t, failing, wager.KindBet, "25.00"))
	if !errors.Is(err, storage.ErrLostWrite) {
		t.Fatalf("settle with the balance write past the lock = %v, want %v", err, storage.ErrLostWrite)
	}
	assertCommits(t, failing, 0, 1)
}

func assertCommits(t *testing.T, ledgerBook *book, commits, rollbacks int) {
	t.Helper()
	if ledgerBook.commits != commits {
		t.Fatalf("commits = %d, want %d", ledgerBook.commits, commits)
	}
	if ledgerBook.rollbacks != rollbacks {
		t.Fatalf("rollbacks = %d, want %d", ledgerBook.rollbacks, rollbacks)
	}
}

func rehydrated(t *testing.T, state wager.State) *wager.Transaction {
	t.Helper()
	recorded, err := wager.Rehydrate(state)
	if err != nil {
		t.Fatalf("Rehydrate of the recorded state = %v, want nil", err)
	}
	return recorded
}

func keyNamed(t *testing.T, text string) identity.IdempotencyKey {
	t.Helper()
	parsed, err := identity.ParseIdempotencyKey(text)
	if err != nil {
		t.Fatalf("ParseIdempotencyKey = %v, want nil", err)
	}
	return parsed
}
