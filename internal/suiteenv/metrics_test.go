package suiteenv

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

// exposition is one scrape as the process writes it: a counter with two
// samples told apart by their labels, and a gauge with no label at all.
const exposition = "# TYPE wager_settlements_total counter\n" +
	`wager_settlements_total{kind="BET",origin="http",status="PROCESSED"} 3` + "\n" +
	`wager_settlements_total{kind="BET",origin="sqs",status="PROCESSED"} 1` + "\n" +
	"# TYPE wager_outbox_pending_events gauge\n" +
	"wager_outbox_pending_events 2\n"

// scraped serves the exposition and answers what ScrapeMetrics read from it.
func scraped(t *testing.T) Scrape {
	t.Helper()
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprint(w, exposition)
	}))
	t.Cleanup(served.Close)
	scrape, err := ScrapeMetrics(context.Background(), served.URL)
	if err != nil {
		t.Fatalf("ScrapeMetrics of the exposition = %v, want nil", err)
	}
	return scrape
}

// One scrape of a process, as the Prometheus would read it: every family of
// the exposition is there, by its name.
func TestScrapeMetrics_readsEveryFamilyOfTheExpositionByName(t *testing.T) {
	scrape := scraped(t)
	for _, name := range []string{"wager_settlements_total", "wager_outbox_pending_events"} {
		if _, ok := scrape[name]; !ok {
			t.Errorf("family %s is absent from the scrape, want it read", name)
		}
	}
	if len(scrape) != 2 {
		t.Errorf("families read = %d, want the 2 of the exposition", len(scrape))
	}
}

// A process that is gone and one that answers anything but 200 are both a
// failure of the scrape. The one that is gone is a server already closed, and
// the read is bounded by its context: a port nothing listens on can take the
// whole connect timeout of the host to refuse.
func TestScrapeMetrics_answersTheFailureOfAProcessThatDoesNotAnswer(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	bounded, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := ScrapeMetrics(bounded, gone.URL); err == nil {
		t.Errorf("ScrapeMetrics of a process that is gone = nil, want an error")
	}
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(refusing.Close)
	if _, err := ScrapeMetrics(context.Background(), refusing.URL); err == nil {
		t.Errorf("ScrapeMetrics of a process answering 503 = nil, want an error")
	}
}

// The value of a sample is found by the name of its family and exactly its
// labels: a label set no sample carries exactly, and a family the scrape does
// not carry, both answer false.
func TestValue_answersTheSampleWhoseLabelsAreExactlyTheOnesAsked(t *testing.T) {
	scrape := scraped(t)
	cases := []struct {
		name    string
		family  string
		labels  map[string]string
		want    float64
		present bool
	}{
		{name: "a counter by all its labels", family: "wager_settlements_total", labels: map[string]string{"origin": "sqs", "kind": "BET", "status": "PROCESSED"}, want: 1, present: true},
		{name: "a gauge with no label", family: "wager_outbox_pending_events", want: 2, present: true},
		{name: "a subset of the labels", family: "wager_settlements_total", labels: map[string]string{"origin": "http"}},
		{name: "a family the scrape does not carry", family: "wager_rejections_total"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := scrape.Value(tc.family, tc.labels)
			if ok != tc.present || got != tc.want {
				t.Errorf("Value(%s, %v) = %v, %t, want %v, %t", tc.family, tc.labels, got, ok, tc.want, tc.present)
			}
		})
	}
}

// Labels walks every sample of a family in the order of the scrape, and a
// family the scrape does not carry has no sample to walk.
func TestLabels_answersEverySampleOfTheFamilyInOrder(t *testing.T) {
	scrape := scraped(t)
	cases := []struct {
		name   string
		family string
		want   []map[string]string
	}{
		{name: "two samples in the order of the scrape", family: "wager_settlements_total", want: []map[string]string{
			{"kind": "BET", "origin": "http", "status": "PROCESSED"},
			{"kind": "BET", "origin": "sqs", "status": "PROCESSED"},
		}},
		{name: "one sample with no label", family: "wager_outbox_pending_events", want: []map[string]string{nil}},
		{name: "a family the scrape does not carry", family: "wager_rejections_total"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scrape.Labels(tc.family)
			if !slices.EqualFunc(got, tc.want, maps.Equal) || (got == nil) != (tc.want == nil) {
				t.Errorf("Labels(%s) = %v, want %v", tc.family, got, tc.want)
			}
		})
	}
}
