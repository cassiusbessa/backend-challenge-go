package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The span marks an error only from the server-error boundary up. A rule that
// answered and a refusal of the contract leave it ok, which is what keeps a
// rejection out of the error rate of the dashboard.
func TestMarkSpan_marksAnErrorOnlyFromTheServerErrorBoundaryUp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		marked bool
	}{
		{status: http.StatusOK, marked: false},
		{status: http.StatusUnprocessableEntity, marked: false},
		{status: http.StatusInternalServerError - 1, marked: false},
		{status: http.StatusInternalServerError, marked: true},
		{status: http.StatusServiceUnavailable, marked: true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("the status %d", tc.status), func(t *testing.T) {
			t.Parallel()
			if got := markedAsError(t, tc.status); got != tc.marked {
				t.Fatalf("span marked as error = %t, want %t at the status %d", got, tc.marked, tc.status)
			}
		})
	}
}

// markedAsError answers whether markSpan left the span in the error state for that
// status, read back from a recorder instead of from the call.
func markedAsError(t *testing.T, status int) bool {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer = %v, want nil", err)
		}
	})
	_, span := provider.Tracer("test").Start(context.Background(), "answer")
	markSpan(span, status)
	span.End()
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want exactly 1", len(ended))
	}
	return ended[0].Status().Code == codes.Error
}
