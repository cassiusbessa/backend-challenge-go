package relayoutbox

import "time"

// The backoff go-outbox fixes: a base of one second, a factor of two and a
// ceiling of sixty seconds. The three are business rules rather than knobs.
const (
	base    = time.Second
	factor  = 2
	ceiling = 60 * time.Second
)

// atCeiling is how many doublings it takes to reach the ceiling: one second
// doubled six times is past sixty. It bounds the loop rather than the window,
// because the count of attempts comes from a stored column and a row that has
// been refused for a long time must not turn into a long loop.
const atCeiling = 6

// Backoff answers how long a row waits before the attempt that follows the ones
// it has already made: the base doubled once per attempt, and never past the
// ceiling.
//
// There is no draw in it, unlike the backoff of the reference wait: the rule of
// that one fixes an interval drawn inside the window, and the rule of this one
// fixes the interval itself.
func Backoff(attempts int64) time.Duration {
	window := base
	for range min(attempts, atCeiling) {
		window = min(window*factor, ceiling)
	}
	return window
}
