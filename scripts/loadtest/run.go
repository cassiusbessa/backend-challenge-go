package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// run is one execution: open the wallets, send for the window, and answer
// whether anything the load saw is a defect.
func run(ctx context.Context, o options, stdout io.Writer) error {
	l := newLoad(o)
	if err := l.open(ctx); err != nil {
		return err
	}
	start := time.Now()
	l.send(ctx, start.Add(o.duration))
	fmt.Fprintf(stdout, "sent %d arrivals: %d decided, %d errors, %d replays\n", l.sent, len(l.latencies), l.errors, l.replays)
	return verdictOf(l.failures())
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
