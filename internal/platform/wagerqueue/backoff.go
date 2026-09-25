package wagerqueue

import "time"

// The backoff go-sqs-ingress fixes for a message that failed transiently: a base
// of one second, a factor of two and a ceiling of sixty seconds. The three are
// rules rather than knobs.
const (
	base    = time.Second
	factor  = 2
	ceiling = 60 * time.Second
)

// atCeiling is how many doublings it takes to reach the ceiling: one second
// doubled six times is past sixty. It bounds the loop rather than the window,
// because the count of deliveries comes from the broker and a message that has
// been around for a long time must not turn into a long loop.
const atCeiling = 6

// Backoff answers how long a message stays invisible before the delivery that
// follows the ones it has already had: the base doubled once per delivery, and
// never past the ceiling.
//
// The count is the one the broker registers on the message. There is no column of
// ours behind it: a message that failed transiently left no row at all, because
// the rollback took it, so a counter of ours would have to be a write outside the
// commit.
func Backoff(deliveries int64) time.Duration {
	window := base
	for range min(max(deliveries-1, 0), atCeiling) {
		window = min(window*factor, ceiling)
	}
	return window
}
