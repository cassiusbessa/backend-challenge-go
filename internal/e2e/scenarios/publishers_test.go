//go:build integration

package scenarios

import (
	"context"
	"testing"
	"time"
)

// publishedWallets is how many wallets the publishers dispute: enough groups that
// every publisher has rows to claim at once, and each wallet leaves four events —
// the opening and the bet, each an outcome and a movement.
const (
	publishedWallets = 5
	eventsPerWallet  = 4
)

// The sixth scenario of the statement: several instances relay the same outbox at
// once. Each event leaves the processes once, counted on the wire by the proxy
// every publisher sends through — the FIFO topic deduplicates, so a subscriber
// would read a second send as one and hide exactly what this case looks for. The
// proxy also sees what the fleet publishes for other wallets of the database, and
// the case reads only the events of its own.
func TestPublishers_sendEachEventOnce(t *testing.T) {
	ctx, at := setUp(t)
	recording, endpoint := front(t, snsEndpoint(), Faculties{RecordPublishes: true})
	// The relay scans every 20ms instead of the 50ms of the other cases, so the
	// publishers contend for the same rows more often than they take turns.
	publishers := at.launch(ctx, t, at.params.Publishers, map[string]string{"SNS_ENDPOINT": endpoint, "OUTBOX_INTERVAL": "20ms"})
	db := connect(ctx, t)
	wallets := make([]owner, publishedWallets)
	for index := range wallets {
		wallets[index] = at.open(ctx, t, publishers.at(index), fundedBalance)
		assertCreated(ctx, t, at, publishers.at(index+1), wallets[index].bet(betAmount))
	}

	started := time.Now()
	recorded := awaitEveryEventPublished(ctx, t, db, wallets)
	if len(recorded) != publishedWallets*eventsPerWallet {
		t.Fatalf("events of the wallets = %d, want %d", len(recorded), publishedWallets*eventsPerWallet)
	}
	t.Logf("%d publishers published the %d events of %d wallets within %s", len(publishers), len(recorded), len(wallets), since(started))
	sent := recording.Published()
	for _, eventID := range recorded {
		if sent[eventID] != 1 {
			t.Errorf("event %s left the publishers %d times, want exactly once", eventID, sent[eventID])
		}
	}
	t.Logf("the proxy saw %d distinct events leave, %d of them of other wallets of the database; each event of the case once", len(sent), len(sent)-len(recorded))
}

// awaitEveryEventPublished waits until no wallet of the case has an event left to
// publish, and answers every event of them.
func awaitEveryEventPublished(ctx context.Context, t *testing.T, db store, wallets []owner) []string {
	t.Helper()
	until(ctx, t, "the publishers to publish every event of the wallets", func() bool {
		for _, each := range wallets {
			if len(db.pendingEvents(ctx, t, each.id)) > 0 {
				return false
			}
		}
		return true
	})
	var recorded []string
	for _, each := range wallets {
		recorded = append(recorded, db.events(ctx, t, each.id)...)
	}
	return recorded
}
