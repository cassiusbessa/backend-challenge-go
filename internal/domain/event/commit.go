package event

import (
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// ErrUnknownOutcome is a transaction in a status no event speaks for. PENDING is
// the only one, and the schema refuses to write it: reaching here means a commit
// is about to record work that has not been decided, which is a defect rather
// than an outcome with nothing to say.
var ErrUnknownOutcome = errors.New("event: transaction is in a status no event names")

// Commit is what one commit decided, as the events of it are read off.
//
// Transaction is nil when the commit recorded none, which is the opening of a
// wallet at zero. Entry is the zero value when nothing moved, and WalletVersion
// is the version the wallet reached when something did.
//
// OutcomeID and BalanceID are minted for every commit and the ones it has no
// event for simply go unused, the same way an opening at zero mints a
// transaction and an entry it does not write.
type Commit struct {
	OutcomeID     identity.EventID
	BalanceID     identity.EventID
	Transaction   *wager.Transaction
	Entry         ledger.Entry
	WalletVersion int64
	At            time.Time
}

// Of answers the events one commit emits, in the order they are recorded: the
// outcome of the transaction first, and the movement of the balance after it.
//
// It is the single place that reads a status as a set of events, so no use case
// decides on its own that a LOSS emits no balance or that a rejection emits no
// movement. A commit that decided nothing — the opening of a wallet at zero —
// answers no events and no error.
func Of(commit Commit) ([]Envelope, error) {
	if commit.Transaction == nil {
		return nil, nil
	}
	outcome, emits, err := commit.outcome()
	if err != nil {
		return nil, err
	}
	events := make([]Envelope, 0, 2)
	if emits {
		events = append(events, outcome)
	}
	return commit.withBalance(events)
}

// outcome answers the event of the status the transaction reached, and reports
// whether there is one: FAILED is durable and has no event of its own, because
// no consumer decides anything from it.
func (c Commit) outcome() (Envelope, bool, error) {
	spec := Spec{ID: c.OutcomeID, AggregateID: c.Transaction.WalletID(), At: c.At}
	switch c.Transaction.Status() {
	case wager.Processed:
		return built(NewProcessed(spec, c.Transaction))
	case wager.Rejected:
		return built(NewRejected(spec, c.Transaction))
	case wager.PendingReference:
		return built(NewPendingReference(spec, c.Transaction))
	case wager.Failed:
		return Envelope{}, false, nil
	}
	return Envelope{}, false, ErrUnknownOutcome
}

// built turns the two results of a constructor into the three the switch above
// answers, so each arm stays a single line.
func built(envelope Envelope, err error) (Envelope, bool, error) {
	return envelope, err == nil, err
}

// withBalance appends the event of the movement, when the commit carried one.
//
// What decides is the entry and not the kind: a LOSS and an opening at zero
// produce none, and every operation that produces one moved the balance by it.
func (c Commit) withBalance(events []Envelope) ([]Envelope, error) {
	if c.Entry.IsZero() {
		return events, nil
	}
	spec := Spec{ID: c.BalanceID, AggregateID: c.Entry.WalletID(), At: c.At}
	balance, err := NewBalanceChanged(spec, c.Entry, c.WalletVersion)
	if err != nil {
		return nil, err
	}
	return append(events, balance), nil
}
