package resolvereference

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// The awaited operation arrived, so the wait is carried out in this very commit:
// the balance moves, the entry is written and the row ends PROCESSED.
func TestResolve_resumesTheOperationWhenTheCitedOneArrived(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		kind      wager.Kind
		amount    string
		cited     wager.State
		balance   string
		direction ledger.Direction
	}{
		{
			name:      "the awaited bet arrived and the win is credited",
			kind:      wager.KindWin,
			amount:    "50.00",
			cited:     citedProcessed(t, wager.KindBet, "25.00"),
			balance:   "1050.00",
			direction: ledger.Credit,
		},
		{
			name:      "the awaited bet arrived and the refund credits it back",
			kind:      wager.KindRefund,
			amount:    "25.00",
			cited:     citedProcessed(t, wager.KindBet, "25.00"),
			balance:   "1025.00",
			direction: ledger.Credit,
		},
		{
			name:      "the awaited win arrived and the rollback debits it",
			kind:      wager.KindRollback,
			amount:    "50.00",
			cited:     citedProcessed(t, wager.KindWin, "50.00"),
			balance:   "950.00",
			direction: ledger.Debit,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := bookWith(t, waiting(t, tc.kind, tc.amount))
			book.cite(t, tc.cited)
			resolve(t, book)
			state := book.decided(t)
			if state.Status != wager.Processed {
				t.Fatalf("status = %s, want PROCESSED", state.Status)
			}
			if state.ObservedBalance.Amount() != tc.balance {
				t.Fatalf("observed balance = %s, want %s", state.ObservedBalance.Amount(), tc.balance)
			}
			assertEntry(t, book, tc.direction, tc.amount, tc.balance)
		})
	}
}

// The scan only chooses: a row that left the wait before the claim is not decided
// a second time, and what the first conclusion recorded stands.
func TestResolve_decidesNothingOverARowThatIsNoLongerWaiting(t *testing.T) {
	t.Parallel()
	settled := waiting(t, wager.KindWin, "50.00")
	settled.Status = wager.Rejected
	settled.FailureCode = wager.ReferenceNotFound
	book := bookWith(t, settled)
	book.cite(t, citedProcessed(t, wager.KindBet, "25.00"))
	resolve(t, book)
	assertNothingWritten(t, book)
}

// A row another replica is holding is skipped and not waited on, and it is no
// failure: the next scan offers it again.
func TestResolve_skipsTheWaitAnotherReplicaIsHolding(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	book.claimed = true
	resolve(t, book)
	assertNothingWritten(t, book)
}

// The wallet is taken before the row of the wait, the order of every operation
// over this wallet, and the cited operation is read under that lock.
func TestResolve_takesTheWalletBeforeTheRowOfTheWait(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindRefund, "25.00"))
	book.cite(t, citedProcessed(t, wager.KindBet, "25.00"))
	resolve(t, book)
	want := []string{"wallet", "claim", "cited", "reversal"}
	if !slices.Equal(book.calls, want) {
		t.Fatalf("asked in the order %v, want %v", book.calls, want)
	}
}

// At the deadline the wait is closed with the token of what was missing, and the
// two cases are told apart here because the domain has no clock to tell them
// apart with.
func TestResolve_closesTheExpiredWaitWithTheTokenOfWhatWasMissing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		cited *wager.State
		want  wager.FailureCode
	}{
		{name: "the cited operation never arrived", want: wager.ReferenceNotFound},
		{name: "the cited operation arrived and is itself waiting", cited: citedWaiting(t), want: wager.ReferenceNotProcessed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
			if tc.cited != nil {
				book.cite(t, *tc.cited)
			}
			// The instant of the deadline itself, which is the boundary the rule
			// fixes: the attempt at the deadline is the one that closes the wait.
			resolveAt(t, book, deadline)
			state := book.decided(t)
			if state.Status != wager.Rejected || state.FailureCode != tc.want {
				t.Fatalf("decided %s with %s, want REJECTED with %s", state.Status, state.FailureCode, tc.want)
			}
			assertNothingMoved(t, book)
		})
	}
}

func TestResolve_recordsTheOutcomeAndTheBalanceOfAWaitThatWasCarriedOut(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	book.cite(t, citedProcessed(t, wager.KindBet, "25.00"))
	resolve(t, book)
	assertEvents(t, book, event.TypeProcessed, event.TypeBalanceChanged)
}

func TestResolve_recordsOnlyTheRejectionOfAWaitTheClockClosed(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	resolveAt(t, book, deadline)
	assertEvents(t, book, event.TypeRejected)
}

func TestResolve_recordsNoEventForAnAttemptThatOnlyMovedTheSchedule(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	resolveAt(t, book, deadline.Add(-time.Nanosecond))
	assertEvents(t, book)
}

func assertEvents(t *testing.T, ledgerBook *book, want ...event.Type) {
	t.Helper()
	if len(ledgerBook.events) != len(want) {
		t.Fatalf("events recorded = %d, want %d", len(ledgerBook.events), len(want))
	}
	for index, expected := range want {
		if ledgerBook.events[index].Type() != expected {
			t.Fatalf("event %d = %s, want %s", index, ledgerBook.events[index].Type(), expected)
		}
	}
}

// One instant before the deadline the wait is not closed: it is scheduled again.
func TestResolve_schedulesAgainWhileTheDeadlineHasNotCome(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	resolveAt(t, book, deadline.Add(-time.Nanosecond))
	if len(book.ended) != 0 {
		t.Fatalf("waits ended = %d, want 0 while the deadline has not come", len(book.ended))
	}
	if len(book.rescheduled) != 1 {
		t.Fatalf("reschedules = %d, want 1", len(book.rescheduled))
	}
	assertNothingMoved(t, book)
}

// The window of the backoff is the attempts already made, which is what the
// claimed row carries.
func TestResolve_schedulesFromTheAttemptsTheRowAlreadyCarries(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	book.attempts = 4
	resolveAt(t, book, entered)
	if len(book.rescheduled) != 1 || book.rescheduled[0].attempts != 4 {
		t.Fatalf("scheduled from %+v, want the 4 attempts the row carries", book.rescheduled)
	}
}

// A draw longer than what is left of the wait schedules the deadline itself: past
// it, the row would sit expired with nobody due to close it until the attempt
// after that.
func TestResolve_neverSchedulesPastTheDeadline(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	// Six attempts put the window at its ceiling, which is longer than the thirty
	// seconds left of the wait.
	book.attempts = 6
	resolveAt(t, book, deadline.Add(-30*time.Second))
	if len(book.rescheduled) != 1 {
		t.Fatalf("reschedules = %d, want 1", len(book.rescheduled))
	}
	if !book.rescheduled[0].next.Equal(deadline) {
		t.Fatalf("next attempt = %s, want the deadline at %s", book.rescheduled[0].next, deadline)
	}
}

// A cited operation that already ended badly closes the wait at once: no later
// arrival changes what it is, so the deadline is not waited for.
func TestResolve_closesTheWaitAtOnceWhenTheCitedOperationEndedBadly(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindRefund, "25.00"))
	ended := citedProcessed(t, wager.KindBet, "25.00")
	ended.Status = wager.Failed
	ended.FailureCode = wager.ReferenceNotFound
	ended.ObservedBalance = money.Money{}
	book.cite(t, ended)
	resolveAt(t, book, entered)
	state := book.decided(t)
	if state.Status != wager.Rejected || state.FailureCode != wager.ReferenceUnsuccessful {
		t.Fatalf("decided %s with %s, want REJECTED with REFERENCE_UNSUCCESSFUL", state.Status, state.FailureCode)
	}
	assertNothingMoved(t, book)
}

// A cited operation that arrived and does not close with the one waiting is
// refused with the token of the mismatch, also without waiting for the deadline.
func TestResolve_closesTheWaitWhenTheCitedOperationDoesNotCloseWithIt(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	other := citedProcessed(t, wager.KindBet, "25.00")
	round, err := identity.ParseRoundID("round-2")
	if err != nil {
		t.Fatalf("ParseRoundID = %v, want nil", err)
	}
	other.RoundID = round
	book.cite(t, other)
	resolveAt(t, book, entered)
	state := book.decided(t)
	if state.Status != wager.Rejected || state.FailureCode != wager.ReferenceMismatch {
		t.Fatalf("decided %s with %s, want REJECTED with REFERENCE_MISMATCH", state.Status, state.FailureCode)
	}
	assertNothingMoved(t, book)
}

// The second reversal of the same operation is refused with no entry, and the
// question that decides it is the one asked under the lock.
func TestResolve_closesTheWaitWhenTheCitedOperationIsAlreadyReversed(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindRollback, "25.00"))
	book.cite(t, citedProcessed(t, wager.KindBet, "25.00"))
	book.reversed = true
	resolveAt(t, book, entered)
	state := book.decided(t)
	if state.Status != wager.Rejected || state.FailureCode != wager.AlreadyReversed {
		t.Fatalf("decided %s with %s, want REJECTED with ALREADY_REVERSED", state.Status, state.FailureCode)
	}
	assertNothingMoved(t, book)
}

// The failure of the commit leaves nothing recorded: the wait stays where it was,
// available to the next scan. Here the write over the row is the thing that
// failed, so the schedule cannot be moved either.
func TestResolve_leavesTheWaitUntouchedWhenTheCommitFails(t *testing.T) {
	t.Parallel()
	broken := errors.New("postgres: connection reset by peer")
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	book.cite(t, citedProcessed(t, wager.KindBet, "25.00"))
	book.updateErr = broken
	if err := service(t, book, entered).Resolve(context.Background(), candidateOf(t)); !errors.Is(err, broken) {
		t.Fatalf("Resolve = %v, want %v", err, broken)
	}
	if book.commits != 0 {
		t.Fatalf("commits = %d, want none when the write is what failed", book.commits)
	}
	assertNothingWritten(t, book)
}

// An attempt that failed before deciding anything still moves the schedule of the
// row, in a commit of its own: the transaction that failed took the counted
// attempt and the moved schedule down with it, and a row left on its old schedule
// is claimed again on the very next tick.
func TestResolve_movesTheScheduleOfAnAttemptThatFailed(t *testing.T) {
	t.Parallel()
	broken := errors.New("mint: entropy exhausted")
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	book.attempts = 2
	failing := New(book, brokenMinter{err: broken}, at(entered), schedule{})
	if err := failing.Resolve(context.Background(), candidateOf(t)); !errors.Is(err, broken) {
		t.Fatalf("Resolve with an unmintable identity = %v, want %v", err, broken)
	}
	assertNothingMoved(t, book)
	if len(book.ended) != 0 {
		t.Fatalf("waits ended by a failed attempt = %d, want none", len(book.ended))
	}
	if len(book.rescheduled) != 1 || book.rescheduled[0].attempts != 2 {
		t.Fatalf("schedule moved by a failed attempt = %+v, want one write off the 2 attempts the row carries", book.rescheduled)
	}
}

// The shutdown is not a failed attempt. A row whose turn was cancelled keeps its
// schedule, so the replica that scans next takes it at the instant it was already
// due rather than at the end of a backoff nobody earned.
func TestResolve_keepsTheScheduleOfAnAttemptTheShutdownCancelled(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	book.cite(t, citedProcessed(t, wager.KindBet, "25.00"))
	book.updateErr = context.Canceled
	if err := service(t, book, entered).Resolve(context.Background(), candidateOf(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve of a cancelled turn = %v, want %v", err, context.Canceled)
	}
	assertNothingWritten(t, book)
	if book.commits != 0 {
		t.Fatalf("commits after a cancelled turn = %d, want none", book.commits)
	}
}

// The scan takes no lock, so two replicas reading the same instant both offer the
// same row. The claim is what tells them apart: the second one finds a row whose
// next attempt the first has already moved, and decides nothing over it.
func TestResolve_decidesNothingOverARowWhoseNextAttemptWasMoved(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	book.row.State.NextAttemptAt = entered.Add(time.Minute)
	if err := service(t, book, entered).Resolve(context.Background(), candidateOf(t)); err != nil {
		t.Fatalf("Resolve of a row no longer due = %v, want nil", err)
	}
	assertNothingWritten(t, book)
}

// A row in the wait always names the operation it waits for. One that does not is
// a row nothing should have written, and it is a defect of ours rather than a
// rule refusing anything.
func TestCitedFor_refusesAWaitThatNamesNoOperation(t *testing.T) {
	t.Parallel()
	state := waiting(t, wager.KindWin, "50.00")
	state.ReferenceExternalID = identity.ExternalTransactionID{}
	book := bookWith(t, state)
	err := service(t, book, entered).Resolve(context.Background(), candidateOf(t))
	if !errors.Is(err, ErrKindNotWaited) {
		t.Fatalf("Resolve of a wait naming no operation = %v, want %v", err, ErrKindNotWaited)
	}
}

// Only the three kinds that can cite another operation reach the wait. A row in
// any other kind has no function to be decided by.
func TestDecisionOf_refusesAKindThatCannotBeWaiting(t *testing.T) {
	t.Parallel()
	turn := attempt{waiting: rehydrate(t, waitingOf(t, wager.KindBet, "25.00")), owner: walletWith(t)}
	if _, err := decisionOf(turn, wager.Reference{}, wager.Movement{}); !errors.Is(err, ErrKindNotWaited) {
		t.Fatalf("decisionOf of a BET = %v, want %v", err, ErrKindNotWaited)
	}
}

// Every kind that waits moves the balance, so a decision with no entry is one
// nothing here can write: writing the row without it would leave the balance and
// the ledger disagreeing at the commit.
func TestResume_refusesADecisionThatMovedNoBalance(t *testing.T) {
	t.Parallel()
	book := bookWith(t, waiting(t, wager.KindWin, "50.00"))
	turn := attempt{waiting: rehydrate(t, waiting(t, wager.KindWin, "50.00")), owner: walletWith(t)}
	err := service(t, book, entered).resume(context.Background(), book, turn, wager.Decision{}, wager.Movement{})
	if !errors.Is(err, ErrKindNotWaited) {
		t.Fatalf("resume of a decision with no entry = %v, want %v", err, ErrKindNotWaited)
	}
}

// expiredWith is what tells REFERENCE_NOT_FOUND from REFERENCE_NOT_PROCESSED,
// and the absence of the cited operation is the whole difference.
func TestExpiredWith_namesWhatWasMissingAtTheDeadline(t *testing.T) {
	t.Parallel()
	if got := expiredWith(wager.Reference{}); got != wager.ReferenceNotFound {
		t.Fatalf("expiredWith of an absent operation = %s, want REFERENCE_NOT_FOUND", got)
	}
	cited := rehydrate(t, citedWaitingState(t))
	if got := expiredWith(wager.Reference{Cited: cited}); got != wager.ReferenceNotProcessed {
		t.Fatalf("expiredWith of an operation still running = %s, want REFERENCE_NOT_PROCESSED", got)
	}
}

// The instants every case is written against: the wait entered at noon and its
// deadline is the fifteen minutes of the rule later. Nothing here reads a wall
// clock.
var (
	entered  = time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	deadline = entered.Add(15 * time.Minute)
)

func resolve(t *testing.T, ledgerBook *book) {
	t.Helper()
	resolveAt(t, ledgerBook, entered)
}

func resolveAt(t *testing.T, ledgerBook *book, now time.Time) {
	t.Helper()
	if err := service(t, ledgerBook, now).Resolve(context.Background(), candidateOf(t)); err != nil {
		t.Fatalf("Resolve = %v, want nil", err)
	}
}

func service(t *testing.T, ledgerBook *book, now time.Time) *Service {
	t.Helper()
	return New(ledgerBook, fixedMinter(t), at(now), schedule{})
}

// schedule is the backoff with no draw in it: it answers the window of the
// attempt whole, capped by the deadline, so a case reads instants and not a
// range.
type schedule struct{}

func (schedule) NextAttemptAt(attempts int64, now, deadline time.Time) time.Time {
	window := time.Second << attempts
	next := now.Add(window)
	if next.After(deadline) {
		return deadline
	}
	return next
}

// at is the clock of one case, frozen at the instant that case is about.
type at time.Time

func (a at) Now() time.Time {
	return time.Time(a)
}

func assertEntry(t *testing.T, ledgerBook *book, direction ledger.Direction, amount, after string) {
	t.Helper()
	if len(ledgerBook.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(ledgerBook.entries))
	}
	entry := ledgerBook.entries[0]
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

// assertNothingMoved is the whole of what a wait closed by a rule leaves behind:
// the row, and no movement of any kind.
func assertNothingMoved(t *testing.T, ledgerBook *book) {
	t.Helper()
	if len(ledgerBook.entries) != 0 {
		t.Fatalf("entries = %d, want 0: closing a wait produces none", len(ledgerBook.entries))
	}
	if len(ledgerBook.balances) != 0 {
		t.Fatalf("balance writes = %d, want 0: closing a wait moves nothing", len(ledgerBook.balances))
	}
}

func assertNothingWritten(t *testing.T, ledgerBook *book) {
	t.Helper()
	assertNothingMoved(t, ledgerBook)
	if len(ledgerBook.ended) != 0 || len(ledgerBook.rescheduled) != 0 {
		t.Fatalf("ended = %d and rescheduled = %d, want neither written", len(ledgerBook.ended), len(ledgerBook.rescheduled))
	}
}

// book is the in-memory persistence of one case: the wallet it locks, the row it
// claims and what each repository was asked to write.
type book struct {
	owner    *wallet.Wallet
	row      storage.Wait
	cited    *wager.State
	reversed bool

	// claimed plays the row already held by another replica, which the claim
	// skips instead of waiting on.
	claimed bool
	// attempts is what the claimed row carries, and the window of the backoff.
	attempts int64
	// updateErr is what the write over the waiting row answers, which is how a
	// case plays a commit that fails halfway.
	updateErr error

	ended       []*wager.Transaction
	rescheduled []schedulingWrite
	entries     []ledger.Entry
	events      []event.Envelope
	balances    []int64
	calls       []string

	commits   int
	rollbacks int
}

type schedulingWrite struct {
	next     time.Time
	attempts int64
}

func (b *book) cite(t *testing.T, state wager.State) {
	t.Helper()
	b.cited = &state
}

// decided answers the transaction the commit wrote, so a case reads the outcome
// the way a later read of the row would.
func (b *book) decided(t *testing.T) wager.State {
	t.Helper()
	if len(b.ended) != 1 {
		t.Fatalf("waits ended = %d, want 1", len(b.ended))
	}
	recorded := b.ended[0]
	return wager.State{
		Status:          recorded.Status(),
		FailureCode:     recorded.FailureCode(),
		ObservedBalance: recorded.ObservedBalance(),
	}
}

func (b *book) Within(_ context.Context, work func(storage.Tx) error) error {
	if err := work(b); err != nil {
		b.rollbacks++
		b.ended = nil
		b.rescheduled = nil
		b.entries = nil
		b.balances = nil
		b.events = nil
		return err
	}
	b.commits++
	return nil
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

func (b *book) Outbox() storage.Outbox {
	return outboxRows{book: b}
}

type outboxRows struct {
	book *book
}

func (r outboxRows) Insert(_ context.Context, envelope event.Envelope) error {
	r.book.events = append(r.book.events, envelope)
	return nil
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

func (r walletRows) UpdateBalance(_ context.Context, moved *wallet.Wallet, _ int64) error {
	r.book.balances = append(r.book.balances, moved.Balance().Cents())
	return nil
}

type transactionRows struct {
	book *book
}

func (r transactionRows) Insert(context.Context, *wager.Transaction) error {
	return nil
}

func (r transactionRows) ByKey(context.Context, identity.ProviderID, identity.IdempotencyKey) (wager.State, error) {
	return wager.State{}, storage.ErrTransactionNotFound
}

func (r transactionRows) ByExternalID(context.Context, identity.ProviderID, identity.ExternalTransactionID) (wager.State, error) {
	r.book.calls = append(r.book.calls, "cited")
	if r.book.cited == nil {
		return wager.State{}, storage.ErrTransactionNotFound
	}
	return *r.book.cited, nil
}

func (r transactionRows) HasProcessedReversal(context.Context, identity.ProviderID, identity.ExternalTransactionID) (bool, error) {
	r.book.calls = append(r.book.calls, "reversal")
	return r.book.reversed, nil
}

func (r transactionRows) ClaimWait(_ context.Context, _ identity.TransactionID, due time.Time) (storage.Wait, error) {
	r.book.calls = append(r.book.calls, "claim")
	if r.book.claimed {
		return storage.Wait{}, storage.ErrTransactionNotFound
	}
	// The predicate of the claim, as the statement writes it: a row whose next
	// attempt has been moved past this instant is not due to this replica.
	if due.Before(r.book.row.State.NextAttemptAt) {
		return storage.Wait{}, storage.ErrTransactionNotFound
	}
	return storage.Wait{State: r.book.row.State, Attempts: r.book.attempts}, nil
}

func (r transactionRows) EndWait(_ context.Context, decided *wager.Transaction) error {
	if r.book.updateErr != nil {
		return r.book.updateErr
	}
	r.book.ended = append(r.book.ended, decided)
	return nil
}

func (r transactionRows) RescheduleWait(_ context.Context, _ identity.TransactionID, next, _ time.Time) error {
	if r.book.updateErr != nil {
		return r.book.updateErr
	}
	r.book.rescheduled = append(r.book.rescheduled, schedulingWrite{next: next, attempts: r.book.attempts})
	return nil
}

type entryRows struct {
	book *book
}

func (r entryRows) Insert(_ context.Context, entry ledger.Entry) error {
	r.book.entries = append(r.book.entries, entry)
	return nil
}

// bookWith is the persistence of one case over the wallet every case starts
// from: a thousand covers the movements below and leaves each outcome readable.
func bookWith(t *testing.T, row wager.State) *book {
	t.Helper()
	return &book{owner: walletWith(t), row: storage.Wait{State: row}}
}

func candidateOf(t *testing.T) storage.WaitCandidate {
	t.Helper()
	return storage.WaitCandidate{TransactionID: transactionOf(t), WalletID: walletOf(t)}
}

// waiting is the row of one wait as the database holds it: PENDING_REFERENCE,
// naming the operation it waits for, with the deadline written on entry.
func waiting(t *testing.T, kind wager.Kind, amount string) wager.State {
	t.Helper()
	state := waitingOf(t, kind, amount)
	state.Status = wager.PendingReference
	state.NextAttemptAt = entered
	state.ReferenceDeadlineAt = deadline
	return state
}

// waitingOf is the row before any status is put on it, which is what lets a case
// build one in a status the wait never reaches.
func waitingOf(t *testing.T, kind wager.Kind, amount string) wager.State {
	t.Helper()
	return wager.State{
		ID:                  transactionOf(t),
		Kind:                kind,
		PlayerID:            playerOf(t),
		WalletID:            walletOf(t),
		Amount:              moneyOf(t, amount),
		ProviderID:          providerOf(t),
		ExternalID:          externalOf(t),
		IdempotencyKey:      keyOf(t),
		BodyHash:            "hash of the waiting operation",
		RoundID:             roundOf(t),
		GameID:              gameOf(t),
		ReferenceExternalID: citedExternalOf(t),
		Status:              wager.PendingReference,
		NextAttemptAt:       entered,
		ReferenceDeadlineAt: deadline,
		CreatedAt:           entered,
		UpdatedAt:           entered,
	}
}

// citedProcessed is the operation the wait names, as the commit that closed it
// left it: of the same provider, player, wallet and round as the one waiting.
func citedProcessed(t *testing.T, kind wager.Kind, amount string) wager.State {
	t.Helper()
	return wager.State{
		ID:              citedTransactionOf(t),
		Kind:            kind,
		PlayerID:        playerOf(t),
		WalletID:        walletOf(t),
		Amount:          moneyOf(t, amount),
		ProviderID:      providerOf(t),
		ExternalID:      citedExternalOf(t),
		IdempotencyKey:  citedKeyOf(t),
		BodyHash:        "hash of the cited operation",
		RoundID:         roundOf(t),
		GameID:          gameOf(t),
		Status:          wager.Processed,
		ObservedBalance: moneyOf(t, openingBalance),
		CreatedAt:       entered,
		UpdatedAt:       entered,
	}
}

// citedWaiting is a cited operation that arrived and is itself still running,
// which is the other half of what the deadline tells apart.
func citedWaiting(t *testing.T) *wager.State {
	t.Helper()
	state := citedWaitingState(t)
	return &state
}

func citedWaitingState(t *testing.T) wager.State {
	t.Helper()
	state := citedProcessed(t, wager.KindBet, "25.00")
	state.Status = wager.PendingReference
	state.ObservedBalance = money.Money{}
	state.NextAttemptAt = entered
	state.ReferenceDeadlineAt = deadline
	state.ReferenceExternalID = externalOf(t)
	return state
}

func rehydrate(t *testing.T, state wager.State) *wager.Transaction {
	t.Helper()
	rehydrated, err := wager.Rehydrate(state)
	if err != nil {
		t.Fatalf("wager.Rehydrate = %v, want nil", err)
	}
	return rehydrated
}

func walletWith(t *testing.T) *wallet.Wallet {
	t.Helper()
	opened, err := wallet.Rehydrate(wallet.State{
		ID:        walletOf(t),
		PlayerID:  playerOf(t),
		Balance:   moneyOf(t, openingBalance),
		Version:   1,
		CreatedAt: entered,
		UpdatedAt: entered,
	})
	if err != nil {
		t.Fatalf("wallet.Rehydrate = %v, want nil", err)
	}
	return opened
}

// minter hands out the fixed identities of one attempt. The events come out in
// order, so a case can name which row carries which identifier.
type minter struct {
	entry  identity.LedgerEntryID
	events []identity.EventID
	minted int
}

func (m *minter) EntryID() (identity.LedgerEntryID, error) {
	return m.entry, nil
}

func (m *minter) EventID() (identity.EventID, error) {
	minted := m.events[m.minted]
	m.minted++
	return minted, nil
}

func fixedMinter(t *testing.T) *minter {
	t.Helper()
	id, err := identity.ParseLedgerEntryID("44444444-4444-4444-8444-444444444444")
	if err != nil {
		t.Fatalf("ParseLedgerEntryID = %v, want nil", err)
	}
	return &minter{
		entry:  id,
		events: []identity.EventID{eventOf(t, "55555555-5555-4555-8555-555555555555"), eventOf(t, "66666666-6666-4666-8666-666666666666")},
	}
}

func eventOf(t *testing.T, text string) identity.EventID {
	t.Helper()
	id, err := identity.ParseEventID(text)
	if err != nil {
		t.Fatalf("ParseEventID = %v, want nil", err)
	}
	return id
}

type brokenMinter struct {
	err error
}

func (m brokenMinter) EntryID() (identity.LedgerEntryID, error) {
	return identity.LedgerEntryID{}, m.err
}

func (m brokenMinter) EventID() (identity.EventID, error) {
	return identity.EventID{}, m.err
}

// openingBalance is the balance every case starts from.
const openingBalance = "1000.00"

// Every case is in the one currency of the wallet: a mismatch of currency is a
// rule of the domain and has its own tests there.
func moneyOf(t *testing.T, amount string) money.Money {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
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
		t.Fatalf("ParsePlayerID = %v, want nil", err)
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

func citedTransactionOf(t *testing.T) identity.TransactionID {
	t.Helper()
	id, err := identity.ParseTransactionID("55555555-5555-4555-8555-555555555555")
	if err != nil {
		t.Fatalf("ParseTransactionID of the cited operation = %v, want nil", err)
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

func citedExternalOf(t *testing.T) identity.ExternalTransactionID {
	t.Helper()
	id, err := identity.ParseExternalTransactionID("cited-1")
	if err != nil {
		t.Fatalf("ParseExternalTransactionID of the cited operation = %v, want nil", err)
	}
	return id
}

func keyOf(t *testing.T) identity.IdempotencyKey {
	t.Helper()
	id, err := identity.ParseIdempotencyKey("key-1")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey = %v, want nil", err)
	}
	return id
}

func citedKeyOf(t *testing.T) identity.IdempotencyKey {
	t.Helper()
	id, err := identity.ParseIdempotencyKey("key-of-the-cited-operation")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey of the cited operation = %v, want nil", err)
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
