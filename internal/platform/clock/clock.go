// Package clock answers the instant the application stamps on a write.
//
// The domain never reads the wall clock: it takes the instant already resolved,
// which is what lets a test advance an injected clock instead of sleeping.
package clock

import "time"

// UTC reads the wall clock in UTC. The zero value is ready to use.
type UTC struct{}

// Now answers the current instant in UTC, because every instant that reaches
// persistence and the event envelope is in UTC.
func (UTC) Now() time.Time {
	return time.Now().UTC()
}
