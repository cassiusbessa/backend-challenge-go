package wager

import (
	"errors"
	"slices"
)

var (
	ErrUnknownStatus        = errors.New("wager: status is not one of the five")
	ErrInvalidTransition    = errors.New("wager: transition is not allowed from this status")
	ErrIncompleteTransition = errors.New("wager: transition is missing what the target status requires")
)

// Status is where the transaction stands. There are five and no more: there is
// no PROCESSING, because interrupted work is PENDING and waiting for a cited
// operation is PENDING_REFERENCE.
type Status uint8

const (
	noStatus Status = iota
	Pending
	PendingReference
	Processed
	Rejected
	Failed
	statusCount
)

var statusTokens = [statusCount]string{
	Pending:          "PENDING",
	PendingReference: "PENDING_REFERENCE",
	Processed:        "PROCESSED",
	Rejected:         "REJECTED",
	Failed:           "FAILED",
}

// The table is the whole machine. A terminal status is simply absent from it,
// which is what makes terminal immutable without a case of its own, and adding
// a status is a line here rather than a branch inside a growing function.
var allowedTransitions = map[Status][]Status{
	Pending:          {Processed, Rejected, PendingReference},
	PendingReference: {Processed, Rejected, Failed},
}

func (s Status) String() string {
	if s >= statusCount {
		return ""
	}
	return statusTokens[s]
}

func (s Status) IsZero() bool {
	return s == noStatus
}

func ParseStatus(text string) (Status, error) {
	for status := Pending; status < statusCount; status++ {
		if status.String() == text {
			return status, nil
		}
	}
	return noStatus, ErrUnknownStatus
}

func (s Status) CanMoveTo(target Status) bool {
	return slices.Contains(allowedTransitions[s], target)
}

func (s Status) IsTerminal() bool {
	switch s {
	case Processed, Rejected, Failed:
		return true
	}
	return false
}
