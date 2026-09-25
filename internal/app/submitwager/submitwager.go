// Package submitwager settles one operation of a provider in a single commit.
//
// The order is fixed: build the transaction, look the key up, lock the wallet,
// let the function of the kind decide, and write. It is that order that separates
// a refusal with no row from a rejection that keeps one.
package submitwager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/bodyhash"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

var (
	// ErrKindNotAccepted is a kind this delivery does not settle. The border
	// refuses those before the use case, so reaching here means the border let
	// one through: it is a defect, not a rule refusing the operation, and it
	// carries no failureCode.
	ErrKindNotAccepted error = defect{errors.New("submitwager: kind is not settled by this use case")}

	// ErrOutcomeInFlight is a transaction recorded under that key which is not
	// terminal yet, so it has no outcome to replay. It is transient: the provider
	// sends the same key again.
	ErrOutcomeInFlight error = transient{errors.New("submitwager: recorded operation is not terminal yet")}

	// ErrRaceUnresolved is the row that won the key no longer being there when the
	// loser re-reads it, which only happens when the winner rolled back. It is
	// transient for the same reason.
	ErrRaceUnresolved error = transient{errors.New("submitwager: row that won the key is no longer there")}
)

// transient carries the failure that resolves by itself shortly, so the border
// answers a retry instead of an outage.
//
// The border looks for the behaviour and not for the type, the same way it looks
// for a replay: the marker belongs to whoever knows the condition is temporary,
// and this package does not import the border to say so.
type transient struct{ error }

// RetryShortly reports that the same request can be sent again in a moment.
func (transient) RetryShortly() bool { return true }

// defect carries the failure this use case should never have been handed, which
// no repetition of the request can fix.
type defect struct{ error }

// Defect reports that the failure is ours and not the caller's.
func (defect) Defect() bool { return true }

// Clock reads the instant the operation is stamped with.
type Clock interface {
	Now() time.Time
}

// Minter answers the identities the domain does not create for itself.
type Minter interface {
	TransactionID() (identity.TransactionID, error)
	EntryID() (identity.LedgerEntryID, error)
}

// Command is the operation the provider sent, with every field already parsed.
//
// It names no cited operation: this delivery settles what ends inside its own
// request, so no action it drives can ask to wait for another one.
type Command struct {
	ProviderID     identity.ProviderID
	ExternalID     identity.ExternalTransactionID
	IdempotencyKey identity.IdempotencyKey
	PlayerID       identity.PlayerID
	WalletID       identity.WalletID
	RoundID        identity.RoundID
	GameID         identity.GameID
	Kind           wager.Kind
	Amount         money.Money
}

// Result is the outcome as the border answers it. ObservedBalance is the balance
// of the commit that closed the operation, and on a replay it is the one observed
// back then and not the current one.
type Result struct {
	TransactionID    identity.TransactionID
	Kind             wager.Kind
	Status           wager.Status
	ExternalID       identity.ExternalTransactionID
	Amount           money.Money
	ObservedBalance  money.Money
	IdempotentReplay bool
}

// Replayed marks a refusal that was already recorded: the same key and the same
// business body arriving again answer the token of the first refusal, and the
// border says it is a replay.
//
// It wraps the rejection instead of replacing it, so errors.As still reaches the
// token, and it carries no amount, no balance and no key.
type Replayed struct {
	rejection error
}

func (r Replayed) Error() string {
	return "submitwager: recorded refusal replayed: " + r.rejection.Error()
}

func (r Replayed) Unwrap() error {
	return r.rejection
}

// IdempotentReplay reports that the outcome was already recorded. The border
// looks for this behaviour and not for this type, so the marker belongs to
// whoever answers a recorded outcome.
func (r Replayed) IdempotentReplay() bool {
	return true
}

// Service coordinates the settlement. The zero value is not used: New is the only
// constructor.
type Service struct {
	uow    storage.UnitOfWork
	reads  storage.Reads
	minter Minter
	clock  Clock
}

func New(uow storage.UnitOfWork, reads storage.Reads, minter Minter, clock Clock) *Service {
	return &Service{uow: uow, reads: reads, minter: minter, clock: clock}
}

// Submit records the operation and answers the outcome. A rule refusing it comes
// back as a wager.Rejection, which the border tells apart from a failure.
func (s *Service) Submit(ctx context.Context, cmd Command) (Result, error) {
	result, err := s.submit(ctx, cmd)
	if err != nil {
		return Result{}, fmt.Errorf("submit wager: %w", err)
	}
	return result, nil
}

func (s *Service) submit(ctx context.Context, cmd Command) (Result, error) {
	job, err := s.pending(cmd)
	if err != nil {
		return Result{}, err
	}
	decided, err := s.settle(ctx, job)
	if err != nil {
		return s.afterRace(ctx, job, err)
	}
	return answerOf(decided)
}

// pending is one operation on its way through the commit: the transaction the
// domain built, and the entry identity and instant the movement will take.
type pending struct {
	op   *wager.Transaction
	move wager.Movement
}

func (p pending) at() time.Time {
	return p.move.At
}

// pending builds the transaction. OPENING_NOT_ALLOWED, AMOUNT_NOT_ALLOWED_FOR_KIND
// and REFERENCE_REQUIRED leave here, before any row exists.
func (s *Service) pending(cmd Command) (pending, error) {
	transactionID, err := s.minter.TransactionID()
	if err != nil {
		return pending{}, err
	}
	entryID, err := s.minter.EntryID()
	if err != nil {
		return pending{}, err
	}
	at := s.clock.Now()
	op, err := wager.NewExternal(cmd.spec(transactionID, bodyhash.Of(cmd.business()), at))
	if err != nil {
		return pending{}, err
	}
	return pending{op: op, move: wager.Movement{EntryID: entryID, At: at}}, nil
}

func (c Command) spec(id identity.TransactionID, hash string, at time.Time) wager.ExternalSpec {
	return wager.ExternalSpec{
		ID:             id,
		ProviderID:     c.ProviderID,
		ExternalID:     c.ExternalID,
		IdempotencyKey: c.IdempotencyKey,
		BodyHash:       hash,
		PlayerID:       c.PlayerID,
		WalletID:       c.WalletID,
		RoundID:        c.RoundID,
		GameID:         c.GameID,
		Kind:           c.Kind,
		Amount:         c.Amount,
		At:             at,
	}
}

func (c Command) business() bodyhash.Business {
	return bodyhash.Business{
		ProviderID: c.ProviderID,
		ExternalID: c.ExternalID,
		PlayerID:   c.PlayerID,
		WalletID:   c.WalletID,
		RoundID:    c.RoundID,
		GameID:     c.GameID,
		Kind:       c.Kind,
		Amount:     c.Amount,
	}
}

// settlement is what one commit decided.
//
// The rejection travels beside the result because a durable rejection is
// committed and still answered as a refusal: returning it from the work function
// would roll its own row back.
type settlement struct {
	result    Result
	rejection error
}

func answerOf(decided settlement) (Result, error) {
	if decided.rejection != nil {
		return Result{}, decided.rejection
	}
	return decided.result, nil
}

func (s *Service) settle(ctx context.Context, job pending) (settlement, error) {
	var decided settlement
	err := s.uow.Within(ctx, func(tx storage.Tx) error {
		var err error
		decided, err = s.decide(ctx, tx, job)
		return err
	})
	return decided, err
}

func (s *Service) decide(ctx context.Context, tx storage.Tx, job pending) (settlement, error) {
	recorded, found, err := s.recorded(ctx, tx, job)
	if found || err != nil {
		return recorded, err
	}
	return s.apply(ctx, tx, job)
}

// recorded is the fast path of a replay: the transaction already written under
// that key, if there is one.
//
// It takes no lock, so it does not invert the order of wallet before transaction,
// and it is not the arbiter: two replicas can both miss it, and the unique index
// decides.
func (s *Service) recorded(ctx context.Context, tx storage.Tx, job pending) (settlement, bool, error) {
	state, err := tx.Transactions().ByKey(ctx, job.op.ProviderID(), job.op.IdempotencyKey())
	if errors.Is(err, storage.ErrTransactionNotFound) {
		return settlement{}, false, nil
	}
	if err != nil {
		return settlement{}, true, err
	}
	decided, err := outcomeOf(state, job.op.BodyHash())
	return decided, true, err
}

// outcomeOf answers what a transaction already recorded under that key means for
// the arrival being decided: the same hash replays it, another hash is the
// conflict, and neither moves money or writes a row.
func outcomeOf(state wager.State, hash string) (settlement, error) {
	if state.BodyHash != hash {
		// The conflict travels as the settlement and not as the failure of the
		// work: what decided it here is the read, so there is no constraint
		// violation for the race handling to resolve afterwards.
		return settlement{rejection: wager.NewRejection(wager.IdempotencyConflict, nil)}, nil
	}
	recorded, err := wager.Rehydrate(state)
	if err != nil {
		return settlement{}, err
	}
	outcome, terminal := recorded.Replay()
	if !terminal {
		return settlement{}, ErrOutcomeInFlight
	}
	return settlement{result: replayOf(recorded), rejection: refusalOf(outcome)}, nil
}

// refusalOf answers the recorded refusal, marked as a replay, or nil when the
// recorded outcome is not a refusal.
func refusalOf(outcome wager.Outcome) error {
	if outcome.Status() != wager.Rejected {
		return nil
	}
	return Replayed{rejection: wager.NewRejection(outcome.FailureCode(), nil)}
}

// apply settles the operation over the locked wallet.
func (s *Service) apply(ctx context.Context, tx storage.Tx, job pending) (settlement, error) {
	state, err := tx.Wallets().GetForUpdate(ctx, job.op.WalletID())
	if err != nil {
		return settlement{}, absentWallet(err)
	}
	owner, err := wallet.Rehydrate(state)
	if err != nil {
		return settlement{}, err
	}
	decision, err := decisionOf(owner, job)
	if err != nil {
		return s.reject(ctx, tx, job, err)
	}
	return s.record(ctx, tx, job, moved{owner: owner, decision: decision, readVersion: state.Version})
}

// absentWallet turns the absence of the wallet into the token of the catalog. It
// happens before the transaction exists and the row could not exist anyway: its
// foreign key names a wallet that is not there.
func absentWallet(err error) error {
	if errors.Is(err, storage.ErrWalletNotFound) {
		return wager.NewRejection(wager.WalletNotFound, err)
	}
	return err
}

// decisionOf names the function of the kind, which is the whole choice the use
// case makes: it writes no SQL and emits no event.
func decisionOf(owner *wallet.Wallet, job pending) (wager.Decision, error) {
	switch job.op.Kind() {
	case wager.KindBet:
		return wager.Bet(owner, job.op, job.move)
	case wager.KindWin:
		// A WIN citing no operation credits right away. The command names none, so
		// the empty reference is never read: the delivery that brings the wait
		// loads the cited operation and passes it here.
		return wager.Win(owner, job.op, wager.Reference{}, job.move)
	case wager.KindLoss:
		return wager.Loss(owner, job.op)
	}
	return wager.Decision{}, ErrKindNotAccepted
}

// moved is what the action decided over the locked wallet, together with the
// version the write will be conditioned on.
type moved struct {
	owner       *wallet.Wallet
	decision    wager.Decision
	readVersion int64
}

// reject records the refusal of a rule.
//
// Every rejection that reaches here comes from the action over a wallet already
// locked, so the row is writable: the refusals that could not carry a row left
// earlier, when there was no transaction and no wallet to point at.
func (s *Service) reject(ctx context.Context, tx storage.Tx, job pending, refusal error) (settlement, error) {
	var rejection wager.Rejection
	if !errors.As(refusal, &rejection) {
		return settlement{}, refusal
	}
	if err := job.op.Reject(rejection.Code(), job.at()); err != nil {
		return settlement{}, err
	}
	if err := tx.Transactions().Insert(ctx, job.op); err != nil {
		return settlement{}, err
	}
	return settlement{result: resultOf(job.op), rejection: refusal}, nil
}

func (s *Service) record(ctx context.Context, tx storage.Tx, job pending, m moved) (settlement, error) {
	if err := job.op.Process(m.decision.Balance(), job.at()); err != nil {
		return settlement{}, err
	}
	if err := write(ctx, tx, job, m); err != nil {
		return settlement{}, err
	}
	return settlement{result: resultOf(job.op)}, nil
}

// write puts the balance, the transaction and the entry in one commit. The order
// is the wallet and then the transaction, which is the order of the lock and the
// one the foreign key of the ledger needs.
func write(ctx context.Context, tx storage.Tx, job pending, m moved) error {
	entry, movedBalance := m.decision.Entry()
	if !movedBalance {
		// A LOSS moves no balance: the version stays where it was and there is no
		// entry, so the transaction row is all there is to write.
		return tx.Transactions().Insert(ctx, job.op)
	}
	if err := tx.Wallets().UpdateBalance(ctx, m.owner, m.readVersion); err != nil {
		return err
	}
	if err := tx.Transactions().Insert(ctx, job.op); err != nil {
		return err
	}
	return tx.Entries().Insert(ctx, entry)
}

// afterRace decides what the loser of a unique index answers.
//
// Both indexes send the loser here. A twin of the same operation under the same
// key violates the external id index as well as the key one, and PostgreSQL names
// only the first it checks, so the token of the violation does not say on its own
// which arrival this is. The winning row under the key is what does: the same hash
// is a replay, another hash is the conflict, and no row at all means the external
// id really belongs to another key, which is the rejection that stands.
//
// The violation aborted its SQL transaction, so the row is read once from outside
// it. The movement the loser had computed died with the rollback, which is the
// single effect the specification asks for.
func (s *Service) afterRace(ctx context.Context, job pending, failure error) (Result, error) {
	if !raced(failure) {
		return Result{}, failure
	}
	state, err := s.reads.TransactionByKey(ctx, job.op.ProviderID(), job.op.IdempotencyKey())
	if err != nil {
		return Result{}, vanished(err, failure)
	}
	decided, err := outcomeOf(state, job.op.BodyHash())
	if err != nil {
		return Result{}, err
	}
	return answerOf(decided)
}

// raced reports whether the violation is one a winning row can answer.
func raced(err error) bool {
	var rejection wager.Rejection
	if !errors.As(err, &rejection) {
		return false
	}
	return rejection.Code() == wager.IdempotencyConflict ||
		rejection.Code() == wager.DuplicateExternalTransaction
}

// vanished answers what an absent winning row means for each violation. A
// duplicate external id with nothing under this key is the conflict the catalog
// describes: the pair belongs to another key, and that rejection is the answer.
// Under the key index the absence has no explanation, so it is transient.
func vanished(err, failure error) error {
	if !errors.Is(err, storage.ErrTransactionNotFound) {
		return err
	}
	var rejection wager.Rejection
	if errors.As(failure, &rejection) && rejection.Code() == wager.DuplicateExternalTransaction {
		return failure
	}
	return ErrRaceUnresolved
}

func resultOf(op *wager.Transaction) Result {
	return Result{
		TransactionID:   op.ID(),
		Kind:            op.Kind(),
		Status:          op.Status(),
		ExternalID:      op.ExternalID(),
		Amount:          op.Amount(),
		ObservedBalance: op.ObservedBalance(),
	}
}

func replayOf(recorded *wager.Transaction) Result {
	result := resultOf(recorded)
	result.IdempotentReplay = true
	return result
}
