package httpapi

import (
	"slices"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// The latency of every pair the routes answer exists before the first request,
// and creating it observes nothing.
func TestNewMetrics_primesTheLatencyOfEveryAnsweredPairAtZero(t *testing.T) {
	t.Parallel()
	reg, _ := NewMetrics()
	pairs := latencyPairs(t, reg)
	want := 0
	for _, statuses := range answered {
		want += len(statuses)
	}
	if len(pairs) != want {
		t.Fatalf("latency pairs primed = %d, want one per answered pair, %d", len(pairs), want)
	}
	for _, pair := range []string{"POST 201", "POST 202", "POST 422", "GET 404"} {
		if !slices.Contains(pairs, pair) {
			t.Fatalf("latency pairs %v, want %s among them", pairs, pair)
		}
	}
}

// latencyPairs answers each child of the latency as its method and status, and
// fails the test on a child that already holds an observation.
func latencyPairs(t *testing.T, reg *prometheus.Registry) []string {
	t.Helper()
	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather of a fresh registry = %v, want nil", err)
	}
	var pairs []string
	for _, family := range gathered {
		if family.GetName() != "http_request_duration_seconds" {
			continue
		}
		for _, sample := range family.GetMetric() {
			if observed := sample.GetHistogram().GetSampleCount(); observed != 0 {
				t.Fatalf("observations of a primed pair = %d, want 0", observed)
			}
			// The labels are gathered in the order of their names: code, method.
			labels := sample.GetLabel()
			pairs = append(pairs, labels[1].GetValue()+" "+labels[0].GetValue())
		}
	}
	return pairs
}
