package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// run is one execution: open the wallets, send for the window — killing one
// replica halfway when asked —, resolve what was left undecided by its own key,
// hold every wallet to what was counted, and read what the replicas counted.
func run(ctx context.Context, o options, stdout io.Writer) error {
	l := newLoad(o)
	if err := l.open(ctx); err != nil {
		return err
	}
	start := time.Now()
	killed := make(chan killOutcome, 1)
	if o.kill != killNone {
		go func() {
			record, err := l.killHalfway(ctx, start)
			killed <- killOutcome{record: record, err: err}
		}()
	}
	l.send(ctx, start.Add(o.duration))
	window := time.Since(start)
	var kill killOutcome
	if o.kill != killNone {
		kill = <-killed
	}
	unresolved := l.resolveAll(ctx)
	published := l.clientReport(window)
	published.Kill = kill.record
	published.Failures = append(published.Failures, errorsOf(kill.err)...)
	published.Failures = append(published.Failures, l.failures()...)
	published.Failures = append(published.Failures, unresolved...)
	published.Failures = append(published.Failures, l.check(ctx)...)
	published.Failures = append(published.Failures, serverSide(ctx, o, &published, start, start.Add(window))...)
	published.Passed = len(published.Failures) == 0
	if err := published.write(stdout, o.report); err != nil {
		return err
	}
	return verdictOf(published.Failures)
}

// serverSide waits for the backend to cover the end of the window, reads what
// the replicas counted over it, and waits for the outbox to drain; it fills the
// report and answers what fails the run.
func serverSide(ctx context.Context, o options, published *report, start, end time.Time) []string {
	select {
	case <-ctx.Done():
		return []string{ctx.Err().Error()}
	case <-time.After(time.Until(end.Add(o.settle))):
	}
	source := newBackend(o)
	figures, err := source.figures(ctx, o.replicaLabel, start, time.Now())
	if err != nil {
		return []string{err.Error()}
	}
	published.VersionConflicts = figures.conflicts
	published.PoolEmptyAcquires = figures.emptyAcquires
	published.OutboxOldestPendingSeconds = figures.oldestPending
	published.DecidedByReplica = figures.byReplica
	var failures []string
	if figures.conflicts > 0 {
		failures = append(failures, fmt.Sprintf("version conflicts = %.0f, want 0: the lock of the wallet makes a conflict unreachable", figures.conflicts))
	}
	if deciding := len(figures.byReplica); deciding < o.replicas {
		failures = append(failures, fmt.Sprintf("replicas that decided arrivals = %d (%s), want at least %d", deciding, listed(figures.byReplica), o.replicas))
	}
	drained, err := source.drain(ctx, end, o.drainWait)
	if err != nil {
		return append(failures, err.Error())
	}
	published.OutboxDrainSeconds = drained.Seconds()
	return failures
}

// killOutcome is what the kill halfway did, or why it could not.
type killOutcome struct {
	record *killRecord
	err    error
}

// errorsOf answers the message of a failure, or nothing.
func errorsOf(err error) []string {
	if err == nil {
		return nil
	}
	return []string{err.Error()}
}

// failures answers what the arrivals themselves showed to be wrong.
func (l *load) failures() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := append([]string(nil), l.findings...)
	if l.unexpected > maxSurprises {
		out = append(out, fmt.Sprintf("%d unexpected answers, the first %d named above", l.unexpected, maxSurprises))
	}
	return out
}

// verdictOf answers nil for no failure, and one error naming each otherwise.
func verdictOf(failures []string) error {
	if len(failures) == 0 {
		return nil
	}
	return errors.New("verdict failed:\n  " + strings.Join(failures, "\n  "))
}
