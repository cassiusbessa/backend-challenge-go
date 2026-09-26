package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The percentiles are exact over the samples: the nearest rank, one sample is
// every percentile of itself, and no sample answers zero.
func TestPercentile_answersTheNearestRankOfTheSamples(t *testing.T) {
	t.Parallel()
	hundred := make([]time.Duration, 0, 100)
	for at := 1; at <= 100; at++ {
		hundred = append(hundred, time.Duration(at)*time.Millisecond)
	}
	three := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}
	one := []time.Duration{7 * time.Millisecond}
	for _, tc := range []struct {
		name    string
		samples []time.Duration
		p       float64
		want    time.Duration
	}{
		{name: "p50 of a hundred", samples: hundred, p: 50, want: 50 * time.Millisecond},
		{name: "p95 of a hundred", samples: hundred, p: 95, want: 95 * time.Millisecond},
		{name: "p99 of a hundred", samples: hundred, p: 99, want: 99 * time.Millisecond},
		{name: "p50 of three", samples: three, p: 50, want: 20 * time.Millisecond},
		{name: "p99 of three", samples: three, p: 99, want: 30 * time.Millisecond},
		{name: "p50 of one", samples: one, p: 50, want: 7 * time.Millisecond},
		{name: "p99 of one", samples: one, p: 99, want: 7 * time.Millisecond},
		{name: "p99 of none", samples: nil, p: 99, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := percentile(tc.samples, tc.p); got != tc.want {
				t.Fatalf("percentile %s = %s, want %s", tc.name, got, tc.want)
			}
		})
	}
}

// The throughput is the decided arrivals over the window, and the latency is
// theirs alone: an error or a surprise has no latency to report.
func TestClientReport_countsTheDecidedArrivalsOfTheWindow(t *testing.T) {
	t.Parallel()
	l := newLoad(options{target: "compose", replicas: 3})
	for _, took := range []time.Duration{30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond} {
		l.record(&operation{key: "k-" + took.String()}, answer{class: decided, status: statusProcessed}, took)
	}
	l.record(&operation{key: "k-failed"}, answer{class: failed}, time.Second)
	got := l.clientReport(2 * time.Second)
	if got.Sent != 4 || got.Decided != 3 || got.Errors != 1 || got.ThroughputPerSecond != 1.5 {
		t.Fatalf("report = %d sent, %d decided, %d errors, %.2f/s; want 4, 3, 1 and 1.5/s", got.Sent, got.Decided, got.Errors, got.ThroughputPerSecond)
	}
	if got.Latency != (latency{P50: 20, P95: 30, P99: 30}) {
		t.Fatalf("latency = %+v, want p50 20ms and p95, p99 30ms over the decided alone", got.Latency)
	}
}

// Every figure the specification lists is in the file, even the empty ones:
// a missing key would read as a figure nobody measured.
func TestWrite_publishesEveryFigureOfTheSpecification(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "load", "report.json")
	published := newLoad(options{target: "cluster"}).clientReport(time.Second)
	published.Kill = &killRecord{Replica: "wager-abc", Mode: killForced, At: "2026-09-26T12:00:30Z", AfterSeconds: 30}
	var out strings.Builder
	if err := published.write(&out, path); err != nil {
		t.Fatalf("write = %v, want nil", err)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the temporary directory of the case
	if err != nil {
		t.Fatalf("read the report back = %v, want nil", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("report = %v, want JSON", err)
	}
	for _, name := range []string{
		"durationSeconds", "sent", "throughputPerSecond", "latencyMilliseconds",
		"errors", "rejections", "replays", "versionConflicts",
		"outboxOldestPendingMaxSeconds", "outboxDrainSeconds", "poolEmptyAcquires",
		"decidedByReplica", "kill", "passed", "failures",
	} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("report = %s, want %q in it", raw, name)
		}
	}
	for _, name := range []string{`"p50"`, `"p95"`, `"p99"`, `"replica": "wager-abc"`, `"rejections": {}`, `"failures": []`} {
		if !strings.Contains(string(raw), name) {
			t.Fatalf("report = %s, want %s in it", raw, name)
		}
	}
	if !strings.Contains(out.String(), "killed wager-abc (forced) 30.0s into the window") || !strings.Contains(out.String(), path) {
		t.Fatalf("output = %q, want the kill and the path of the report", out.String())
	}
}

// The output says whether the verdict passed and how many findings failed it.
func TestPrint_saysWhetherTheVerdictPassed(t *testing.T) {
	t.Parallel()
	var passed, failed strings.Builder
	report{Passed: true}.print(&passed)
	report{Failures: []string{"wallet w: version = 3, want 2"}}.print(&failed)
	if !strings.Contains(passed.String(), "every wallet closes") || !strings.Contains(failed.String(), "failed with 1 finding(s)") {
		t.Fatalf("outputs = %q and %q, want the pass and the count of findings", passed.String(), failed.String())
	}
}

// A report path that cannot be written is a failure of the run, not a silence.
func TestWrite_failsWhereTheReportCannotBeWritten(t *testing.T) {
	t.Parallel()
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatalf("write the blocking file = %v, want nil", err)
	}
	if err := (report{}).write(&strings.Builder{}, filepath.Join(blocked, "report.json")); err == nil {
		t.Fatalf("write under a file = nil, want the failure")
	}
}

// A map is listed in the order of its keys, so two runs read alike.
func TestListed_writesTheKeysInOrder(t *testing.T) {
	t.Parallel()
	if got := listed(map[string]int{"b": 2, "a": 1}); got != "a 1, b 2" {
		t.Fatalf("listed = %q, want the keys in order", got)
	}
	if got := listed(map[string]float64{}); got != "none" {
		t.Fatalf("listed of nothing = %q, want none", got)
	}
}
