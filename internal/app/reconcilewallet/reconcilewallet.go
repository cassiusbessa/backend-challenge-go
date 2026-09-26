// Package reconcilewallet compares the balance a wallet stores with the
// balance its ledger rebuilds, and names each way the two disagree.
//
// It reads and never corrects: no balance, version or entry changes because of
// the verdict. The verdict is decided here, over the aggregates the SQL
// answers, and not inside the SQL, so the rule is testable without a database
// and callable by whoever is not HTTP. See ADR 0022.
package reconcilewallet

import (
	"context"
	"fmt"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// Divergence is one way the stored balance and the ledger disagree. The
// vocabulary is closed, and it is not a failure code: it names a deviation a
// read found, not a rule that refused an operation. The zero value is not a
// divergence.
type Divergence uint8

const (
	noDivergence Divergence = iota
	// BalanceMismatch is the ledger summing to something other than the stored
	// balance.
	BalanceMismatch
	// SequenceGap is the count of entries differing from the last sequence:
	// some sequence between one and the last has no entry.
	SequenceGap
	// ChainBreak is an entry whose balance before is not the balance after of
	// the entry before it, with zero as what the first one starts from.
	ChainBreak
	divergenceCount
)

var divergenceTokens = [divergenceCount]string{
	BalanceMismatch: "BALANCE_MISMATCH",
	SequenceGap:     "SEQUENCE_GAP",
	ChainBreak:      "CHAIN_BREAK",
}

func (d Divergence) String() string {
	if d >= divergenceCount {
		return ""
	}
	return divergenceTokens[d]
}

// Vocabulary lists every divergence, in declaration order. It is what lets the
// series per token be created ahead of the first verdict, and what a test of
// exhaustiveness walks.
func Vocabulary() []Divergence {
	var out []Divergence
	for token := BalanceMismatch; token < divergenceCount; token++ {
		out = append(out, token)
	}
	return out
}

// Report is the verdict over one wallet. Divergences is empty and
// FirstBreakSequence is zero when the wallet is consistent.
//
// LedgerBalance may be negative: a broken ledger can subtract past zero, and
// the report says what the ledger sums rather than refusing to say it.
// Difference is the stored balance minus the ledger balance: zero when the two
// close, positive when the wallet stores more than the ledger sums, negative
// when it stores less.
type Report struct {
	WalletID           identity.WalletID
	StoredBalance      money.Money
	LedgerBalance      money.Money
	Difference         money.Money
	Version            int64
	EntryCount         int64
	LastSequence       int64
	Consistent         bool
	Divergences        []Divergence
	FirstBreakSequence int64
}

// Service answers the reconciliation. The zero value is not used: New is the
// only constructor.
type Service struct {
	reads storage.Reads
}

func New(reads storage.Reads) *Service {
	return &Service{reads: reads}
}

// Reconcile reads the wallet and its ledger from one snapshot and decides the
// verdict. It takes no lock and writes no row.
func (s *Service) Reconcile(ctx context.Context, id identity.WalletID) (Report, error) {
	summary, err := s.reads.Summary(ctx, id)
	if err != nil {
		return Report{}, fmt.Errorf("read ledger summary: %w", err)
	}
	return reportOf(summary)
}

func reportOf(summary storage.LedgerSummary) (Report, error) {
	stored := summary.Wallet.Balance
	rebuilt, err := money.FromCents(summary.LedgerBalance, stored.Currency())
	if err != nil {
		return Report{}, fmt.Errorf("rebuild ledger balance: %w", err)
	}
	// A difference past int64 is a state Money does not represent, the same as a
	// SUM past it (ADR 0022): there is no honest verdict, so it is a failure.
	difference, err := stored.Sub(rebuilt)
	if err != nil {
		return Report{}, fmt.Errorf("subtract ledger balance: %w", err)
	}
	divergences := divergencesOf(summary, stored, rebuilt)
	return Report{
		WalletID:           summary.Wallet.ID,
		StoredBalance:      stored,
		LedgerBalance:      rebuilt,
		Difference:         difference,
		Version:            summary.Wallet.Version,
		EntryCount:         summary.EntryCount,
		LastSequence:       summary.LastSequence,
		Consistent:         len(divergences) == 0,
		Divergences:        divergences,
		FirstBreakSequence: summary.FirstBreakSequence,
	}, nil
}

// divergencesOf names every deviation, in the order of the vocabulary. A wallet
// can carry more than one at once, and the report lists them all rather than
// the first found.
func divergencesOf(summary storage.LedgerSummary, stored, rebuilt money.Money) []Divergence {
	var found []Divergence
	if !rebuilt.Equal(stored) {
		found = append(found, BalanceMismatch)
	}
	if summary.EntryCount != summary.LastSequence {
		found = append(found, SequenceGap)
	}
	if summary.FirstBreakSequence != 0 {
		found = append(found, ChainBreak)
	}
	return found
}
