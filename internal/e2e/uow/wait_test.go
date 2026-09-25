//go:build integration

// The repository side of the reference wait against a real PostgreSQL: the
// lookup of the cited operation, the question of the reversal already processed,
// the write over a waiting row, the scan of the queue and the claim that two
// replicas cannot both win.
package uow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/platform/postgres"
)

func TestByExternalID_answersTheOperationThatProviderRecorded(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	external := "external-" + newID()
	recorded := rejection(t, opened.wallet, "key-"+newID(), external)
	insert(ctx, t, unit, recorded)
	state := citedOf(ctx, t, unit, recorded.ProviderID(), externalOf(t, external))
	if state.ID != recorded.ID() || state.Status != wager.Rejected {
		t.Fatalf("read back %s in %s, want %s in REJECTED", state.ID, state.Status, recorded.ID())
	}
}

// The provider is part of the query, so a cited operation of somebody else leaves
// by the same path as one that was never sent: neither answer tells a provider
// that a stranger holds the identifier it named.
func TestByExternalID_answersTheSameAbsenceForAnotherProviderAndForNothing(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	external := "external-" + newID()
	insert(ctx, t, unit, rejection(t, opened.wallet, "key-"+newID(), external))
	alien := citedError(ctx, t, unit, providerOf(t, "provider-b"), externalOf(t, external))
	absent := citedError(ctx, t, unit, providerOf(t, "provider-a"), externalOf(t, "external-"+newID()))
	if !errors.Is(alien, storage.ErrTransactionNotFound) || !errors.Is(absent, storage.ErrTransactionNotFound) {
		t.Fatalf("alien = %v and absent = %v, want both %v", alien, absent, storage.ErrTransactionNotFound)
	}
	if alien.Error() != absent.Error() {
		t.Fatalf("alien = %q and absent = %q, want the same answer for both", alien, absent)
	}
}

// Only a reversal that reached PROCESSED takes the place. One that ended REJECTED
// left no movement behind, so the operation it cited is still open to a reversal.
func TestHasProcessedReversal_countsOnlyTheReversalThatWentThrough(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	cited := externalOf(t, "external-"+newID())
	provider := providerOf(t, "provider-a")
	if got := reversed(ctx, t, unit, provider, cited); got {
		t.Fatalf("reversal of an operation nobody reversed = %t, want false", got)
	}
	insert(ctx, t, unit, rejectedReversal(t, opened.wallet, cited))
	if got := reversed(ctx, t, unit, provider, cited); got {
		t.Fatalf("reversal after a REJECTED one = %t, want false: it took no place", got)
	}
	insert(ctx, t, unit, processedReversal(t, opened.wallet, cited))
	if got := reversed(ctx, t, unit, provider, cited); !got {
		t.Fatalf("reversal after a PROCESSED one = %t, want true", got)
	}
}

// The deadline is written once, on entry. A later attempt writes the schedule
// alone, and leaves the deadline and every field of the status untouched.
func TestRescheduleWait_movesTheNextAttemptAndLeavesTheDeadlineAlone(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	waiting := storedWait(ctx, t, unit, opened.wallet)
	later := stamp().Add(30 * time.Second)
	reschedule(ctx, t, unit, waiting.ID(), later)
	row := waitRow(ctx, t, waiting.ID().String())
	if !row.next.Equal(later) {
		t.Fatalf("next attempt = %s, want %s", row.next, later)
	}
	assertDeadlineKept(t, row)
	if row.status != "PENDING_REFERENCE" || row.attempts != 1 || row.observed != nil || row.failure != nil {
		t.Fatalf("row = %+v, want PENDING_REFERENCE at one attempt with no balance and no token", row)
	}
}

// The deadline is written once, on entry, so no write of the worker may have
// moved it.
func assertDeadlineKept(t *testing.T, row waitState) {
	t.Helper()
	if !row.deadline.Equal(waitDeadline()) {
		t.Fatalf("deadline = %s, want the %s written on entry", row.deadline, waitDeadline())
	}
}

// The wait that ends keeps the deadline and the schedule it had: the update
// writes the terminal status and what that status carries, and clears nothing.
func TestEndWait_closesTheWaitWithoutClearingWhatTheRowAlreadyHeld(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	waiting := storedWait(ctx, t, unit, opened.wallet)
	closing := rehydrateWait(ctx, t, unit, waiting.ID())
	if err := closing.Reject(wager.ReferenceNotFound, stamp()); err != nil {
		t.Fatalf("Reject of the wait that ends = %v, want nil", err)
	}
	endWait(ctx, t, unit, closing)
	row := waitRow(ctx, t, waiting.ID().String())
	assertClosedWith(t, row, "REFERENCE_NOT_FOUND")
	assertDeadlineKept(t, row)
	if !row.next.Equal(stamp()) {
		t.Fatalf("next attempt = %s, want the %s it was written with", row.next, stamp())
	}
	if row.attempts != 1 {
		t.Fatalf("attempts = %d, want the 1 attempt that ended the wait", row.attempts)
	}
}

func assertClosedWith(t *testing.T, row waitState, code string) {
	t.Helper()
	if row.status != "REJECTED" || row.failure == nil || *row.failure != code {
		t.Fatalf("row = %+v, want REJECTED with %s", row, code)
	}
}

// A row that left the wait between the claim and the write is not written over:
// both statements name the status of the wait, and neither has a row to update.
func TestEndWait_writesNothingOverARowThatLeftTheWait(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	waiting := storedWait(ctx, t, unit, opened.wallet)
	closing := rehydrateWait(ctx, t, unit, waiting.ID())
	if err := closing.Reject(wager.ReferenceNotFound, stamp()); err != nil {
		t.Fatalf("Reject of the wait written over twice = %v, want nil", err)
	}
	endWait(ctx, t, unit, closing)
	again := unit.Within(ctx, func(tx storage.Tx) error { return tx.Transactions().EndWait(ctx, closing) })
	if !errors.Is(again, storage.ErrTransactionNotFound) {
		t.Fatalf("second EndWait = %v, want %v", again, storage.ErrTransactionNotFound)
	}
	moved := unit.Within(ctx, func(tx storage.Tx) error {
		return tx.Transactions().RescheduleWait(ctx, waiting.ID(), stamp().Add(time.Minute), stamp())
	})
	if !errors.Is(moved, storage.ErrTransactionNotFound) {
		t.Fatalf("RescheduleWait of a row that ended = %v, want %v", moved, storage.ErrTransactionNotFound)
	}
	if got := waitRow(ctx, t, waiting.ID().String()); got.attempts != 1 {
		t.Fatalf("attempts = %d, want the 1 of the attempt that ended the wait", got.attempts)
	}
}

// wholeQueue is a limit past anything one run of the suite leaves behind. The
// database is shared, so a case about the order has to see its own two rows among
// whatever else is still waiting, and a limit of its own size would cut them off.
const wholeQueue = 1000

// The scan only chooses candidates: it takes no lock, so it answers while a
// submission holds the very wallet those candidates belong to.
func TestDueWaits_answersTheEarliestFirstWithoutBlockingASubmission(t *testing.T) {
	ctx, pool, unit := open(t)
	opened := stored(ctx, t, unit)
	late := storedWaitAt(ctx, t, unit, opened.wallet, stamp().Add(time.Minute))
	early := storedWaitAt(ctx, t, unit, opened.wallet, stamp())
	release := holdWallet(ctx, t, unit, opened.wallet.ID())
	defer release()
	scanned, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	due, err := postgres.NewReads(pool).DueWaits(scanned, stamp().Add(2*time.Minute), wholeQueue)
	if err != nil {
		t.Fatalf("DueWaits while a submission holds the wallet = %v, want nil", err)
	}
	assertOrder(t, due, early.ID(), late.ID())
}

func TestDueWaits_answersNoMoreThanTheLimitAsked(t *testing.T) {
	ctx, pool, unit := open(t)
	opened := stored(ctx, t, unit)
	storedWait(ctx, t, unit, opened.wallet)
	storedWait(ctx, t, unit, opened.wallet)
	due, err := postgres.NewReads(pool).DueWaits(ctx, stamp().Add(time.Minute), 1)
	if err != nil {
		t.Fatalf("DueWaits = %v, want nil", err)
	}
	if len(due) != 1 {
		t.Fatalf("candidates = %d, want the 1 the limit asked", len(due))
	}
}

// The wait a replica already holds is skipped and not queued behind: the second
// session answers the absence at once instead of waiting for the lock.
func TestClaimWait_skipsTheRowAnotherSessionHolds(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	waiting := storedWait(ctx, t, unit, opened.wallet)
	release := holdWait(ctx, t, unit, waiting.ID())
	defer release()
	claimed, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := unit.Within(claimed, func(tx storage.Tx) error {
		_, err := tx.Transactions().ClaimWait(claimed, waiting.ID(), stamp())
		return err
	})
	if !errors.Is(err, storage.ErrTransactionNotFound) {
		t.Fatalf("second claim = %v, want %v: the row is held by the first", err, storage.ErrTransactionNotFound)
	}
}

// The scan takes no lock, so two replicas reading the same instant both offer the
// same row, and the claim is the only thing between them. A row whose next attempt
// the first replica has already moved is not claimed by the second.
func TestClaimWait_skipsTheRowWhoseNextAttemptIsNotDueYet(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	later := storedWaitAt(ctx, t, unit, opened.wallet, stamp().Add(time.Minute))
	err := unit.Within(ctx, func(tx storage.Tx) error {
		_, err := tx.Transactions().ClaimWait(ctx, later.ID(), stamp())
		return err
	})
	if !errors.Is(err, storage.ErrTransactionNotFound) {
		t.Fatalf("claim of a row not due = %v, want %v", err, storage.ErrTransactionNotFound)
	}
}

// A row nobody holds is claimed with the state and the attempts it carries, which
// is what the decision is taken on.
func TestClaimWait_answersTheRowReReadUnderTheLock(t *testing.T) {
	ctx, _, unit := open(t)
	opened := stored(ctx, t, unit)
	waiting := storedWait(ctx, t, unit, opened.wallet)
	var claimed storage.Wait
	err := unit.Within(ctx, func(tx storage.Tx) error {
		var err error
		claimed, err = tx.Transactions().ClaimWait(ctx, waiting.ID(), stamp())
		return err
	})
	if err != nil {
		t.Fatalf("ClaimWait = %v, want nil", err)
	}
	if claimed.State.Status != wager.PendingReference || claimed.Attempts != 0 {
		t.Fatalf("claimed %s at %d attempts, want PENDING_REFERENCE at 0", claimed.State.Status, claimed.Attempts)
	}
	if !claimed.State.ReferenceDeadlineAt.Equal(waitDeadline()) {
		t.Fatalf("deadline = %s, want %s", claimed.State.ReferenceDeadlineAt, waitDeadline())
	}
}

func assertOrder(t *testing.T, due []storage.WaitCandidate, first, second identity.TransactionID) {
	t.Helper()
	positions := map[identity.TransactionID]int{}
	for at, candidate := range due {
		positions[candidate.TransactionID] = at
	}
	early, found := positions[first]
	if !found {
		t.Fatalf("the wait scheduled first is absent from %d candidates, want it chosen", len(due))
	}
	late, found := positions[second]
	if !found {
		t.Fatalf("the wait scheduled later is absent from %d candidates, want it chosen", len(due))
	}
	if early > late {
		t.Fatalf("the earliest schedule came at %d and the later one at %d, want the earliest first", early, late)
	}
}

// holdWallet locks the wallet in a transaction of its own and keeps it open until
// the returned function is called, which is how a case puts a real submission in
// the way of the scan.
func holdWallet(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, id identity.WalletID) func() {
	t.Helper()
	return hold(ctx, t, unit, func(tx storage.Tx) error {
		_, err := tx.Wallets().GetForUpdate(ctx, id)
		return err
	})
}

func holdWait(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, id identity.TransactionID) func() {
	t.Helper()
	return hold(ctx, t, unit, func(tx storage.Tx) error {
		_, err := tx.Transactions().ClaimWait(ctx, id, waitDeadline())
		return err
	})
}

// hold runs the work in an open transaction and blocks that transaction until it
// is released, so the lock it took is still there for the case to run against.
func hold(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, work func(storage.Tx) error) func() {
	t.Helper()
	taken := make(chan error, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		taken <- unit.Within(ctx, func(tx storage.Tx) error {
			err := work(tx)
			taken <- err
			<-release
			return err
		})
	}()
	if err := <-taken; err != nil {
		t.Fatalf("take the lock = %v, want nil", err)
	}
	return func() {
		close(release)
		<-done
	}
}

// waitState is the waiting row as the database holds it, which is what tells an
// update that touched a column from one that left it alone.
type waitState struct {
	status   string
	failure  *string
	observed *int64
	next     time.Time
	deadline time.Time
	attempts int64
}

const selectWaitRow = `
SELECT status, failure_code, observed_balance_cents, next_attempt_at, reference_deadline_at, attempt_count
  FROM wager_transactions WHERE id = $1`

func waitRow(ctx context.Context, t *testing.T, id string) waitState {
	t.Helper()
	var row waitState
	err := connect(ctx, t).QueryRow(ctx, selectWaitRow, id).
		Scan(&row.status, &row.failure, &row.observed, &row.next, &row.deadline, &row.attempts)
	if err != nil {
		t.Fatalf("read the waiting row = %v, want nil", err)
	}
	return row
}

// waitDeadline is the deadline every wait below is written with: the entry plus
// the fifteen minutes of the rule.
func waitDeadline() time.Time {
	return stamp().Add(15 * time.Minute)
}

func storedWait(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, owner *wallet.Wallet) *wager.Transaction {
	t.Helper()
	return storedWaitAt(ctx, t, unit, owner, stamp())
}

func storedWaitAt(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, owner *wallet.Wallet, next time.Time) *wager.Transaction {
	t.Helper()
	op := citing(t, owner, wager.KindWin, "25.00", externalOf(t, "external-"+newID()))
	if err := op.WaitForReference(next, waitDeadline(), stamp()); err != nil {
		t.Fatalf("WaitForReference = %v, want nil", err)
	}
	insert(ctx, t, unit, op)
	return op
}

// citing is one operation that names another. A reversal always carries the
// reference and a WIN may, which is what lets both shapes be built here.
func citing(t *testing.T, owner *wallet.Wallet, kind wager.Kind, amount string, cited identity.ExternalTransactionID) *wager.Transaction {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	key := "key-" + newID()
	op, err := wager.NewExternal(wager.ExternalSpec{
		ID:                  transactionOf(t, newID()),
		ProviderID:          providerOf(t, "provider-a"),
		ExternalID:          externalOf(t, "external-"+newID()),
		IdempotencyKey:      keyOf(t, key),
		BodyHash:            "hash-" + key,
		PlayerID:            owner.PlayerID(),
		WalletID:            owner.ID(),
		RoundID:             roundOf(t, "round-1"),
		GameID:              gameOf(t, "game-1"),
		Kind:                kind,
		Amount:              parsed,
		ReferenceExternalID: cited,
		At:                  stamp(),
	})
	if err != nil {
		t.Fatalf("wager.NewExternal = %v, want nil", err)
	}
	return op
}

// rejectedReversal is a reversal a rule closed, which takes no place: the partial
// unique index only covers the one that reached PROCESSED.
func rejectedReversal(t *testing.T, owner *wallet.Wallet, cited identity.ExternalTransactionID) *wager.Transaction {
	t.Helper()
	op := citing(t, owner, wager.KindRefund, "25.00", cited)
	if err := op.Reject(wager.ReferenceMismatch, stamp()); err != nil {
		t.Fatalf("Reject = %v, want nil", err)
	}
	return op
}

// processedReversal is a reversal that went through. It carries the balance the
// wallet holds, because a PROCESSED row records the balance observed and the
// deferred trigger compares it to the last entry of that wallet.
func processedReversal(t *testing.T, owner *wallet.Wallet, cited identity.ExternalTransactionID) *wager.Transaction {
	t.Helper()
	op := citing(t, owner, wager.KindRefund, "25.00", cited)
	if err := op.Process(owner.Balance(), stamp()); err != nil {
		t.Fatalf("Process = %v, want nil", err)
	}
	return op
}

func rehydrateWait(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, id identity.TransactionID) *wager.Transaction {
	t.Helper()
	var claimed storage.Wait
	err := unit.Within(ctx, func(tx storage.Tx) error {
		var err error
		claimed, err = tx.Transactions().ClaimWait(ctx, id, waitDeadline())
		return err
	})
	if err != nil {
		t.Fatalf("ClaimWait before rehydrating = %v, want nil", err)
	}
	rehydrated, err := wager.Rehydrate(claimed.State)
	if err != nil {
		t.Fatalf("wager.Rehydrate = %v, want nil", err)
	}
	return rehydrated
}

func endWait(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, decided *wager.Transaction) {
	t.Helper()
	err := unit.Within(ctx, func(tx storage.Tx) error { return tx.Transactions().EndWait(ctx, decided) })
	if err != nil {
		t.Fatalf("EndWait = %v, want nil", err)
	}
}

func reschedule(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, id identity.TransactionID, next time.Time) {
	t.Helper()
	err := unit.Within(ctx, func(tx storage.Tx) error {
		return tx.Transactions().RescheduleWait(ctx, id, next, next)
	})
	if err != nil {
		t.Fatalf("RescheduleWait = %v, want nil", err)
	}
}

func citedOf(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, provider identity.ProviderID, external identity.ExternalTransactionID) wager.State {
	t.Helper()
	var state wager.State
	err := unit.Within(ctx, func(tx storage.Tx) error {
		var err error
		state, err = tx.Transactions().ByExternalID(ctx, provider, external)
		return err
	})
	if err != nil {
		t.Fatalf("ByExternalID = %v, want nil", err)
	}
	return state
}

func citedError(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, provider identity.ProviderID, external identity.ExternalTransactionID) error {
	t.Helper()
	return unit.Within(ctx, func(tx storage.Tx) error {
		_, err := tx.Transactions().ByExternalID(ctx, provider, external)
		return err
	})
}

func reversed(ctx context.Context, t *testing.T, unit *postgres.UnitOfWork, provider identity.ProviderID, cited identity.ExternalTransactionID) bool {
	t.Helper()
	var answered bool
	err := unit.Within(ctx, func(tx storage.Tx) error {
		var err error
		answered, err = tx.Transactions().HasProcessedReversal(ctx, provider, cited)
		return err
	})
	if err != nil {
		t.Fatalf("HasProcessedReversal = %v, want nil", err)
	}
	return answered
}
