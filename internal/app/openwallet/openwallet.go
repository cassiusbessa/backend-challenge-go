// Package openwallet opens a wallet, which is the shortest write path of the
// system: one commit, no idempotency key and no contest over the balance.
package openwallet

import (
	"context"
	"fmt"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

// Clock reads the instant the opening is stamped with.
type Clock interface {
	Now() time.Time
}

// Minter answers the identities the domain does not create for itself.
type Minter interface {
	WalletID() (identity.WalletID, error)
	TransactionID() (identity.TransactionID, error)
	EntryID() (identity.LedgerEntryID, error)
	EventID() (identity.EventID, error)
}

// Command is the opening asked for. The currency is the one of the initial
// balance, so the two cannot disagree.
type Command struct {
	PlayerID       identity.PlayerID
	InitialBalance money.Money
}

// Result is the wallet as it was committed: the border answers this and never
// the aggregate.
type Result struct {
	WalletID identity.WalletID
	PlayerID identity.PlayerID
	Balance  money.Money
	Version  int64
}

// Service coordinates the opening. The zero value is not used: New is the only
// constructor.
type Service struct {
	uow    storage.UnitOfWork
	minter Minter
	clock  Clock
}

func New(uow storage.UnitOfWork, minter Minter, clock Clock) *Service {
	return &Service{uow: uow, minter: minter, clock: clock}
}

// Open records the wallet at version 1 and, when the initial balance is
// positive, the internal OPENING already PROCESSED and its credit entry — all in
// the same commit. An opening at zero records the wallet alone.
func (s *Service) Open(ctx context.Context, cmd Command) (Result, error) {
	result, err := s.open(ctx, cmd)
	if err != nil {
		return Result{}, fmt.Errorf("open wallet: %w", err)
	}
	return result, nil
}

func (s *Service) open(ctx context.Context, cmd Command) (Result, error) {
	minted, err := s.identities()
	if err != nil {
		return Result{}, err
	}
	at := s.clock.Now()
	opened, movement, err := wallet.Open(wallet.OpenSpec{
		ID:             minted.wallet,
		PlayerID:       cmd.PlayerID,
		InitialBalance: cmd.InitialBalance,
		EntryID:        minted.entry,
		TransactionID:  minted.transaction,
		At:             at,
	})
	if err != nil {
		return Result{}, err
	}
	if err := s.record(ctx, opened, movement, minted, at); err != nil {
		return Result{}, err
	}
	return resultOf(opened, movement), nil
}

func resultOf(opened *wallet.Wallet, movement wallet.Result) Result {
	return Result{
		WalletID: opened.ID(),
		PlayerID: opened.PlayerID(),
		Balance:  movement.Balance(),
		Version:  movement.Version(),
	}
}

// record writes everything in one commit, wallet first and transaction after,
// which is the order go-wallet-concurrency fixes and the order the ledger
// foreign key needs.
func (s *Service) record(ctx context.Context, opened *wallet.Wallet, movement wallet.Result, minted identities, at time.Time) error {
	return s.uow.Within(ctx, func(tx storage.Tx) error {
		if err := tx.Wallets().Insert(ctx, opened); err != nil {
			return err
		}
		entry, moved := movement.Entry()
		if !moved {
			// An opening at zero records the wallet alone: it writes no
			// transaction and moves no balance, so it emits no event either.
			return nil
		}
		opening, err := recordOpening(ctx, tx, opened, entry, at)
		if err != nil {
			return err
		}
		return storage.Record(ctx, tx, event.Commit{
			OutcomeID:     minted.outcome,
			BalanceID:     minted.balance,
			Transaction:   opening,
			Entry:         entry,
			WalletVersion: movement.Version(),
			At:            at,
		})
	})
}

func recordOpening(ctx context.Context, tx storage.Tx, opened *wallet.Wallet, entry ledger.Entry, at time.Time) (*wager.Transaction, error) {
	opening, err := wager.NewOpening(wager.OpeningSpec{
		ID:       entry.TransactionID(),
		PlayerID: opened.PlayerID(),
		WalletID: opened.ID(),
		Amount:   entry.Amount(),
		At:       at,
	})
	if err != nil {
		return nil, err
	}
	if err := opening.Process(entry.BalanceAfter(), at); err != nil {
		return nil, err
	}
	if err := tx.Transactions().Insert(ctx, opening); err != nil {
		return nil, err
	}
	if err := tx.Entries().Insert(ctx, entry); err != nil {
		return nil, err
	}
	return opening, nil
}

// identities is the set one opening needs. The transaction, the entry and the
// two events are minted even for an opening at zero, which simply does not use
// them.
type identities struct {
	wallet      identity.WalletID
	transaction identity.TransactionID
	entry       identity.LedgerEntryID
	outcome     identity.EventID
	balance     identity.EventID
}

func (s *Service) identities() (identities, error) {
	walletID, err := s.minter.WalletID()
	if err != nil {
		return identities{}, err
	}
	transactionID, err := s.minter.TransactionID()
	if err != nil {
		return identities{}, err
	}
	entryID, err := s.minter.EntryID()
	if err != nil {
		return identities{}, err
	}
	outcome, balance, err := s.eventIDs()
	if err != nil {
		return identities{}, err
	}
	return identities{wallet: walletID, transaction: transactionID, entry: entryID, outcome: outcome, balance: balance}, nil
}

func (s *Service) eventIDs() (identity.EventID, identity.EventID, error) {
	outcome, err := s.minter.EventID()
	if err != nil {
		return identity.EventID{}, identity.EventID{}, err
	}
	balance, err := s.minter.EventID()
	if err != nil {
		return identity.EventID{}, identity.EventID{}, err
	}
	return outcome, balance, nil
}
