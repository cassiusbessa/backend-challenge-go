package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

type Routes struct {
	Live            http.Handler
	Ready           http.Handler
	Metrics         http.Handler
	OpenWallet      http.Handler
	ReadWallet      http.Handler
	ListLedger      http.Handler
	ReconcileWallet http.Handler
	SubmitWager     http.Handler
	ReadTransaction http.Handler
	Logger          *slog.Logger
	Tracer          trace.Tracer
	Latency         *prometheus.HistogramVec
}

func Handler(routes Routes) http.Handler {
	mux := http.NewServeMux()
	// Live, ready and metrics stay outside the span and the log: the probe and
	// the scrape run every second and would drown the trace and the histogram.
	mux.Handle("GET /health/live", routes.Live)
	mux.Handle("GET /health/ready", routes.Ready)
	mux.Handle("GET /metrics", routes.Metrics)
	// The span name is the route pattern and not the path: a wallet identity in
	// the name would give every request a series of its own.
	mux.Handle("POST /wallets", routes.wrap("POST /wallets", routes.OpenWallet))
	mux.Handle("GET /wallets/{walletId}", routes.wrap("GET /wallets/{walletId}", routes.ReadWallet))
	mux.Handle("GET /wallets/{walletId}/ledger", routes.wrap("GET /wallets/{walletId}/ledger", routes.ListLedger))
	mux.Handle("GET /wallets/{walletId}/reconciliation", routes.wrap("GET /wallets/{walletId}/reconciliation", routes.ReconcileWallet))
	mux.Handle("POST /wagering/transactions", routes.wrap("POST /wagering/transactions", routes.SubmitWager))
	mux.Handle("GET /wagering/transactions/{transactionId}", routes.wrap("GET /wagering/transactions/{transactionId}", routes.ReadTransaction))
	mux.Handle("/", routes.wrap("unmatched", http.NotFoundHandler()))
	return mux
}

func (routes Routes) wrap(name string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes.serve(r.Context(), name, next, w, r)
	})
}

func (routes Routes) serve(ctx context.Context, name string, next http.Handler, w http.ResponseWriter, r *http.Request) {
	ctx, span := routes.Tracer.Start(extract(ctx, r), name)
	defer span.End()
	// The correlation is decided here, before the handler runs, because what the
	// operation writes carries it: an outbox row records the correlation of the
	// request that produced it, and the access line at the end is too late.
	ctx = telemetry.WithCorrelation(ctx, correlationOf(span, r))
	started := time.Now()
	writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(writer, r.WithContext(ctx))
	routes.finish(ctx, span, r, writer.status, time.Since(started))
}

// correlationOf answers the token of the request, which is the header when it is
// a short opaque one and the trace otherwise.
func correlationOf(span trace.Span, r *http.Request) string {
	return telemetry.CorrelationID(r.Header.Get("X-Correlation-Id"), span.SpanContext().TraceID().String())
}

func extract(ctx context.Context, r *http.Request) context.Context {
	return propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(r.Header))
}

func (routes Routes) finish(ctx context.Context, span trace.Span, r *http.Request, status int, elapsed time.Duration) {
	markSpan(span, status)
	routes.observe(ctx, r.Method, status, elapsed)
	routes.log(ctx, span, status)
}

func markSpan(span trace.Span, status int) {
	if status < http.StatusInternalServerError {
		return
	}
	span.SetStatus(codes.Error, "unavailable")
}

func (routes Routes) observe(ctx context.Context, method string, status int, elapsed time.Duration) {
	observer := routes.Latency.WithLabelValues(method, strconv.Itoa(status))
	seconds := elapsed.Seconds()
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		observer.Observe(seconds)
		return
	}
	exemplify(observer, seconds, sc.TraceID().String())
}

func exemplify(observer prometheus.Observer, seconds float64, traceID string) {
	ex, ok := observer.(prometheus.ExemplarObserver)
	if !ok {
		observer.Observe(seconds)
		return
	}
	ex.ObserveWithExemplar(seconds, prometheus.Labels{"trace_id": traceID})
}

func (routes Routes) log(ctx context.Context, span trace.Span, status int) {
	sc := span.SpanContext()
	routes.Logger.LogAttrs(ctx, slog.LevelInfo, "request",
		slog.String("correlationId", telemetry.Correlation(ctx)),
		slog.String("trace_id", sc.TraceID().String()),
		slog.String("span_id", sc.SpanID().String()),
		slog.String("status", strconv.Itoa(status)),
	)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
