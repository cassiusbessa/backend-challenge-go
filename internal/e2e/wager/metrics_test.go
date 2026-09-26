//go:build integration

// The series the wager route and the reference worker move, read off /metrics
// of the process under test. The process is fresh per case, so what the route
// counts is exactly what the case did; the worker sweeps the queue of the
// whole suite database, so its series are asserted as floors.
package wager

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

func TestSubmit_countsEveryOutcomeOfTheRoute(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	key := "key-" + suiteenv.NewID()
	external := "external-" + suiteenv.NewID()
	payload := wallet.bet("25.00", map[string]any{"externalTransactionId": external})
	if placed := submit(ctx, t, at, at.provider, key, payload); placed.status != http.StatusCreated {
		t.Fatalf("the bet = %d, want 201: %s", placed.status, placed.body)
	}
	if refused := submitBody(ctx, t, at, wallet.bet("2000.00", nil)); refused.status != http.StatusUnprocessableEntity {
		t.Fatalf("the bet past the balance = %d, want 422: %s", refused.status, refused.body)
	}
	if again := submit(ctx, t, at, at.provider, key, payload); again.status != http.StatusOK {
		t.Fatalf("the replay = %d, want 200: %s", again.status, again.body)
	}
	if other := submit(ctx, t, at, at.provider, key, wallet.bet("30.00", map[string]any{"externalTransactionId": external})); other.status != http.StatusUnprocessableEntity {
		t.Fatalf("another body under the same key = %d, want 422: %s", other.status, other.body)
	}
	if twin := submit(ctx, t, at, at.provider, "key-"+suiteenv.NewID(), payload); twin.status != http.StatusUnprocessableEntity {
		t.Fatalf("another key for the same external transaction = %d, want 422: %s", twin.status, twin.body)
	}
	scraped := scrape(ctx, t, at)
	assertExactly(t, scraped, map[string]float64{
		`wager_settlements_total{origin="http",kind="BET",status="PROCESSED"}`:    1,
		`wager_settlements_total{origin="http",kind="BET",status="REJECTED"}`:     1,
		`wager_rejections_total{origin="http",failure_code="INSUFFICIENT_FUNDS"}`: 1,
		`wager_duplicates_total{origin="http",reason="replay"}`:                   1,
		`wager_duplicates_total{origin="http",reason="key_conflict"}`:             1,
		`wager_duplicates_total{origin="http",reason="external_duplicate"}`:       1,
	})
}

// The wait is counted by the route when it is recorded, rescheduled by the
// worker at least once before a deadline it cannot reach on the first attempt,
// and closed by the worker under its own origin with the token of the clock.
func TestWorker_countsTheWaitsItReschedulesAndCloses(t *testing.T) {
	// A TTL of three seconds: the first attempt is drawn inside the base window
	// of one second, so at least one reschedule happens before the deadline, and
	// the deadline still comes inside the case.
	ctx, at := startWith(t, map[string]string{"REFERENCE_TTL": "3s", "REFERENCE_INTERVAL": "50ms"})
	wallet := openWallet(ctx, t, at)
	waiting := assertWaits(ctx, t, at, wallet.win("50.00", citing("external-"+suiteenv.NewID(), "round-"+suiteenv.NewID())))
	assertExactly(t, scrape(ctx, t, at), map[string]float64{
		`wager_settlements_total{origin="http",kind="WIN",status="PENDING_REFERENCE"}`: 1,
	})
	awaitRejection(ctx, t, at, waiting, "REFERENCE_NOT_FOUND")
	// The worker of this process closes every due wait of the suite database,
	// not only the one of this case, so the series under its origin are floors.
	// They are polled: the worker counts right after the commit the read above
	// already saw, and a background component is read through a poll.
	awaitAtLeast(ctx, t, at, map[string]float64{
		`wager_retries_total{component="reference",reason="reference_pending"}`:         1,
		`wager_settlements_total{origin="reference",kind="WIN",status="REJECTED"}`:      1,
		`wager_rejections_total{origin="reference",failure_code="REFERENCE_NOT_FOUND"}`: 1,
	})
}

func scrape(ctx context.Context, t *testing.T, at suite) suiteenv.Scrape {
	t.Helper()
	scraped, err := suiteenv.ScrapeMetrics(ctx, at.base)
	if err != nil {
		t.Fatalf("scrape the process = %v, want nil", err)
	}
	return scraped
}

// assertExactly reads each series named as name{label="value",...} and demands
// the value: the process is fresh per case, so the count is the case's alone.
func assertExactly(t *testing.T, scraped suiteenv.Scrape, want map[string]float64) {
	t.Helper()
	for series, count := range want {
		name, labels := parseSeries(t, series)
		got, _ := scraped.Value(name, labels)
		if got != count {
			t.Fatalf("%s = %v, want %v", series, got, count)
		}
	}
}

// awaitAtLeast polls the scrape until every series named reaches its floor,
// within the deadline every wait on the worker has.
func awaitAtLeast(ctx context.Context, t *testing.T, at suite, floors map[string]float64) {
	t.Helper()
	deadline := time.Now().Add(pollFor)
	for time.Now().Before(deadline) {
		if short := belowTheFloor(t, scrape(ctx, t, at), floors); short == "" {
			return
		}
		time.Sleep(pollEvery)
	}
	t.Fatalf("%s after %s, want it at its floor", belowTheFloor(t, scrape(ctx, t, at), floors), pollFor)
}

// belowTheFloor names the first series still under its floor, with its value,
// or nothing when every one reached it.
func belowTheFloor(t *testing.T, scraped suiteenv.Scrape, floors map[string]float64) string {
	t.Helper()
	for series, floor := range floors {
		name, labels := parseSeries(t, series)
		if got, _ := scraped.Value(name, labels); got < floor {
			return series + " = " + strconv.FormatFloat(got, 'f', -1, 64)
		}
	}
	return ""
}

// parseSeries reads name{label="value",...} the way a case writes it, so an
// assertion reads like the series it is about.
func parseSeries(t *testing.T, series string) (string, map[string]string) {
	t.Helper()
	open := indexOf(series, '{')
	if open < 0 {
		return series, nil
	}
	labels := map[string]string{}
	for _, pair := range splitPairs(series[open+1 : len(series)-1]) {
		eq := indexOf(pair, '=')
		if eq < 0 {
			t.Fatalf("series %q is not name{label=\"value\"}", series)
		}
		labels[pair[:eq]] = pair[eq+2 : len(pair)-1]
	}
	return series[:open], labels
}

func indexOf(text string, r rune) int {
	for at, each := range text {
		if each == r {
			return at
		}
	}
	return -1
}

func splitPairs(text string) []string {
	var out []string
	start := 0
	for at, r := range text {
		if r == ',' {
			out = append(out, text[start:at])
			start = at + 1
		}
	}
	return append(out, text[start:])
}
