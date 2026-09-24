package ledger

import (
	"errors"
	"testing"
)

// movement builds an already computed entry, the way the wallet would hand it
// over.
func movement(t *testing.T, last string, sequence int64, direction Direction, amount, before, after int64) Entry {
	t.Helper()
	spec := creditSpec(t)
	spec.ID = entryID(t, last)
	spec.Sequence = sequence
	spec.Direction = direction
	spec.Amount = brl(t, amount)
	spec.BalanceBefore = brl(t, before)
	spec.BalanceAfter = brl(t, after)
	return mustEntry(t, spec)
}

func TestNewEntries_ordersBySequenceAndNotByTheInstant(t *testing.T) {
	t.Parallel()
	later := movement(t, "a", 3, Credit, 5000, 97500, 102500)
	earlier := movement(t, "b", 2, Debit, 2500, 100000, 97500)
	if !later.CreatedAt().Equal(earlier.CreatedAt()) {
		t.Fatalf("the fixture instants differ, want both entries created at the same moment")
	}
	ordered := NewEntries(later, earlier).All()
	if ordered[0].Sequence() != 2 {
		t.Fatalf("first sequence = %d, want 2", ordered[0].Sequence())
	}
	if ordered[1].Sequence() != 3 {
		t.Fatalf("second sequence = %d, want 3", ordered[1].Sequence())
	}
}

func TestNewEntries_breaksTheSequenceTieByIdentity(t *testing.T) {
	t.Parallel()
	second := movement(t, "b", 2, Debit, 2500, 100000, 97500)
	first := movement(t, "a", 2, Debit, 2500, 100000, 97500)
	ordered := NewEntries(second, first).All()
	if ordered[0].ID() != first.ID() {
		t.Fatalf("first entry id = %s, want %s", ordered[0].ID(), first.ID())
	}
	if ordered[1].ID() != second.ID() {
		t.Fatalf("second entry id = %s, want %s", ordered[1].ID(), second.ID())
	}
}

func TestBalance_rebuildsTheBalanceOfTheLastEntry(t *testing.T) {
	t.Parallel()
	opening := movement(t, "1", 1, Credit, 100000, 0, 100000)
	bet := movement(t, "2", 2, Debit, 2500, 100000, 97500)
	win := movement(t, "3", 3, Credit, 5000, 97500, 102500)
	entries := NewEntries(opening, bet, win)
	rebuilt, err := entries.Balance()
	if err != nil {
		t.Fatalf("Balance error = %v, want nil", err)
	}
	if rebuilt.Amount() != "1025.00" {
		t.Fatalf("rebuilt balance = %s, want 1025.00", rebuilt.Amount())
	}
	last, ok := entries.Last()
	if !ok {
		t.Fatalf("Last on a filled collection reported ok = false, want true")
	}
	if !rebuilt.Equal(last.BalanceAfter()) {
		t.Fatalf("rebuilt balance %s differs from the last entry balance %s", rebuilt.Amount(), last.BalanceAfter().Amount())
	}
}

func TestBalance_refusesTheEmptyCollection(t *testing.T) {
	t.Parallel()
	_, err := NewEntries().Balance()
	if !errors.Is(err, ErrNoEntries) {
		t.Fatalf("Balance of an empty collection error = %v, want ErrNoEntries", err)
	}
}

// An entry that skipped the constructor carries neither currency nor
// direction. The collection refuses instead of answering an invented balance.
func TestBalance_refusesAnEntryThatSkippedTheConstructor(t *testing.T) {
	t.Parallel()
	if _, err := NewEntries(Entry{}).Balance(); err == nil {
		t.Fatalf("Balance over the zero entry error = nil, want a refusal")
	}
	unset := Entry{amount: brl(t, 2500)}
	if _, err := NewEntries(unset).Balance(); !errors.Is(err, ErrInvalidDirection) {
		t.Fatalf("Balance over an entry without a direction error = %v, want ErrInvalidDirection", err)
	}
}

func TestAll_doesNotLetTheCallerChangeWhatIsStored(t *testing.T) {
	t.Parallel()
	original := movement(t, "1", 1, Credit, 100000, 0, 100000)
	stored := NewEntries(original)
	taken := stored.All()
	taken[0] = movement(t, "2", 2, Debit, 2500, 100000, 97500)
	kept := stored.All()
	if kept[0].ID() != original.ID() {
		t.Fatalf("stored entry id after altering the returned slice = %s, want %s", kept[0].ID(), original.ID())
	}
	if kept[0].Amount().Amount() != "1000.00" {
		t.Fatalf("stored amount after altering the returned slice = %s, want 1000.00", kept[0].Amount().Amount())
	}
}

func TestAdd_leavesTheOriginalCollectionAlone(t *testing.T) {
	t.Parallel()
	opening := movement(t, "1", 1, Credit, 100000, 0, 100000)
	original := NewEntries(opening)
	extended := original.Add(movement(t, "2", 2, Debit, 2500, 100000, 97500))
	if original.Len() != 1 {
		t.Fatalf("original length after Add = %d, want 1", original.Len())
	}
	if extended.Len() != 2 {
		t.Fatalf("extended length = %d, want 2", extended.Len())
	}
	if extended.All()[1].Sequence() != 2 {
		t.Fatalf("appended entry sequence = %d, want 2", extended.All()[1].Sequence())
	}
}

func TestLast_answersFalseForTheEmptyCollection(t *testing.T) {
	t.Parallel()
	entry, ok := NewEntries().Last()
	if ok {
		t.Fatalf("Last on an empty collection reported ok = true, want false")
	}
	if !entry.IsZero() {
		t.Fatalf("Last on an empty collection returned %s, want the zero entry", entry.ID())
	}
}
