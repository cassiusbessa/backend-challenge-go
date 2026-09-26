package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// PoolStats reads the saturation of the pool at scrape time and answers it as
// three series. The zero value collects nothing: NewPoolStats is the only
// constructor.
//
// It reads pgxpool.Stat, which is bookkeeping of the process and never a
// round trip to the database, so a scrape while the database is out still
// answers — with the pool empty of idle connections, which is the point.
type PoolStats struct {
	source *Pool
}

func NewPoolStats(source *Pool) *PoolStats {
	return &PoolStats{source: source}
}

var (
	poolConnections = prometheus.NewDesc("wager_db_pool_connections",
		"Connections of the pool, by state.", []string{"state"}, nil)
	poolMax = prometheus.NewDesc("wager_db_pool_max_connections",
		"Ceiling of the pool.", nil, nil)
	poolEmptyAcquires = prometheus.NewDesc("wager_db_pool_empty_acquires_total",
		"Acquisitions that found the pool empty and waited.", nil, nil)
)

// The states of a connection, as pgxpool counts them. They are label values,
// so they are a closed set of short tokens.
const (
	stateAcquired     = "acquired"
	stateIdle         = "idle"
	stateConstructing = "constructing"
)

func (c *PoolStats) Describe(ch chan<- *prometheus.Desc) {
	ch <- poolConnections
	ch <- poolMax
	ch <- poolEmptyAcquires
}

// Collect answers nothing while the pool is closed: the series disappear from
// the scrape instead of reading zero, which is what a gauge of saturation has
// to do outside the window the pool is open in.
func (c *PoolStats) Collect(ch chan<- prometheus.Metric) {
	pool, err := c.source.Querier()
	if err != nil {
		return
	}
	emit(ch, pool.Stat())
}

func emit(ch chan<- prometheus.Metric, stat *pgxpool.Stat) {
	ch <- prometheus.MustNewConstMetric(poolConnections, prometheus.GaugeValue, float64(stat.AcquiredConns()), stateAcquired)
	ch <- prometheus.MustNewConstMetric(poolConnections, prometheus.GaugeValue, float64(stat.IdleConns()), stateIdle)
	ch <- prometheus.MustNewConstMetric(poolConnections, prometheus.GaugeValue, float64(stat.ConstructingConns()), stateConstructing)
	ch <- prometheus.MustNewConstMetric(poolMax, prometheus.GaugeValue, float64(stat.MaxConns()))
	ch <- prometheus.MustNewConstMetric(poolEmptyAcquires, prometheus.CounterValue, float64(stat.EmptyAcquireCount()))
}
