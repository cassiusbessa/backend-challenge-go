package relayoutbox

import (
	"testing"
	"time"
)

func TestBackoff_doublesEachAttemptAndStopsAtTheCeiling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		attempts int64
		want     time.Duration
	}{
		{attempts: 0, want: time.Second},
		{attempts: 1, want: 2 * time.Second},
		{attempts: 2, want: 4 * time.Second},
		{attempts: 5, want: 32 * time.Second},
		{attempts: 6, want: 60 * time.Second},
		{attempts: 40, want: 60 * time.Second},
	}
	for _, tc := range cases {
		if got := Backoff(tc.attempts); got != tc.want {
			t.Fatalf("Backoff after %d attempts = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}
