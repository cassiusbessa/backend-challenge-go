// Package resolvereference decides one operation that is waiting for the one it
// cites. It is the ResolvePendingReference use case of go-tactical-ddd.
//
// It is handed the identity of a single wait and decides that one: the ticker,
// the batch and the shutdown belong to the runner in the platform. That split is
// what makes the rule testable against an injected clock instead of a wall one.
//
// The order is the order of every other operation: lock the wallet, then claim
// the row of the wait, re-read its state under that lock, load the cited
// operation and let the function of the kind decide.
package resolvereference

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// ErrKindNotWaited is a kind that has no function able to wait for another
// operation. Only a WIN citing one and the two reversals reach the wait, so a
// row in any other kind is a row nothing should have written: it is a defect,
// not a rule refusing the operation, and it carries no failureCode.
var ErrKindNotWaited error = defect{errors.New("resolvereference: kind cannot be waiting for a cited operation")}

// defect carries the failure this use case should never have been handed, which
// no repetition can fix.
type defect struct{ error }

// Defect reports that the failure is ours and not a caller's.
func (defect) Defect() bool { return true }

// Clock reads the instant the decision is stamped with and the one the deadline
// is measured against.
type Clock interface {
	Now() time.Time
}

// Minter answers the identity of the entry a resumed operation writes.
type Minter interface {
	EntryID() (identity.LedgerEntryID, error)
}

// Schedule answers when a wait that this attempt did not end is tried again.
// It is the same policy the submission writes the wait with.
type Schedule interface {
	NextAttemptAt(attempts int64, now, deadline time.Time) time.Time
}

// Service decides one wait. The zero value is not used: New is the only
// constructor.
type Service struct {
	uow      storage.UnitOfWork
	minter   Minter
	clock    Clock
	schedule Schedule
}

func New(uow storage.UnitOfWork, minter Minter, clock Clock, schedule Schedule) *Service {
	return &Service{uow: uow, minter: minter, clock: clock, schedule: schedule}
}

// Resolve decides the wait the candidate names, in one commit.
//
// A candidate that is no longer waiting, or that another replica is holding, is
// not decided and is not a failure: the scan chose it, and what decides is the
// row re-read under the lock. It comes back as a candidate on the next scan.
func (s *Service) Resolve(ctx context.Context, candidate storage.WaitCandidate) error {
	err := s.resolve(ctx, candidate)
	if err == nil {
		return nil
	}
	var attempted attemptFailure
	if errors.As(err, &attempted) {
		s.setBack(ctx, attempted)
	}
	return fmt.Errorf("resolve pending reference: %w", err)
}

// attemptFailure is a failure of one attempt over a row this replica had already
// claimed, carrying what the schedule of that row is moved by.
type attemptFailure struct {
	error
	id       identity.TransactionID
	attempts int64
	deadline time.Time
}

func (f attemptFailure) Unwrap() error { return f.error }

// setBack moves the schedule of a row whose attempt failed, in a transaction of
// its own, because the one that failed is rolled back whole: the attempt it did
// not count and the schedule it did not move are undone along with it.
//
// A row left on its old schedule holds the head of the queue and is claimed again
// on every tick, so one row that keeps failing is a failure repeating per second
// instead of on the backoff, and enough of them fill every scan.
func (s *Service) setBack(ctx context.Context, failed attemptFailure) {
	if errors.Is(failed.error, context.Canceled) {
		// The attempt was stopped, not failed. The row keeps its schedule and the
		// next scan, here or in another replica, offers it again.
		return
	}
	now := s.clock.Now()
	next := s.schedule.NextAttemptAt(failed.attempts, now, failed.deadline)
	// Nothing is reported: a schedule that could not be moved leaves the row as
	// this attempt already left it, and the failure that caused it is the one
	// worth the line.
	_ = s.uow.Within(ctx, func(tx storage.Tx) error {
		return tx.Transactions().RescheduleWait(ctx, failed.id, next, now)
	})
}

func (s *Service) resolve(ctx context.Context, candidate storage.WaitCandidate) error {
	return s.uow.Within(ctx, func(tx storage.Tx) error {
		return s.decide(ctx, tx, candidate)
	})
}

// decide takes the wallet first and the row of the wait second, the order of
// every operation on this wallet. The scan took no lock at all, so nothing here
// stands between a submission and the balance it moves for longer than one
// decision.
func (s *Service) decide(ctx context.Context, tx storage.Tx, candidate storage.WaitCandidate) error {
	// A wait names its wallet by foreign key, so the absence of that wallet is
	// not a state this row can reach: it leaves as the failure it is, and the
	// wait is not decided.
	state, err := tx.Wallets().GetForUpdate(ctx, candidate.WalletID)
	if err != nil {
		return err
	}
	// One instant stamps the whole attempt: the claim tests the schedule against
	// it, and the decision is written with it.
	now := s.clock.Now()
	claimed, err := tx.Transactions().ClaimWait(ctx, candidate.TransactionID, now)
	if errors.Is(err, storage.ErrTransactionNotFound) {
		// Held by another replica, no longer there, or no longer due. None is this
		// replica's to decide, and none is a failure.
		return nil
	}
	if err != nil {
		return err
	}
	return s.decideClaimed(ctx, tx, claimed, state, now)
}

// decideClaimed is one attempt over the row as it reads under the lock.
//
// A row that left the wait between the scan and the claim is not decided again:
// what the first conclusion recorded stands.
func (s *Service) decideClaimed(ctx context.Context, tx storage.Tx, claimed storage.Wait, state wallet.State, now time.Time) error {
	if claimed.State.Status != wager.PendingReference {
		return nil
	}
	if err := s.attempt(ctx, tx, claimed, state, now); err != nil {
		// Past the claim, a failure belongs to a row this replica holds, so its
		// schedule is what keeps it from being claimed again on the next tick.
		return attemptFailure{
			error:    err,
			id:       claimed.State.ID,
			attempts: claimed.Attempts,
			deadline: claimed.State.ReferenceDeadlineAt,
		}
	}
	return nil
}

func (s *Service) attempt(ctx context.Context, tx storage.Tx, claimed storage.Wait, state wallet.State, now time.Time) error {
	waiting, err := wager.Rehydrate(claimed.State)
	if err != nil {
		return err
	}
	owner, err := wallet.Rehydrate(state)
	if err != nil {
		return err
	}
	return s.run(ctx, tx, attempt{waiting: waiting, owner: owner, claimed: claimed, readVersion: state.Version, now: now})
}

// attempt is one wait as this turn sees it: the transaction, the wallet it is
// decided over, the row as it was claimed and the version the balance write is
// conditioned on.
type attempt struct {
	waiting     *wager.Transaction
	owner       *wallet.Wallet
	claimed     storage.Wait
	readVersion int64
	now         time.Time
}

func (s *Service) run(ctx context.Context, tx storage.Tx, turn attempt) error {
	move, err := s.movement(turn.now)
	if err != nil {
		return err
	}
	reference, err := citedFor(ctx, tx, turn)
	if err != nil {
		return err
	}
	decision, err := decisionOf(turn, reference, move)
	if err != nil {
		return s.reject(ctx, tx, turn, err)
	}
	if decision.IsWaiting() {
		return s.goOn(ctx, tx, turn, reference)
	}
	return s.resume(ctx, tx, turn, decision, move)
}

func (s *Service) movement(at time.Time) (wager.Movement, error) {
	entryID, err := s.minter.EntryID()
	if err != nil {
		return wager.Movement{}, err
	}
	return wager.Movement{EntryID: entryID, At: at}, nil
}

// citedFor loads the operation the wait names, under the lock already taken. A
// row in the wait always names one: that is what it is waiting for.
func citedFor(ctx context.Context, tx storage.Tx, turn attempt) (wager.Reference, error) {
	named, cites := turn.waiting.ReferenceExternalID()
	if !cites {
		return wager.Reference{}, ErrKindNotWaited
	}
	state, err := tx.Transactions().ByExternalID(ctx, turn.waiting.ProviderID(), named)
	if errors.Is(err, storage.ErrTransactionNotFound) {
		return wager.Reference{}, nil
	}
	if err != nil {
		return wager.Reference{}, err
	}
	cited, err := wager.Rehydrate(state)
	if err != nil {
		return wager.Reference{}, err
	}
	return reversalOf(ctx, tx, turn, cited, named)
}

func reversalOf(ctx context.Context, tx storage.Tx, turn attempt, cited *wager.Transaction, named identity.ExternalTransactionID) (wager.Reference, error) {
	if !turn.waiting.Kind().IsReversal() {
		return wager.Reference{Cited: cited}, nil
	}
	reversed, err := tx.Transactions().HasProcessedReversal(ctx, turn.waiting.ProviderID(), named)
	if err != nil {
		return wager.Reference{}, err
	}
	return wager.Reference{Cited: cited, AlreadyReversed: reversed}, nil
}

// decisionOf names the function of the kind, the same three the submission
// dispatches to. Nothing about the decision of a kind is reimplemented here: the
// only difference is who is asking and when.
func decisionOf(turn attempt, reference wager.Reference, move wager.Movement) (wager.Decision, error) {
	switch turn.waiting.Kind() {
	case wager.KindWin:
		return wager.Win(turn.owner, turn.waiting, reference, move)
	case wager.KindRefund:
		return wager.Refund(turn.owner, turn.waiting, reference, move)
	case wager.KindRollback:
		return wager.Rollback(turn.owner, turn.waiting, reference, move)
	}
	return wager.Decision{}, ErrKindNotWaited
}

// goOn answers the wait that this attempt did not end: either the deadline has
// come and the wait is closed with the token of what was missing, or the next
// attempt is scheduled.
func (s *Service) goOn(ctx context.Context, tx storage.Tx, turn attempt, reference wager.Reference) error {
	if !turn.now.Before(turn.waiting.ReferenceDeadlineAt()) {
		return s.close(ctx, tx, turn, expiredWith(reference), turn.now)
	}
	next := s.schedule.NextAttemptAt(turn.claimed.Attempts, turn.now, turn.waiting.ReferenceDeadlineAt())
	return tx.Transactions().RescheduleWait(ctx, turn.waiting.ID(), next, turn.now)
}

// expiredWith names what was missing when the deadline came.
//
// The domain answers the same decision to wait for a cited operation that is
// absent and for one that is still running, because the difference only matters
// once the clock ends the wait — and the clock is not in the domain. Here it is,
// so here the two are told apart.
func expiredWith(reference wager.Reference) wager.FailureCode {
	if reference.Cited == nil {
		return wager.ReferenceNotFound
	}
	return wager.ReferenceNotProcessed
}

// reject closes the wait on a rule: the cited operation arrived and does not
// close with the one waiting, or it already ended badly, or it is already
// reversed. None of them waits for the deadline.
func (s *Service) reject(ctx context.Context, tx storage.Tx, turn attempt, refusal error) error {
	var rejection wager.Rejection
	if !errors.As(refusal, &rejection) {
		return refusal
	}
	return s.close(ctx, tx, turn, rejection.Code(), turn.now)
}

// close writes the wait as REJECTED with its token. Nothing moves: no entry, no
// balance and no version.
func (s *Service) close(ctx context.Context, tx storage.Tx, turn attempt, code wager.FailureCode, at time.Time) error {
	if err := turn.waiting.Reject(code, at); err != nil {
		return err
	}
	return tx.Transactions().EndWait(ctx, turn.waiting)
}

// resume carries out the operation the wait was for: the credit of the WIN or
// the movement of the reversal, with the entry and the balance in this very
// commit.
func (s *Service) resume(ctx context.Context, tx storage.Tx, turn attempt, decision wager.Decision, move wager.Movement) error {
	entry, movedBalance := decision.Entry()
	if !movedBalance {
		// No kind that waits for a cited operation settles without moving the
		// balance, so a decision with no entry is one nothing here can write.
		return ErrKindNotWaited
	}
	if err := turn.waiting.Process(decision.Balance(), move.At); err != nil {
		return err
	}
	if err := tx.Wallets().UpdateBalance(ctx, turn.owner, turn.readVersion); err != nil {
		return err
	}
	if err := tx.Transactions().EndWait(ctx, turn.waiting); err != nil {
		return err
	}
	return tx.Entries().Insert(ctx, entry)
}
