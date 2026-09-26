package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// report is what one run publishes, on the output and in the file. The figures
// of the server side come from the metric backend and cover the window; the
// rest are counted by the load itself. No figure is held to a goal: the verdict
// is consistency, not throughput.
type report struct {
	Target      string `json:"target"`
	Replicas    int    `json:"replicas"`
	Concurrency int    `json:"concurrency"`
	Wallets     int    `json:"wallets"`
	Seed        uint64 `json:"seed"`

	DurationSeconds     float64 `json:"durationSeconds"`
	Sent                int     `json:"sent"`
	Decided             int     `json:"decided"`
	ThroughputPerSecond float64 `json:"throughputPerSecond"`
	Latency             latency `json:"latencyMilliseconds"`

	Errors     int            `json:"errors"`
	Rejections map[string]int `json:"rejections"`
	Replays    int            `json:"replays"`

	VersionConflicts           float64            `json:"versionConflicts"`
	OutboxOldestPendingSeconds float64            `json:"outboxOldestPendingMaxSeconds"`
	OutboxDrainSeconds         float64            `json:"outboxDrainSeconds"`
	PoolEmptyAcquires          float64            `json:"poolEmptyAcquires"`
	DecidedByReplica           map[string]float64 `json:"decidedByReplica"`

	Kill *killRecord `json:"kill"`

	Passed   bool     `json:"passed"`
	Failures []string `json:"failures"`
}

// latency is the percentiles of the decided arrivals, in milliseconds.
type latency struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
}

// killRecord is the replica a run stopped or killed, when, and how.
type killRecord struct {
	Replica      string  `json:"replica"`
	Mode         string  `json:"mode"`
	At           string  `json:"at"`
	AfterSeconds float64 `json:"afterSeconds"`
}

// clientReport answers the figures the load counted itself over the window.
func (l *load) clientReport(window time.Duration) report {
	l.mu.Lock()
	defer l.mu.Unlock()
	sorted := slices.Clone(l.latencies)
	slices.Sort(sorted)
	return report{
		Target:              l.opts.target,
		Replicas:            l.opts.replicas,
		Concurrency:         l.opts.concurrency,
		Wallets:             l.opts.wallets,
		Seed:                l.opts.seed,
		DurationSeconds:     window.Seconds(),
		Sent:                l.sent,
		Decided:             len(sorted),
		ThroughputPerSecond: float64(len(sorted)) / window.Seconds(),
		Latency: latency{
			P50: milliseconds(percentile(sorted, 50)),
			P95: milliseconds(percentile(sorted, 95)),
			P99: milliseconds(percentile(sorted, 99)),
		},
		Errors:           l.errors,
		Rejections:       maps.Clone(l.rejections),
		Replays:          l.replays,
		DecidedByReplica: map[string]float64{},
		Failures:         []string{},
	}
}

// percentile answers the nearest-rank percentile of samples already sorted:
// the smallest sample at or below which that share of the samples falls. It is
// exact over every sample, with no histogram in between; one sample is every
// percentile of itself, and none answers zero.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	return sorted[max(rank, 1)-1]
}

func milliseconds(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000
}

// write prints the report for the operator and writes it for the machine.
func (r report) write(stdout io.Writer, path string) error {
	r.print(stdout)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create the directory of the report: %w", err)
	}
	encoded, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the report: %w", err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil { //nolint:gosec // the path comes from a flag the operator controls
		return fmt.Errorf("write the report: %w", err)
	}
	fmt.Fprintf(stdout, "report written to %s\n", path)
	return nil
}

func (r report) print(out io.Writer) {
	fmt.Fprintf(out, "target %s: %d replicas, %d workers, %d wallets, seed %d\n", r.Target, r.Replicas, r.Concurrency, r.Wallets, r.Seed)
	fmt.Fprintf(out, "window %.1fs: %d sent, %d decided, %.1f decided/s\n", r.DurationSeconds, r.Sent, r.Decided, r.ThroughputPerSecond)
	fmt.Fprintf(out, "latency of the decided: p50 %.1fms, p95 %.1fms, p99 %.1fms\n", r.Latency.P50, r.Latency.P95, r.Latency.P99)
	fmt.Fprintf(out, "errors %d, replays %d, rejections %s\n", r.Errors, r.Replays, listed(r.Rejections))
	fmt.Fprintf(out, "version conflicts %.0f, acquisitions on an empty pool %.0f\n", r.VersionConflicts, r.PoolEmptyAcquires)
	fmt.Fprintf(out, "outbox: oldest pending at most %.1fs, drained %.1fs after the window, read at the resolution of the scrape\n", r.OutboxOldestPendingSeconds, r.OutboxDrainSeconds)
	fmt.Fprintf(out, "decided by replica: %s\n", listed(r.DecidedByReplica))
	if r.Kill != nil {
		fmt.Fprintf(out, "killed %s (%s) %.1fs into the window, at %s\n", r.Kill.Replica, r.Kill.Mode, r.Kill.AfterSeconds, r.Kill.At)
	}
	if r.Passed {
		fmt.Fprintln(out, "verdict: every wallet closes")
		return
	}
	fmt.Fprintf(out, "verdict: failed with %d finding(s)\n", len(r.Failures))
}

// listed writes a map in the order of its keys, so two runs read alike.
func listed[V int | float64](values map[string]V) string {
	if len(values) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(values))
	for _, key := range slices.Sorted(maps.Keys(values)) {
		parts = append(parts, fmt.Sprintf("%s %v", key, values[key]))
	}
	return strings.Join(parts, ", ")
}
