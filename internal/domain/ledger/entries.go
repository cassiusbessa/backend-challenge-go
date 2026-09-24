package ledger

import (
	"cmp"
	"errors"
	"slices"
	"strings"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

var ErrNoEntries = errors.New("ledger: collection has no entry")

// Entries is the first-class collection of the ledger: it holds the stable
// order and rebuilds the balance.
//
// Every entry leaves by copy, so altering what the collection returns does not
// reach what it stores.
type Entries struct {
	entries []Entry
}

// NewEntries copies the entries and sorts them, so the collection is ordered
// from the moment it exists.
func NewEntries(entries ...Entry) Entries {
	kept := slices.Clone(entries)
	sortEntries(kept)
	return Entries{entries: kept}
}

// Add returns another collection. The original is left as it was.
func (e Entries) Add(entry Entry) Entries {
	return NewEntries(append(slices.Clone(e.entries), entry)...)
}

func (e Entries) Len() int {
	return len(e.entries)
}

func (e Entries) All() []Entry {
	return slices.Clone(e.entries)
}

// Last returns the entry with the highest sequence and reports whether the
// collection had any.
func (e Entries) Last() (Entry, bool) {
	if len(e.entries) == 0 {
		return Entry{}, false
	}
	return e.entries[len(e.entries)-1], true
}

// Balance adds credits and subtracts debits in collection order, starting from
// zero. The result is the balance after the last entry.
func (e Entries) Balance() (money.Money, error) {
	if len(e.entries) == 0 {
		return money.Money{}, ErrNoEntries
	}
	balance, err := money.Zero(e.entries[0].amount.Currency())
	if err != nil {
		return money.Money{}, err
	}
	for _, entry := range e.entries {
		balance, err = entry.direction.Apply(balance, entry.amount)
		if err != nil {
			return money.Money{}, err
		}
	}
	return balance, nil
}

// sortEntries orders by the monotonic sequence and, on a tie, by identity.
//
// The creation instant alone does not order, as go-reads requires: two entries
// fit in the same instant, and the pair is what the opaque cursor carries.
func sortEntries(entries []Entry) {
	slices.SortStableFunc(entries, func(left, right Entry) int {
		if order := cmp.Compare(left.sequence, right.sequence); order != 0 {
			return order
		}
		return strings.Compare(left.id.String(), right.id.String())
	})
}
