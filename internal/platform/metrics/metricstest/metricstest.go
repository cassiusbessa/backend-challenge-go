// Package metricstest reads the business series back in a test.
package metricstest

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Sum answers the total of every counter the collector holds.
//
// The vectors are primed at zero, so they hold their children before any
// traffic moves them: counting the children no longer tells whether something
// moved, and the total does.
func Sum(t testing.TB, c prometheus.Collector) float64 {
	t.Helper()
	collected := make(chan prometheus.Metric)
	go func() {
		c.Collect(collected)
		close(collected)
	}()
	var total float64
	for metric := range collected {
		var sample dto.Metric
		if err := metric.Write(&sample); err != nil {
			t.Errorf("write of a collected metric = %v, want nil", err)
			continue
		}
		total += sample.GetCounter().GetValue()
	}
	return total
}
