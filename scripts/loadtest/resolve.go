package main

import (
	"context"
	"fmt"
	"time"
)

// The pause between two attempts of the resolution: short at first, because a
// replica killed by force is replaced by the balancer within a second or two,
// and never longer than the base of the backoff the rest of the system uses.
const (
	firstPause = 100 * time.Millisecond
	lastPause  = time.Second
)

// resolveAll sends each operation that has no decided answer again, key and
// body alike, until it gets one, and answers a finding for each that did not
// within the bound. The answer is either the replay of what had been recorded
// or the first conclusion of the operation: the idempotency is what makes the
// outcome of an interrupted arrival recoverable.
func (l *load) resolveAll(ctx context.Context) []string {
	deadline := time.Now().Add(l.opts.resolveWait)
	var findings []string
	for _, op := range l.undecided() {
		if !l.resolveOne(ctx, op, deadline) {
			findings = append(findings, fmt.Sprintf("key %s has no decided answer after %s of resolution", op.key, l.opts.resolveWait))
		}
	}
	return findings
}

// undecided answers the operations that neither got a decided answer nor an
// unexpected one: the unexpected are already a finding, and sending them again
// would only repeat it.
func (l *load) undecided() []*operation {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*operation
	for _, op := range l.operations {
		if _, known := l.decisions[op.key]; !known && !l.surprised[op.key] {
			out = append(out, op)
		}
	}
	return out
}

// resolveOne reports whether the operation got a decided answer before the
// deadline. An unexpected answer ends the attempts: it is counted as one.
func (l *load) resolveOne(ctx context.Context, op *operation, deadline time.Time) bool {
	for pause := firstPause; time.Now().Before(deadline) && ctx.Err() == nil; pause = min(2*pause, lastPause) {
		got := l.arrive(ctx, l.api, op)
		switch got.class {
		case decided:
			l.mu.Lock()
			l.decide(op, got)
			l.mu.Unlock()
			return true
		case unexpected:
			l.mu.Lock()
			l.surprise(op, got)
			l.mu.Unlock()
			return true
		}
		select {
		case <-ctx.Done():
		case <-time.After(pause):
		}
	}
	return false
}
