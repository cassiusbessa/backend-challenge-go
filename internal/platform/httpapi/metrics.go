package httpapi

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewMetrics registra heap, goroutines e a latência HTTP.
func NewMetrics() (*prometheus.Registry, *prometheus.HistogramVec) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	latency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Latência HTTP em segundos.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "code"})
	reg.MustRegister(latency)
	return reg, latency
}

// MetricsHandler publica o registro em texto Prometheus, com exemplar.
func MetricsHandler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{EnableOpenMetrics: true})
}
