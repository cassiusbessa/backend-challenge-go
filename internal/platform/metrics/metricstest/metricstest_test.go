package metricstest

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestSum_addsEveryChildOfTheVectorAndCountsAPrimedOneAsZero(t *testing.T) {
	t.Parallel()
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "moved_total", Help: "Moved."}, []string{"reason"})
	vec.WithLabelValues("primed")
	if got := Sum(t, vec); got != 0 {
		t.Fatalf("Sum of a vector primed at zero = %v, want 0", got)
	}
	vec.WithLabelValues("first").Add(2)
	vec.WithLabelValues("second").Inc()
	if got := Sum(t, vec); got != 3 {
		t.Fatalf("Sum after two children moved = %v, want 3", got)
	}
}
