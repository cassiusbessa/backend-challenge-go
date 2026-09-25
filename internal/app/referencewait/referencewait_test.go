package referencewait

import (
	"testing"
	"time"
)

// entered is the instant every case below starts from, injected rather than
// read: nothing here touches the wall clock.
var entered = time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)

// ttl is the default of the rule, which is what the cases about the deadline are
// written against.
const ttl = 15 * time.Minute

func TestDeadlineAt_answersTheEntryPlusTheConfiguredTTL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ttl  time.Duration
		want time.Time
	}{
		{name: "the default of fifteen minutes", ttl: ttl, want: entered.Add(15 * time.Minute)},
		{name: "a TTL the configuration shortened", ttl: time.Second, want: entered.Add(time.Second)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := New(tc.ttl, whole).DeadlineAt(entered)
			if !got.Equal(tc.want) {
				t.Fatalf("DeadlineAt = %s, want %s", got, tc.want)
			}
		})
	}
}

// The schedule takes the window of the attempt from windowOf, which the table of
// that function pins: what is asserted here is that it is the one used.
func TestNextAttemptAt_drawsInsideTheWindowOfThatAttempt(t *testing.T) {
	t.Parallel()
	deadline := entered.Add(ttl)
	first := New(ttl, whole).NextAttemptAt(0, entered, deadline)
	if !first.Equal(entered.Add(time.Second)) {
		t.Fatalf("NextAttemptAt of the first attempt = %s, want %s", first, entered.Add(time.Second))
	}
	long := New(ttl, whole).NextAttemptAt(40, entered, deadline)
	if !long.Equal(entered.Add(60 * time.Second)) {
		t.Fatalf("NextAttemptAt of a long wait = %s, want the ceiling at %s", long, entered.Add(60*time.Second))
	}
}

// The draw sits anywhere between zero and the window, ends included: an interval
// of zero schedules the attempt at once.
func TestNextAttemptAt_takesTheIntervalTheJitterDrew(t *testing.T) {
	t.Parallel()
	deadline := entered.Add(ttl)
	for _, drawn := range []time.Duration{0, 250 * time.Millisecond, time.Second} {
		got := New(ttl, fixed(drawn)).NextAttemptAt(0, entered, deadline)
		if !got.Equal(entered.Add(drawn)) {
			t.Fatalf("NextAttemptAt with a draw of %s = %s, want %s", drawn, got, entered.Add(drawn))
		}
	}
}

// A draw longer than what is left of the wait schedules the deadline itself:
// past it, the row would sit expired with nobody due to close it.
func TestNextAttemptAt_capsTheScheduleAtTheDeadline(t *testing.T) {
	t.Parallel()
	now := entered.Add(14*time.Minute + 30*time.Second)
	deadline := entered.Add(ttl)
	got := New(ttl, fixed(40*time.Second)).NextAttemptAt(6, now, deadline)
	if !got.Equal(deadline) {
		t.Fatalf("NextAttemptAt = %s, want the deadline at %s", got, deadline)
	}
	if got.After(deadline) {
		t.Fatalf("NextAttemptAt = %s, want nothing past the deadline at %s", got, deadline)
	}
}

// The draw that lands exactly on the deadline is kept: the cap is about going
// past it, and the attempt at the deadline is the one that closes the wait.
func TestNextAttemptAt_keepsADrawThatLandsOnTheDeadline(t *testing.T) {
	t.Parallel()
	now := entered.Add(14*time.Minute + 30*time.Second)
	deadline := entered.Add(ttl)
	got := New(ttl, fixed(30*time.Second)).NextAttemptAt(6, now, deadline)
	if !got.Equal(deadline) {
		t.Fatalf("NextAttemptAt = %s, want %s", got, deadline)
	}
}

// FullJitter is the draw of production, and what it must never do is answer
// outside the window it was given.
func TestFullJitter_staysInsideTheWindowItWasGiven(t *testing.T) {
	t.Parallel()
	for _, window := range []time.Duration{time.Second, 60 * time.Second} {
		for range 200 {
			drawn := FullJitter(window)
			if drawn < 0 || drawn >= window {
				t.Fatalf("FullJitter of %s = %s, want it inside [0, %s)", window, drawn, window)
			}
		}
	}
}

// A window of zero or less has nothing to draw from, and the answer is the
// interval that schedules the attempt at once.
func TestFullJitter_answersZeroForAWindowWithNothingToDraw(t *testing.T) {
	t.Parallel()
	for _, window := range []time.Duration{0, -time.Second} {
		if drawn := FullJitter(window); drawn != 0 {
			t.Fatalf("FullJitter of %s = %s, want 0", window, drawn)
		}
	}
}

// whole takes the entire window, which is what makes the ceiling of each attempt
// readable in a case about the window and not about the draw.
func whole(window time.Duration) time.Duration {
	return window
}

func fixed(interval time.Duration) Jitter {
	return func(time.Duration) time.Duration { return interval }
}

// The window doubles per attempt already made and then stops: what is past the
// ceiling is the ceiling, and a wait that ran for a long time does not turn the
// count it carries into a long loop.
func TestWindowOf_doublesTheBaseAndStopsAtTheCeiling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		attempts int64
		want     time.Duration
	}{
		{attempts: 0, want: time.Second},
		{attempts: 1, want: 2 * time.Second},
		{attempts: 5, want: 32 * time.Second},
		{attempts: 6, want: 60 * time.Second},
		{attempts: 1 << 40, want: 60 * time.Second},
	}
	for _, tc := range cases {
		if got := windowOf(tc.attempts); got != tc.want {
			t.Fatalf("windowOf(%d) = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}
