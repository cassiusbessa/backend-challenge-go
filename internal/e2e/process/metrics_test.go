//go:build integration

// The scrape of the process as the Prometheus reads it: every series the
// specification fixes is there by name, every label value comes from a closed
// set, no label carries an identity, and the divergence watcher is up and
// sweeping at the interval configured.
package process

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/platform/app"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// The series present on a fresh process: the gauges, the plain counters and
// the reconciliation series primed at zero. The vectors the traffic moves
// appear once a label set has been seen, and the suites of each border assert
// them by name after their own traffic.
var seriesAtRest = []string{
	"wager_ingress_queue_depth",
	"wager_ingress_dead_letter_depth",
	"wager_outbox_pending_events",
	"wager_outbox_oldest_pending_age_seconds",
	"wager_outbox_dead_events_total",
	"wager_reference_wait_oldest_age_seconds",
	"wager_reconciliation_wallets_checked_total",
	"wager_reconciliation_divergences_total",
	"wager_reconciliation_failures_total",
	"wager_db_pool_connections",
	"wager_db_pool_max_connections",
	"wager_db_pool_empty_acquires_total",
}

// The closed sets every label of a wager series draws its values from.
var allowedLabelValues = map[string][]string{
	"origin":       {"http", "sqs", "reference", "watch"},
	"component":    {"http", "sqs", "outbox", "reference"},
	"reason":       {"replay", "key_conflict", "external_duplicate", "redelivery", "version_conflict", "outcome_in_flight", "race_unresolved", "transient", "refused", "reference_pending", "invalid_body", "refused_sender", "body_differs", "delivery_limit"},
	"status":       {"PROCESSED", "REJECTED", "PENDING_REFERENCE"},
	"kind":         {"BET", "WIN", "LOSS", "REFUND", "ROLLBACK"},
	"failure_code": {"INSUFFICIENT_FUNDS", "REVERSAL_INSUFFICIENT_FUNDS", "REFERENCE_NOT_FOUND", "REFERENCE_NOT_PROCESSED", "REFERENCE_UNSUCCESSFUL", "ALREADY_REVERSED", "PLAYER_WALLET_MISMATCH", "CURRENCY_MISMATCH", "REVERSAL_AMOUNT_MISMATCH", "REFERENCE_MISMATCH", "WALLET_NOT_FOUND", "OPENING_NOT_ALLOWED", "AMOUNT_NOT_ALLOWED_FOR_KIND", "REFERENCE_REQUIRED"},
	"divergence":   {"BALANCE_MISMATCH", "SEQUENCE_GAP", "CHAIN_BREAK"},
	"state":        {"acquired", "idle", "constructing"},
}

var forbiddenLabels = []string{"walletId", "providerId", "transactionId", "messageId", "eventId"}

func TestMetrics_exposeEverySeriesWithClosedLabelsAndNoIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	base := startProcess(t, createQueue(ctx, t))
	scraped := scrapeOf(ctx, t, base)
	for _, name := range seriesAtRest {
		if _, ok := scraped[name]; !ok {
			t.Fatalf("series %s is absent from the scrape, want it there by name", name)
		}
	}
	assertEveryWagerLabelClosed(t, scraped)
	assertFailuresPrimedAtZero(t, scraped)
	if acquired, idle, ceiling := poolOf(t, scraped); acquired+idle > ceiling {
		t.Fatalf("pool = %v acquired and %v idle over a ceiling of %v, want the two under it", acquired, idle, ceiling)
	}
}

// assertFailuresPrimedAtZero reads the reconciliations that produced no verdict
// on a fresh process: both origins are there at zero, so the first failure of
// either reads as a rise and not as the first sample of a new series.
func assertFailuresPrimedAtZero(t *testing.T, scraped suiteenv.Scrape) {
	t.Helper()
	for _, origin := range []string{"http", "watch"} {
		failures, present := scraped.Value("wager_reconciliation_failures_total", map[string]string{"origin": origin})
		if !present || failures != 0 {
			t.Fatalf("reconciliation_failures{%s} on a fresh process = %v, present %t, want 0 and present", origin, failures, present)
		}
	}
}

func scrapeOf(ctx context.Context, t *testing.T, base string) suiteenv.Scrape {
	t.Helper()
	scraped, err := suiteenv.ScrapeMetrics(ctx, base)
	if err != nil {
		t.Fatalf("scrape the process = %v, want nil", err)
	}
	return scraped
}

// assertEveryWagerLabelClosed walks every sample of every series of the
// process and checks each label against the closed sets. The series of the
// runtime and of the HTTP latency are not the ones the specification fixes.
func assertEveryWagerLabelClosed(t *testing.T, scraped suiteenv.Scrape) {
	t.Helper()
	for name := range scraped {
		if !strings.HasPrefix(name, "wager_") {
			continue
		}
		for _, labels := range scraped.Labels(name) {
			assertClosedLabels(t, name, labels)
		}
	}
}

func assertClosedLabels(t *testing.T, name string, labels map[string]string) {
	t.Helper()
	for label, value := range labels {
		if slices.Contains(forbiddenLabels, label) {
			t.Fatalf("series %s carries the label %q, want no identity in a label", name, label)
		}
		allowed, closed := allowedLabelValues[label]
		if !closed {
			t.Fatalf("series %s carries the label %q, which no closed set fixes", name, label)
		}
		if !slices.Contains(allowed, value) {
			t.Fatalf("series %s carries %s=%q, want a value of the closed set %v", name, label, value, allowed)
		}
	}
}

func poolOf(t *testing.T, scraped suiteenv.Scrape) (acquired, idle, ceiling float64) {
	t.Helper()
	acquired, ok := scraped.Value("wager_db_pool_connections", map[string]string{"state": "acquired"})
	if !ok {
		t.Fatalf("pool connections acquired are absent from the scrape, want the series of the open pool")
	}
	idle, _ = scraped.Value("wager_db_pool_connections", map[string]string{"state": "idle"})
	ceiling, _ = scraped.Value("wager_db_pool_max_connections", nil)
	return acquired, idle, ceiling
}

// The watcher is up and sweeping at the interval configured: with a wallet in
// the table, the count of wallets checked rises within a few turns, and the
// process with the watcher in flight stops inside its budget.
func TestWatcher_sweepsAtTheConfiguredIntervalAndStopsInsideTheBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	insertWallet(ctx, t)
	application, base := startApplication(t, withWatcher(createQueue(ctx, t)))
	if checked := awaitChecked(ctx, t, base); checked < 1 {
		t.Fatalf("wallets_checked{watch} = %v after %s at a 50ms interval, want the watcher sweeping", checked, sweepWait)
	}
	// The budget is not measured here: each stop runs inside its own share, and a
	// share overrun is what makes Stop answer an error.
	stopping, release := context.WithTimeout(context.Background(), 20*time.Second)
	defer release()
	if err := application.Stop(stopping); err != nil {
		t.Fatalf("Stop with the watcher in flight = %v, want nil", err)
	}
}

// sweepWait bounds how long a case waits for the watcher to have checked one
// wallet: several turns at the shortened interval, with room for the page.
const sweepWait = 20 * time.Second

// awaitChecked polls the count of wallets the watcher checked until it moved,
// or the bound came, and answers the last value read.
func awaitChecked(ctx context.Context, t *testing.T, base string) float64 {
	t.Helper()
	deadline := time.Now().Add(sweepWait)
	var checked float64
	for time.Now().Before(deadline) && checked < 1 {
		checked, _ = scrapeOf(ctx, t, base).Value("wager_reconciliation_wallets_checked_total", map[string]string{"origin": "watch"})
		time.Sleep(100 * time.Millisecond)
	}
	return checked
}

// withWatcher is the environment of the process with the watcher sweeping far
// more often than production, so a case reads a verdict instead of waiting out
// the default.
func withWatcher(queueURL string) map[string]string {
	env := integrationEnv(queueURL, suiteenv.DatabaseURL())
	env["RECONCILIATION_INTERVAL"] = "50ms"
	env["RECONCILIATION_BATCH"] = "500"
	return env
}

// startApplication boots the process and hands it back for the case to stop
// itself, which is what a case about the shutdown needs.
func startApplication(t *testing.T, env map[string]string) (*fx.App, string) {
	t.Helper()
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	got := make(chan *httpapi.Server, 1)
	application := app.New(cfg, fx.Invoke(func(srv *httpapi.Server) { got <- srv }))
	startCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := application.Start(startCtx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = application.Stop(stopCtx)
	})
	return application, "http://" + (<-got).Addr()
}

// insertWallet writes one wallet the sweep has to find, through the role of
// the application, so the table is never empty for the case.
func insertWallet(ctx context.Context, t *testing.T) {
	t.Helper()
	conn, err := pgx.Connect(ctx, suiteenv.DatabaseURL())
	if err != nil {
		t.Fatalf("connect = %v, want nil: the suite needs the migration applied", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(ctx)) })
	const insert = `
INSERT INTO wallets (id, player_id, currency, balance_cents, version, created_at, updated_at)
VALUES ($1, $2, 'BRL', 0, 1, now(), now())`
	if _, err := conn.Exec(ctx, insert, suiteenv.NewID(), suiteenv.NewID()); err != nil {
		t.Fatalf("insert a wallet = %v, want nil", err)
	}
}
