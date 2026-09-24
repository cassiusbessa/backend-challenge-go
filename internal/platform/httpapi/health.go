// Package httpapi serve live, ready e metrics.
package httpapi

import (
	"context"
	"net/http"
	"time"
)

// PostgresChecker consulta o PostgreSQL.
type PostgresChecker interface {
	Check(context.Context) error
}

// QueueChecker consulta a fila SQS.
type QueueChecker interface {
	Check(context.Context) error
}

// Ready consulta PostgreSQL e a fila, cada uma com prazo próprio.
type Ready struct {
	postgres PostgresChecker
	queue    QueueChecker
	timeout  time.Duration
}

// NewReady usa o prazo de 1s em cada consulta.
func NewReady(postgres PostgresChecker, queue QueueChecker) *Ready {
	return &Ready{postgres: postgres, queue: queue, timeout: time.Second}
}

// ServeHTTP responde 200 quando as duas consultas passam.
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

// Live responde 200 enquanto o processo aceita o pedido.
func Live(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}
