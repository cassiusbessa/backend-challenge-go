package reconcilewallet

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestReconcile_answersConsistentWhenTheLedgerClosesWithTheBalance(t *testing.T) {
	t.Parallel()
	report, err := New(&rows{summary: summaryOf(t, "1025.00", 102500, 3, 3, 0)}).Reconcile(context.Background(), walletOf(t))
	if err != nil {
		t.Fatalf("Reconcile of the closed ledger = %v, want nil", err)
	}
	assertConsistentReport(t, report)
	if report.EntryCount != 3 || report.LastSequence != 3 || report.Version != 4 {
		t.Fatalf("report = %d entries up to %d at version %d, want 3 up to 3 at version 4", report.EntryCount, report.LastSequence, report.Version)
	}
}

// assertConsistentReport checks the verdict of a wallet whose ledger closes with
// the balance: both sides equal, nothing named, no break.
func assertConsistentReport(t *testing.T, report Report) {
	t.Helper()
	if !report.Consistent {
		t.Fatalf("consistent = false with divergences %v, want true", report.Divergences)
	}
	if !report.StoredBalance.Equal(report.LedgerBalance) {
		t.Fatalf("balances = %s stored and %s rebuilt, want both sides equal", report.StoredBalance.Amount(), report.LedgerBalance.Amount())
	}
	if len(report.Divergences) != 0 || report.FirstBreakSequence != 0 {
		t.Fatalf("divergences = %v with a break at %d, want none", report.Divergences, report.FirstBreakSequence)
	}
}

// A wallet opened at zero has no entry, so the ledger sums to zero, the count is
// zero and the last sequence is zero: nothing to compare disagrees.
func TestReconcile_answersConsistentForTheWalletAtZeroWithoutMovements(t *testing.T) {
	t.Parallel()
	report, err := New(&rows{summary: summaryOf(t, "0.00", 0, 0, 0, 0)}).Reconcile(context.Background(), walletOf(t))
	if err != nil {
		t.Fatalf("Reconcile at zero = %v, want nil", err)
	}
	if !report.Consistent {
		t.Fatalf("consistent = false with divergences %v, want true at zero", report.Divergences)
	}
	if report.LedgerBalance.Amount() != "0.00" || report.LedgerBalance.Currency().Code() != "BRL" {
		t.Fatalf("rebuilt balance = %s, want 0.00 BRL", report.LedgerBalance)
	}
}

func TestReconcile_namesEachDivergenceAlone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		summary storage.LedgerSummary
		want    Divergence
	}{
		{name: "a balance written past the ledger", summary: summaryOf(t, "2000.00", 102500, 3, 3, 0), want: BalanceMismatch},
		{name: "a sequence with no entry", summary: summaryOf(t, "1025.00", 102500, 3, 4, 0), want: SequenceGap},
		{name: "an entry that does not start where the one before ended", summary: summaryOf(t, "1025.00", 102500, 3, 3, 2), want: ChainBreak},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is named", func(t *testing.T) {
			report, err := New(&rows{summary: tc.summary}).Reconcile(context.Background(), walletOf(t))
			if err != nil {
				t.Fatalf("Reconcile of the case = %v, want nil", err)
			}
			if report.Consistent {
				t.Fatalf("consistent = true, want false for %s", tc.name)
			}
			if !reflect.DeepEqual(report.Divergences, []Divergence{tc.want}) {
				t.Fatalf("divergences = %v, want exactly %s", report.Divergences, tc.want)
			}
		})
	}
}

func TestReconcile_namesTwoDivergencesTogether(t *testing.T) {
	t.Parallel()
	report, err := New(&rows{summary: summaryOf(t, "2000.00", 102500, 3, 4, 0)}).Reconcile(context.Background(), walletOf(t))
	if err != nil {
		t.Fatalf("Reconcile with two divergences = %v, want nil", err)
	}
	if want := []Divergence{BalanceMismatch, SequenceGap}; !reflect.DeepEqual(report.Divergences, want) {
		t.Fatalf("divergences = %v, want %v", report.Divergences, want)
	}
}

func TestReconcile_pointsAtTheFirstEntryOutOfTheChain(t *testing.T) {
	t.Parallel()
	report, err := New(&rows{summary: summaryOf(t, "1025.00", 102500, 3, 3, 2)}).Reconcile(context.Background(), walletOf(t))
	if err != nil {
		t.Fatalf("Reconcile with a broken chain = %v, want nil", err)
	}
	if report.FirstBreakSequence != 2 {
		t.Fatalf("first break = %d, want 2", report.FirstBreakSequence)
	}
}

// A broken ledger can subtract past zero. The report says what it sums, in the
// currency of the wallet, instead of refusing to say it.
func TestReconcile_carriesANegativeLedgerBalance(t *testing.T) {
	t.Parallel()
	report, err := New(&rows{summary: summaryOf(t, "0.00", -2500, 1, 1, 0)}).Reconcile(context.Background(), walletOf(t))
	if err != nil {
		t.Fatalf("Reconcile over a negative sum = %v, want nil", err)
	}
	if report.LedgerBalance.Amount() != "-25.00" {
		t.Fatalf("rebuilt balance = %s, want -25.00", report.LedgerBalance.Amount())
	}
	if !reflect.DeepEqual(report.Divergences, []Divergence{BalanceMismatch}) {
		t.Fatalf("divergences = %v, want exactly BALANCE_MISMATCH", report.Divergences)
	}
}

func TestReconcile_passesTheAbsenceThrough(t *testing.T) {
	t.Parallel()
	_, err := New(&rows{err: storage.ErrWalletNotFound}).Reconcile(context.Background(), walletOf(t))
	if !errors.Is(err, storage.ErrWalletNotFound) {
		t.Fatalf("Reconcile = %v, want %v", err, storage.ErrWalletNotFound)
	}
}

func TestReconcile_asksTheReadModelForTheIdentityInTheURL(t *testing.T) {
	t.Parallel()
	asked := &rows{summary: summaryOf(t, "0.00", 0, 0, 0, 0)}
	if _, err := New(asked).Reconcile(context.Background(), walletOf(t)); err != nil {
		t.Fatalf("Reconcile of the asked identity = %v, want nil", err)
	}
	if asked.asked != walletOf(t) {
		t.Fatalf("asked for = %s, want %s", asked.asked, walletOf(t))
	}
}

func TestString_answersTheTokenOfEachDivergenceAndNothingOutsideTheVocabulary(t *testing.T) {
	t.Parallel()
	cases := map[Divergence]string{BalanceMismatch: "BALANCE_MISMATCH", SequenceGap: "SEQUENCE_GAP", ChainBreak: "CHAIN_BREAK", noDivergence: "", divergenceCount: ""}
	for divergence, want := range cases {
		if got := divergence.String(); got != want {
			t.Fatalf("String of %d = %q, want %q", divergence, got, want)
		}
	}
}

// Each divergence is decided by its own comparison, and a wallet that fails
// none of them names nothing.
func TestDivergencesOf_namesEveryDeviationInTheOrderOfTheVocabulary(t *testing.T) {
	t.Parallel()
	stored := moneyOf(t, "1025.00")
	cases := []struct {
		name    string
		summary storage.LedgerSummary
		rebuilt money.Money
		want    []Divergence
	}{
		{name: "nothing deviates", summary: summaryOf(t, "1025.00", 102500, 3, 3, 0), rebuilt: stored, want: nil},
		{name: "the sum deviates", summary: summaryOf(t, "1025.00", 102500, 3, 3, 0), rebuilt: moneyOf(t, "1000.00"), want: []Divergence{BalanceMismatch}},
		{name: "everything deviates", summary: summaryOf(t, "1025.00", 102500, 3, 4, 2), rebuilt: moneyOf(t, "1000.00"), want: []Divergence{BalanceMismatch, SequenceGap, ChainBreak}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := divergencesOf(tc.summary, stored, tc.rebuilt)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("divergencesOf when %s = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func moneyOf(t *testing.T, amount string) money.Money {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse of %s = %v, want nil", amount, err)
	}
	return parsed
}

// rows is the read port in memory. Only the summary belongs to this use case:
// the other reads are part of the same port and are never reached from here.
type rows struct {
	summary storage.LedgerSummary
	err     error
	asked   identity.WalletID
}

func (r *rows) Summary(_ context.Context, id identity.WalletID) (storage.LedgerSummary, error) {
	r.asked = id
	if r.err != nil {
		return storage.LedgerSummary{}, r.err
	}
	return r.summary, nil
}

func (r *rows) Wallet(context.Context, identity.WalletID) (storage.WalletView, error) {
	return storage.WalletView{}, storage.ErrWalletNotFound
}

func (r *rows) Ledger(context.Context, identity.WalletID, storage.EntryPosition, int) ([]storage.EntryView, error) {
	return nil, storage.ErrWalletNotFound
}

func (r *rows) Transaction(context.Context, identity.TransactionID, identity.ProviderID) (storage.TransactionView, error) {
	return storage.TransactionView{}, storage.ErrTransactionNotFound
}

func (r *rows) TransactionByKey(context.Context, identity.ProviderID, identity.IdempotencyKey) (wager.State, error) {
	return wager.State{}, storage.ErrTransactionNotFound
}

func (r *rows) DueWaits(context.Context, time.Time, int) ([]storage.WaitCandidate, error) {
	return nil, nil
}

func summaryOf(t *testing.T, stored string, ledgerCents, count, last, firstBreak int64) storage.LedgerSummary {
	t.Helper()
	balance, err := money.Parse(stored, "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	playerID, err := identity.ParsePlayerID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("ParsePlayerID = %v, want nil", err)
	}
	at := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	return storage.LedgerSummary{
		Wallet: storage.WalletView{
			ID:        walletOf(t),
			PlayerID:  playerID,
			Balance:   balance,
			Version:   4,
			CreatedAt: at,
			UpdatedAt: at,
		},
		LedgerBalance:      ledgerCents,
		EntryCount:         count,
		LastSequence:       last,
		FirstBreakSequence: firstBreak,
	}
}

func walletOf(t *testing.T) identity.WalletID {
	t.Helper()
	id, err := identity.ParseWalletID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	return id
}
