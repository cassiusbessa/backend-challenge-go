package postgres

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

// pgxpool dials on the first acquire, so an open pool pointed at nothing
// answers its bookkeeping: nothing acquired, nothing idle, and the ceiling the
// configuration gave it.
func TestCollect_answersTheThreeSeriesOfAnOpenPoolWithoutDialling(t *testing.T) {
	t.Parallel()
	pool := openedPool(t, unreachable+"&pool_max_conns=7")
	if got := testutil.CollectAndCount(NewPoolStats(pool)); got != 5 {
		t.Fatalf("samples collected = %d, want the three states, the ceiling and the empty acquires", got)
	}
	values := gatheredValues(t, NewPoolStats(pool))
	if values["wager_db_pool_max_connections"] != 7 {
		t.Fatalf("max connections = %v, want the 7 of the configuration", values["wager_db_pool_max_connections"])
	}
	if values["wager_db_pool_connections{state=acquired}"] != 0 || values["wager_db_pool_connections{state=idle}"] != 0 {
		t.Fatalf("connections = %v, want none acquired and none idle before the first acquire", values)
	}
	if values["wager_db_pool_empty_acquires_total"] != 0 {
		t.Fatalf("empty acquires = %v, want 0 before any acquire", values["wager_db_pool_empty_acquires_total"])
	}
}

// Outside the window the pool is open in, the series disappear rather than
// lying zero: a saturation of zero would read as a pool with room in it.
func TestCollect_answersNothingWhileThePoolIsClosed(t *testing.T) {
	t.Parallel()
	pool := NewPool(config.Config{DatabaseURL: unreachable})
	if got := testutil.CollectAndCount(NewPoolStats(pool)); got != 0 {
		t.Fatalf("samples collected of a pool never opened = %d, want 0", got)
	}
	if err := pool.Open(context.Background()); err != nil {
		t.Fatalf("Open = %v, want nil", err)
	}
	if err := pool.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
	if got := testutil.CollectAndCount(NewPoolStats(pool)); got != 0 {
		t.Fatalf("samples collected of a closed pool = %d, want 0", got)
	}
}

// openedPool opens a pool pointed at nothing, which pgxpool allows: it dials
// on the first acquire and not before.
func openedPool(t *testing.T, url string) *Pool {
	t.Helper()
	pool := NewPool(config.Config{DatabaseURL: url})
	if err := pool.Open(context.Background()); err != nil {
		t.Fatalf("Open = %v, want nil", err)
	}
	t.Cleanup(func() { _ = pool.Close(context.Background()) })
	return pool
}

// gatheredValues registers the collector on a registry of its own and answers
// what one gather reads off it.
func gatheredValues(t *testing.T, collector prometheus.Collector) map[string]float64 {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(collector)
	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather = %v, want nil", err)
	}
	return valuesOf(gathered)
}

// valuesOf flattens a gather into name{label=value} keys, so a case names the
// sample it asserts on.
func valuesOf(gathered []*dto.MetricFamily) map[string]float64 {
	out := map[string]float64{}
	for _, family := range gathered {
		for _, sample := range family.GetMetric() {
			key := family.GetName()
			for _, label := range sample.GetLabel() {
				key += "{" + label.GetName() + "=" + label.GetValue() + "}"
			}
			out[key] = sample.GetGauge().GetValue() + sample.GetCounter().GetValue()
		}
	}
	return out
}
