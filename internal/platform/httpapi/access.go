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

// Routes são os handlers públicos do processo.
type Routes struct {
	Live    http.Handler
	Ready   http.Handler
	Metrics http.Handler
	Logger  *slog.Logger
	Tracer  trace.Tracer
	Latency *prometheus.HistogramVec
}

// Handler publica só live, ready e metrics. O resto responde 404.
func Handler(routes Routes) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /health/live", routes.wrap("GET /health/live", routes.Live))
	mux.Handle("GET /health/ready", routes.wrap("GET /health/ready", routes.Ready))
	mux.Handle("GET /metrics", routes.wrap("GET /metrics", routes.Metrics))
	mux.Handle("/", routes.wrap("unmatched", http.HandlerFunc(notFound)))
	return mux
}

func notFound(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}

func (routes Routes) wrap(name string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routes.serve(r.Context(), name, next, w, r)
	})
}

func (routes Routes) serve(ctx context.Context, name string, next http.Handler, w http.ResponseWriter, r *http.Request) {
	ctx, span := routes.Tracer.Start(extract(ctx, r), name)
	defer span.End()
	started := time.Now()
	writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(writer, r.WithContext(ctx))
	routes.finish(ctx, span, r, writer.status, time.Since(started))
}

func extract(ctx context.Context, r *http.Request) context.Context {
	return propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(r.Header))
}

func (routes Routes) finish(ctx context.Context, span trace.Span, r *http.Request, status int, elapsed time.Duration) {
	markSpan(span, status)
	routes.observe(ctx, r.Method, status, elapsed)
	routes.log(ctx, span, r.Header.Get("X-Correlation-Id"), status)
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

func (routes Routes) log(ctx context.Context, span trace.Span, header string, status int) {
	sc := span.SpanContext()
	traceID := sc.TraceID().String()
	routes.Logger.LogAttrs(ctx, slog.LevelInfo, "pedido",
		slog.String("correlationId", telemetry.CorrelationID(header, traceID)),
		slog.String("trace_id", traceID),
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
