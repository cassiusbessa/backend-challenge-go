package httpapi

import (
	"context"
	"net/http"
	"time"
)

type PostgresChecker interface {
	Check(context.Context) error
}

type QueueChecker interface {
	Check(context.Context) error
}

type Ready struct {
	postgres PostgresChecker
	queue    QueueChecker
	timeout  time.Duration
}

func NewReady(postgres PostgresChecker, queue QueueChecker) *Ready {
	return &Ready{postgres: postgres, queue: queue, timeout: time.Second}
}

func (h *Ready) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h.probe(r.Context(), h.postgres); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := h.probe(r.Context(), h.queue); err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *Ready) probe(parent context.Context, checker interface{ Check(context.Context) error }) error {
	ctx, cancel := context.WithTimeout(parent, h.timeout)
	defer cancel()
	return checker.Check(ctx)
}

func Live(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
