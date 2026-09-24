package clock

import (
	"testing"
	"time"
)

func TestNow_answersInUTC(t *testing.T) {
	t.Parallel()
	reader := UTC{}
	if got := reader.Now().Location(); got != time.UTC {
		t.Fatalf("location = %s, want UTC", got)
	}
}

func TestNow_movesForward(t *testing.T) {
	t.Parallel()
	reader := UTC{}
	first := reader.Now()
	second := reader.Now()
	if second.Before(first) {
		t.Fatalf("second reading = %s, want it at or after %s", second, first)
	}
}
