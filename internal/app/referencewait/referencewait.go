// Package referencewait answers when the wait for a cited operation is tried
// again, and when it expires.
//
// The domain answers only that the operation has to wait; the deadline, the
// backoff and the clock are not its business. They live here, beside the use
// case that writes the wait and the one that closes it, so both read the same
// policy instead of each keeping its own.
package referencewait

import (
	"math/rand/v2"
	"time"
)

// The backoff go-reference-wait fixes: a base of one second, a factor of two and
// a ceiling of sixty seconds. The three are business rules rather than knobs, so
// they stay here, while the TTL — which the same rule says is configurable —
// arrives in the constructor.
const (
	base    = time.Second
	factor  = 2
	ceiling = 60 * time.Second
)

// Jitter draws an interval between zero and the window it is given.
//
// Drawing instead of taking the whole window is what keeps two waits that
// entered together from retrying in step for as long as they both last.
type Jitter func(window time.Duration) time.Duration

// FullJitter draws uniformly anywhere in the window, which is the shape the rule
// fixes: the interval sits between zero and the ceiling of that attempt.
func FullJitter(window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	//nolint:gosec // the interval of a retry is not a secret: it only keeps two
	// waits that entered together from retrying in step.
	return time.Duration(rand.Int64N(int64(window)))
}

// Schedule is the policy of one wait: how long it lasts and when it is tried.
// The zero value is not used: New is the only constructor.
type Schedule struct {
	ttl    time.Duration
	jitter Jitter
}

func New(ttl time.Duration, jitter Jitter) Schedule {
	return Schedule{ttl: ttl, jitter: jitter}
}

// DeadlineAt answers the instant a wait entered at that time expires. It is
// written once, on entry, and a later attempt never moves it.
func (s Schedule) DeadlineAt(entered time.Time) time.Time {
	return entered.Add(s.ttl)
}

// NextAttemptAt answers when a wait that already made that many attempts is
// tried again, never past its deadline.
//
// The cap is not a rounding: a draw landing after the deadline would schedule
// the attempt past the end of the wait, and the row would sit expired with
// nobody due to close it until the attempt after that.
func (s Schedule) NextAttemptAt(attempts int64, now, deadline time.Time) time.Time {
	next := now.Add(s.jitter(windowOf(attempts)))
	if next.After(deadline) {
		return deadline
	}
	return next
}

// atCeiling is how many doublings it takes to reach the ceiling: one second
// doubled six times is past sixty. It bounds the loop rather than the window,
// because the count of attempts comes from a stored column and a wait that ran
// for a long time must not turn into a long loop.
const atCeiling = 6

// windowOf answers the ceiling of one attempt: the base doubled once per attempt
// already made, and never past the ceiling.
//
// The clamp is a min and not a comparison so that the two are one statement: a
// doubling sequence never lands on sixty exactly, so a branch there would be a
// branch no case could tell the two sides of apart.
func windowOf(attempts int64) time.Duration {
	window := base
	for range min(attempts, atCeiling) {
		window = min(window*factor, ceiling)
	}
	return window
}
