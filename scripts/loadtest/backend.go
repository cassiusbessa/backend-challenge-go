package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// backend reads the figures of the server side from the metric backend of the
// Compose, which holds the series of every replica of either target.
type backend struct {
	address  string
	selector string
	client   *http.Client
}

// sample is one series of an instant vector.
type sample struct {
	labels map[string]string
	value  float64
}

// serverFigures is what the replicas themselves counted over the window.
type serverFigures struct {
	conflicts     float64
	emptyAcquires float64
	oldestPending float64
	byReplica     map[string]float64
}

func newBackend(o options) backend {
	return backend{address: strings.TrimRight(o.prometheus, "/"), selector: targets[o.target].selector, client: &http.Client{Timeout: requestTimeout}}
}

// query answers the instant vector of the expression at that time. A backend
// that cannot be reached, or that refuses the query, is a failure naming it:
// no figure is published as zero because nobody could read it.
func (b backend) query(ctx context.Context, expr string, at time.Time) ([]sample, error) {
	endpoint := b.address + "/api/v1/query?" + url.Values{
		"query": {expr},
		"time":  {strconv.FormatFloat(float64(at.UnixMilli())/1000, 'f', 3, 64)},
	}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil) //nolint:gosec // the operator names the backend
	if err != nil {
		return nil, fmt.Errorf("build the query of the metric backend %s: %w", b.address, err)
	}
	response, err := b.client.Do(request) //nolint:gosec // the operator names the backend
	if err != nil {
		return nil, fmt.Errorf("metric backend %s is unreachable: %w", b.address, err)
	}
	defer response.Body.Close()
	var answered struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answered); err != nil || answered.Status != "success" {
		return nil, fmt.Errorf("metric backend %s refused %s: %d %s", b.address, expr, response.StatusCode, answered.Error)
	}
	out := make([]sample, 0, len(answered.Data.Result))
	for _, each := range answered.Data.Result {
		text, _ := each.Value[1].(string)
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("metric backend %s answered %q for %s: %w", b.address, text, expr, err)
		}
		delete(each.Metric, "__name__")
		out = append(out, sample{labels: each.Metric, value: value})
	}
	return out, nil
}

// rise answers how much each series of a counter rose over the window: its
// highest value in the window less its value at the start.
//
// Not increase(): a child of a vector appears the first time it is moved, so a
// series born at 1 inside the window has no earlier sample, and increase()
// reads its first conflict as no rise at all. A series absent at the start rose
// from zero, and one whose replica died keeps its last value in the window.
func (b backend) rise(ctx context.Context, selector string, start, at time.Time) ([]sample, error) {
	before, err := b.query(ctx, selector, start)
	if err != nil {
		return nil, err
	}
	during, err := b.query(ctx, fmt.Sprintf("max_over_time(%s[%ds])", selector, windowSeconds(start, at)), at)
	if err != nil {
		return nil, err
	}
	base := map[string]float64{}
	for _, each := range before {
		base[keyOf(each.labels)] = each.value
	}
	for i := range during {
		during[i].value -= base[keyOf(during[i].labels)]
	}
	return during, nil
}

// figures reads what the replicas of the target counted over the window. The
// conflicts may be absent — the child only exists once moved — but the other
// series exist from the start of every replica, and their absence means the
// backend holds nothing of this target.
func (b backend) figures(ctx context.Context, label string, start, at time.Time) (serverFigures, error) {
	conflicts, err := b.rise(ctx, b.series("wager_retries_total", `reason="version_conflict"`), start, at)
	if err != nil {
		return serverFigures{}, err
	}
	acquires, err := b.required("wager_db_pool_empty_acquires_total")(b.rise(ctx, b.series("wager_db_pool_empty_acquires_total", ""), start, at))
	if err != nil {
		return serverFigures{}, err
	}
	settled, err := b.required("wager_settlements_total")(b.rise(ctx, b.series("wager_settlements_total", `origin="http"`), start, at))
	if err != nil {
		return serverFigures{}, err
	}
	duplicates, err := b.rise(ctx, b.series("wager_duplicates_total", `origin="http"`), start, at)
	if err != nil {
		return serverFigures{}, err
	}
	oldest, err := b.required("wager_outbox_oldest_pending_age_seconds")(b.query(ctx, fmt.Sprintf("max(max_over_time(%s[%ds]))", b.series("wager_outbox_oldest_pending_age_seconds", ""), windowSeconds(start, at)), at))
	if err != nil {
		return serverFigures{}, err
	}
	return serverFigures{
		conflicts:     total(conflicts),
		emptyAcquires: total(acquires),
		oldestPending: oldest[0].value,
		byReplica:     byLabel(label, append(settled, duplicates...)),
	}, nil
}

// required refuses an empty answer, which is a series the backend does not hold.
func (b backend) required(name string) func([]sample, error) ([]sample, error) {
	return func(found []sample, err error) ([]sample, error) {
		if err == nil && len(found) == 0 {
			return nil, fmt.Errorf("the metric backend %s holds no %s{%s}: are the replicas of this target scraped?", b.address, name, b.selector)
		}
		return found, err
	}
}

// drain waits for the outbox of every replica to read nothing pending, and
// answers how long after the window that was. The answer has the resolution of
// the scrape and, for the cluster, of the send of the agent.
func (b backend) drain(ctx context.Context, end time.Time, wait time.Duration) (time.Duration, error) {
	deadline := time.Now().Add(wait)
	for {
		pending, err := b.required("wager_outbox_pending_events")(b.query(ctx, "max("+b.series("wager_outbox_pending_events", "")+")", time.Now()))
		if err != nil {
			return 0, err
		}
		if pending[0].value == 0 {
			return time.Since(end), nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("the outbox still reads %.0f pending events %s after the window", pending[0].value, wait)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// series writes the selector of a series of the replicas of this target.
func (b backend) series(name, matcher string) string {
	if matcher == "" {
		return name + "{" + b.selector + "}"
	}
	return name + "{" + b.selector + "," + matcher + "}"
}

// windowSeconds is the range of a query that covers the window, rounded up.
func windowSeconds(start, at time.Time) int {
	return int(math.Ceil(at.Sub(start).Seconds()))
}

func keyOf(labels map[string]string) string {
	var key strings.Builder
	for _, name := range slices.Sorted(maps.Keys(labels)) {
		key.WriteString(name + "=" + labels[name] + ",")
	}
	return key.String()
}

func total(found []sample) float64 {
	var sum float64
	for _, each := range found {
		sum += each.value
	}
	return sum
}

// byLabel sums the rise of every series by the label that names the replica.
func byLabel(label string, found []sample) map[string]float64 {
	out := map[string]float64{}
	for _, each := range found {
		if replica := each.labels[label]; replica != "" {
			out[replica] += each.value
		}
	}
	return out
}
