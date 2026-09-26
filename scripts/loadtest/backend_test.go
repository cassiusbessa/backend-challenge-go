package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// backendAnswers is a metric backend by the start of the expression: what the
// instant read at the start answers, and what the read over the window does.
type backendAnswers struct {
	atStart map[string][]string
	window  map[string][]string
	pending int
}

func (a backendAnswers) answer(query string, _ time.Time) (int, string) {
	if strings.HasPrefix(query, "max(wager_outbox_pending_events") {
		return vectorOf(sampleOf(a.pending))
	}
	found := a.atStart
	if strings.HasPrefix(query, "max_over_time(") || strings.HasPrefix(query, "max(max_over_time(") {
		found = a.window
	}
	for prefix, series := range found {
		if strings.Contains(query, prefix+"{") {
			return vectorOf(series...)
		}
	}
	return vectorOf()
}

// healthyOn answers a backend where every replica of the list decided arrivals
// and nothing is wrong, labelled the way the target labels its replicas.
func healthyOn(label string, replicas ...string) backendAnswers {
	var settled []string
	for _, replica := range replicas {
		settled = append(settled, sampleOf(40, label, replica, "kind", "BET", "status", "PROCESSED"))
	}
	return backendAnswers{
		atStart: map[string][]string{
			"wager_db_pool_empty_acquires_total": {sampleOf(2, label, replicas[0])},
		},
		window: map[string][]string{
			"wager_settlements_total":                 settled,
			"wager_duplicates_total":                  {sampleOf(5, label, replicas[0], "reason", "replay")},
			"wager_db_pool_empty_acquires_total":      {sampleOf(7, label, replicas[0])},
			"wager_outbox_oldest_pending_age_seconds": {sampleOf(3)},
		},
	}
}

// serverSideOf runs the reading of the server side against the answers given,
// for a target and a count of replicas.
func serverSideOf(t *testing.T, name string, replicas int, answers backendAnswers) (report, []string) {
	t.Helper()
	fake, server := newFakeService(t)
	fake.metrics = answers.answer
	o := optionsFor(t, server)
	o.target, o.replicaLabel, o.replicas = name, targets[name].replica, replicas
	var published report
	failures := serverSide(context.Background(), o, &published, longAgo.Add(-time.Minute), longAgo)
	return published, failures
}

// Over a healthy backend each target reads its replicas by its own label, and
// the figures are the rise over the window: a series absent at the start rose
// from zero, and one present rose by the difference.
func TestServerSide_readsTheReplicasOfEachTargetByItsLabel(t *testing.T) {
	t.Parallel()
	for name, label := range map[string]string{"compose": "instance", "cluster": "pod"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			published, failures := serverSideOf(t, name, 3, healthyOn(label, "r-1", "r-2", "r-3"))
			if len(failures) != 0 {
				t.Fatalf("failures over a healthy backend = %v, want none", failures)
			}
			want := map[string]float64{"r-1": 45, "r-2": 40, "r-3": 40}
			if listed(published.DecidedByReplica) != listed(want) {
				t.Fatalf("decided by replica = %s, want %s", listed(published.DecidedByReplica), listed(want))
			}
			if published.PoolEmptyAcquires != 5 || published.VersionConflicts != 0 || published.OutboxOldestPendingSeconds != 3 {
				t.Fatalf("figures = %.0f empty acquires, %.0f conflicts, %.0fs oldest; want 5, 0 and 3s", published.PoolEmptyAcquires, published.VersionConflicts, published.OutboxOldestPendingSeconds)
			}
		})
	}
}

// One conflict is a defect under the lock, and the child that counts it is
// born at 1 inside the window: it is read as a rise of one, not as nothing.
func TestServerSide_failsOnTheFirstVersionConflict(t *testing.T) {
	t.Parallel()
	answers := healthyOn("instance", "r-1")
	answers.window["wager_retries_total"] = []string{sampleOf(1, "instance", "r-1", "component", "http", "reason", "version_conflict")}
	published, failures := serverSideOf(t, "compose", 1, answers)
	if published.VersionConflicts != 1 || len(failures) != 1 || !strings.Contains(failures[0], "version conflicts = 1, want 0") {
		t.Fatalf("conflicts %.0f with failures %v, want 1 and the run failed on it", published.VersionConflicts, failures)
	}
}

// Fewer replicas deciding arrivals than asked for fails the run, naming them.
func TestServerSide_failsWhenARequestedReplicaDecidedNothing(t *testing.T) {
	t.Parallel()
	_, failures := serverSideOf(t, "cluster", 3, healthyOn("pod", "wager-a", "wager-b"))
	if len(failures) != 1 || !strings.Contains(failures[0], "replicas that decided arrivals = 2 (wager-a 45, wager-b 40), want at least 3") {
		t.Fatalf("failures with two of three replicas = %v, want the count named", failures)
	}
}

// A series every replica exposes from its start, absent from the backend, is a
// failure naming it, and not a zero.
func TestServerSide_failsOnASeriesTheBackendDoesNotHold(t *testing.T) {
	t.Parallel()
	answers := healthyOn("instance", "r-1")
	delete(answers.window, "wager_db_pool_empty_acquires_total")
	_, failures := serverSideOf(t, "compose", 1, answers)
	if len(failures) != 1 || !strings.Contains(failures[0], `holds no wager_db_pool_empty_acquires_total{job="wager",pod=""}`) {
		t.Fatalf("failures without the pool series = %v, want the series named", failures)
	}
}

// An outbox that still holds events past the bound fails the run.
func TestServerSide_failsWhenTheOutboxDoesNotDrain(t *testing.T) {
	t.Parallel()
	answers := healthyOn("instance", "r-1")
	answers.pending = 4
	_, failures := serverSideOf(t, "compose", 1, answers)
	if len(failures) != 1 || !strings.Contains(failures[0], "still reads 4 pending events") {
		t.Fatalf("failures with an outbox that never drains = %v, want it named", failures)
	}
}

// A backend that is not there fails the run naming it.
func TestServerSide_failsNamingABackendItCannotReach(t *testing.T) {
	t.Parallel()
	closed := httptest.NewServer(http.NotFoundHandler())
	address := closed.URL
	closed.Close()
	_, server := newFakeService(t)
	o := optionsFor(t, server)
	o.prometheus = address
	failures := serverSide(context.Background(), o, &report{}, longAgo.Add(-time.Minute), longAgo)
	if len(failures) != 1 || !strings.Contains(failures[0], "metric backend "+address+" is unreachable") {
		t.Fatalf("failures with the backend down = %v, want it named", failures)
	}
}

// A query the backend refuses is a failure naming the query, not an empty
// answer read as zero.
func TestQuery_failsOnAQueryTheBackendRefuses(t *testing.T) {
	t.Parallel()
	fake, server := newFakeService(t)
	fake.metrics = func(string, time.Time) (int, string) {
		return http.StatusBadRequest, `{"status":"error","error":"parse error"}`
	}
	o := optionsFor(t, server)
	if _, err := newBackend(o).query(context.Background(), "max(x)", longAgo); err == nil || !strings.Contains(err.Error(), "refused max(x): 400 parse error") {
		t.Fatalf("query refused by the backend = %v, want the query and the refusal named", err)
	}
	fake.metrics = func(string, time.Time) (int, string) { return vectorOf(`{"metric":{},"value":[1,"NaN?"]}`) }
	if _, err := newBackend(o).query(context.Background(), "max(x)", longAgo); err == nil {
		t.Fatalf("query answered with a value that is not a number = nil, want the failure")
	}
}

// The outbox drains on the first read that finds nothing pending, and the time
// is counted from the end of the window.
func TestDrain_answersTheTimeFromTheEndOfTheWindow(t *testing.T) {
	t.Parallel()
	fake, server := newFakeService(t)
	fake.metrics = backendAnswers{}.answer
	took, err := newBackend(optionsFor(t, server)).drain(context.Background(), longAgo, time.Second)
	if err != nil || took <= 0 {
		t.Fatalf("drain of an empty outbox = %s, %v, want the time since the window", took, err)
	}
}

// The selector of a series scopes it to the replicas of the target.
func TestSeries_scopesTheSelectorToTheTarget(t *testing.T) {
	t.Parallel()
	source := backend{selector: `job="wager",pod!=""`}
	if got := source.series("wager_retries_total", `reason="version_conflict"`); got != `wager_retries_total{job="wager",pod!="",reason="version_conflict"}` {
		t.Fatalf("series with a matcher = %s, want the target selector and the matcher", got)
	}
	if got := source.series("wager_outbox_pending_events", ""); got != `wager_outbox_pending_events{job="wager",pod!=""}` {
		t.Fatalf("series without a matcher = %s, want the target selector alone", got)
	}
}
