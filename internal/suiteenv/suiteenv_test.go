package suiteenv

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOr_answersTheFallbackOnlyWhenNothingIsSet(t *testing.T) {
	t.Run("a variable that carries a value", func(t *testing.T) {
		t.Setenv("SUITEENV_PROBE", "from the environment")
		if got := Or("SUITEENV_PROBE", "the fallback"); got != "from the environment" {
			t.Errorf("value of a set variable = %q, want the one from the environment", got)
		}
	})

	t.Run("a variable that is set to nothing", func(t *testing.T) {
		t.Setenv("SUITEENV_PROBE", "")
		if got := Or("SUITEENV_PROBE", "the fallback"); got != "the fallback" {
			t.Errorf("value of an empty variable = %q, want the fallback", got)
		}
	})

	t.Run("a variable that is not set at all", func(t *testing.T) {
		if got := Or("SUITEENV_ABSENT", "the fallback"); got != "the fallback" {
			t.Errorf("value of an absent variable = %q, want the fallback", got)
		}
	})
}

func TestDatabaseURL_landsOnTheSuiteDatabaseWhenNothingSaysOtherwise(t *testing.T) {
	t.Run("nothing in the environment", func(t *testing.T) {
		t.Setenv(DatabaseURLKey, "")
		if got := DatabaseURL(); got != SuiteDatabaseURL {
			t.Errorf("database of a bare environment = %q, want the suite database", got)
		}
	})

	t.Run("an override in the environment", func(t *testing.T) {
		t.Setenv(DatabaseURLKey, "postgres://elsewhere")
		if got := DatabaseURL(); got != "postgres://elsewhere" {
			t.Errorf("database under an override = %q, want the override", got)
		}
	})
}

// One scrape of a process, as the Prometheus would read it: the value of a
// sample is found by the name of its family and exactly its labels.
func TestScrapeMetrics_answersTheValueOfOneSeriesByNameAndLabels(t *testing.T) {
	served := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprint(w, "# TYPE wager_settlements_total counter\n"+
			`wager_settlements_total{kind="BET",origin="http",status="PROCESSED"} 3`+"\n"+
			`wager_settlements_total{kind="BET",origin="sqs",status="PROCESSED"} 1`+"\n"+
			"# TYPE wager_outbox_pending_events gauge\n"+
			"wager_outbox_pending_events 2\n")
	}))
	t.Cleanup(served.Close)
	scraped, err := ScrapeMetrics(context.Background(), served.URL)
	if err != nil {
		t.Fatalf("ScrapeMetrics = %v, want nil", err)
	}
	assertValue(t, scraped, "wager_settlements_total", map[string]string{"origin": "http", "kind": "BET", "status": "PROCESSED"}, 3, true)
	assertValue(t, scraped, "wager_outbox_pending_events", nil, 2, true)
	// A label set no sample carries exactly, and a family the scrape does not
	// carry, both answer false.
	assertValue(t, scraped, "wager_settlements_total", map[string]string{"origin": "http"}, 0, false)
	assertValue(t, scraped, "wager_rejections_total", nil, 0, false)
	if got := scraped.Labels("wager_settlements_total"); len(got) != 2 || got[1]["origin"] != "sqs" {
		t.Errorf("labels of the settlements = %v, want the two samples in order", got)
	}
}

func assertValue(t *testing.T, scraped Scrape, name string, labels map[string]string, want float64, present bool) {
	t.Helper()
	got, ok := scraped.Value(name, labels)
	if ok != present || got != want {
		t.Errorf("%s%v = %v, %t, want %v, %t", name, labels, got, ok, want, present)
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
