package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// run is one execution: open the wallets, send for the window, resolve what
// was left undecided by its own key, and hold every wallet to what was counted.
func run(ctx context.Context, o options, stdout io.Writer) error {
	l := newLoad(o)
	if err := l.open(ctx); err != nil {
		return err
	}
	start := time.Now()
	l.send(ctx, start.Add(o.duration))
	window := time.Since(start)
	unresolved := l.resolveAll(ctx)
	published := l.clientReport(window)
	published.Failures = append(published.Failures, l.failures()...)
	published.Failures = append(published.Failures, unresolved...)
	published.Failures = append(published.Failures, l.check(ctx)...)
	published.Passed = len(published.Failures) == 0
	if err := published.write(stdout, o.report); err != nil {
		return err
	}
	return verdictOf(published.Failures)
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
