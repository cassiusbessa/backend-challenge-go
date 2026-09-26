package postgres

import (
	"context"
	"strings"
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
		t.Fatalf("Open of the pool the case reads = %v, want nil", err)
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

// The collector reads the pool it was given and no other: a closed pool beside
// an open one answers nothing while the open one answers its five samples.
func TestNewPoolStats_readsThePoolItWasGivenAndNoOther(t *testing.T) {
	t.Parallel()
	open := openedPool(t, unreachable)
	closed := NewPool(config.Config{DatabaseURL: unreachable})
	if got := collected(NewPoolStats(open)); got != 5 {
		t.Fatalf("samples of the open pool = %d, want 5", got)
	}
	if got := collected(NewPoolStats(closed)); got != 0 {
		t.Fatalf("samples of the closed pool beside it = %d, want 0", got)
	}
}

// Describe names the three series whatever state the pool is in, which is what
// lets the registry check the names once, at registration.
func TestDescribe_namesTheThreeSeries(t *testing.T) {
	t.Parallel()
	described := make(chan *prometheus.Desc, 8)
	NewPoolStats(NewPool(config.Config{DatabaseURL: unreachable})).Describe(described)
	close(described)
	var names []string
	for desc := range described {
		names = append(names, desc.String())
	}
	if len(names) != 3 {
		t.Fatalf("descriptions = %d, want the three series", len(names))
	}
	for _, name := range []string{"wager_db_pool_connections", "wager_db_pool_max_connections", "wager_db_pool_empty_acquires_total"} {
		if !strings.Contains(strings.Join(names, " "), name) {
			t.Fatalf("descriptions = %v, want %s among them", names, name)
		}
	}
}

// Collect is the scrape: five samples from an open pool, and none from one the
// process has not opened.
func TestCollect_sendsTheSamplesOfAnOpenPoolOnly(t *testing.T) {
	t.Parallel()
	if got := collected(NewPoolStats(openedPool(t, unreachable))); got != 5 {
		t.Fatalf("samples sent by Collect of an open pool = %d, want 5", got)
	}
	if got := collected(NewPoolStats(NewPool(config.Config{DatabaseURL: unreachable}))); got != 0 {
		t.Fatalf("samples sent by Collect of a pool never opened = %d, want 0", got)
	}
}

// emit answers one sample per state, the ceiling and the empty acquires, read
// off the bookkeeping of the pool.
func TestEmit_answersTheThreeStatesTheCeilingAndTheEmptyAcquires(t *testing.T) {
	t.Parallel()
	pool, err := openedPool(t, unreachable+"&pool_max_conns=3").Querier()
	if err != nil {
		t.Fatalf("Querier of the opened pool = %v, want nil", err)
	}
	sent := make(chan prometheus.Metric, 8)
	emit(sent, pool.Stat())
	close(sent)
	var emitted []prometheus.Metric
	for metric := range sent {
		emitted = append(emitted, metric)
	}
	if ceiling := ceilingOf(t, emitted); len(emitted) != 5 || ceiling != 3 {
		t.Fatalf("emitted %d samples with a ceiling of %v, want 5 with the 3 of the configuration", len(emitted), ceiling)
	}
}

// ceilingOf reads the sample of the ceiling out of what emit sent.
func ceilingOf(t *testing.T, emitted []prometheus.Metric) float64 {
	t.Helper()
	for _, metric := range emitted {
		if !strings.Contains(metric.Desc().String(), "wager_db_pool_max_connections") {
			continue
		}
		var out dto.Metric
		if err := metric.Write(&out); err != nil {
			t.Fatalf("Write of the emitted ceiling = %v, want nil", err)
		}
		return out.GetGauge().GetValue()
	}
	return 0
}

// collected runs Collect the way the registry does and answers how many
// samples it sent.
func collected(stats *PoolStats) int {
	sent := make(chan prometheus.Metric, 8)
	stats.Collect(sent)
	close(sent)
	count := 0
	for range sent {
		count++
	}
	return count
}
