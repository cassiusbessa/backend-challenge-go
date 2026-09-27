package httpapi

import (
	"net/http"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// answered is every method and status the routes answer. The latency of each
// pair exists at zero before the first request, for the reason the business
// series are primed: rate() reads the first sample of a series as its baseline,
// and the first requests of a pair on a replica would never reach the p99.
var answered = map[string][]int{
	http.MethodGet: {
		http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusInternalServerError, http.StatusServiceUnavailable,
	},
	http.MethodPost: {
		http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusBadRequest,
		http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
		http.StatusUnprocessableEntity, http.StatusInternalServerError, http.StatusServiceUnavailable,
	},
}

func NewMetrics() (*prometheus.Registry, *prometheus.HistogramVec) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	latency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "code"})
	for method, statuses := range answered {
		for _, status := range statuses {
			latency.WithLabelValues(method, strconv.Itoa(status))
		}
	}
	reg.MustRegister(latency)
	return reg, latency
}

func MetricsHandler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{EnableOpenMetrics: true})
}
