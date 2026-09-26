//go:build integration

// The series the queue consumer moves, read off /metrics of the process under
// test. The process consumes a pair of queues of its own, so what it counts is
// exactly what the case sent.
package ingress

import (
	"context"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// One message settles once; the same message delivered again is deduced by the
// inbox and counted as a redelivery, not as a second settlement.
func TestIngress_countsTheSettlementAndTheRedelivery(t *testing.T) {
	ctx, at := start(t)
	conn := connect(ctx, t)
	holder := openWallet(ctx, t, at)
	identity := suiteenv.NewID()
	body := holder.bet(identity, "25.00", nil)
	at.queues.send(ctx, t, mappedSender, holder.id, body)
	at.queues.awaitEmpty(ctx, t)
	awaitEvents(ctx, t, conn, holder.id, openingEvents+2)
	awaitSeries(ctx, t, at, "wager_settlements_total", map[string]string{"origin": "sqs", "kind": "BET", "status": "PROCESSED"}, 1)

	at.queues.send(ctx, t, mappedSender, holder.id, body)
	at.queues.awaitEmpty(ctx, t)
	awaitSeries(ctx, t, at, "wager_duplicates_total", map[string]string{"origin": "sqs", "reason": "redelivery"}, 1)
	if got := seriesOf(ctx, t, at, "wager_settlements_total", map[string]string{"origin": "sqs", "kind": "BET", "status": "PROCESSED"}); got != 1 {
		t.Fatalf("settlements{sqs,BET,PROCESSED} after the redelivery = %v, want still 1", got)
	}
}

// The depth of the ingress queue is read once per turn beside the depth of the
// dead-letter queue, and the consumer sets the two only together. Both gauges
// start at zero, so a case that read two zeros would prove nothing: this one
// abandons a message, and the dead-letter gauge reaching one is a turn that
// measured — the same turn that read the ingress queue empty.
func TestIngress_readsTheDepthOfBothQueuesEveryTurn(t *testing.T) {
	ctx, at := start(t)
	holder := openWallet(ctx, t, at)
	at.queues.send(ctx, t, mappedSender, holder.id, holder.bet(suiteenv.NewID(), "25.001", nil))
	at.queues.awaitDeadLetter(ctx, t)
	at.queues.awaitEmpty(ctx, t)
	until(t, "the consumer to read the depth of both queues in one turn", func() bool {
		scraped := scrapeOf(ctx, t, at)
		ingress, _ := scraped.Value("wager_ingress_queue_depth", nil)
		dead, _ := scraped.Value("wager_ingress_dead_letter_depth", nil)
		return dead == 1 && ingress == 0
	})
}

func scrapeOf(ctx context.Context, t *testing.T, at suite) suiteenv.Scrape {
	t.Helper()
	scraped, err := suiteenv.ScrapeMetrics(ctx, at.base)
	if err != nil {
		t.Fatalf("scrape the process = %v, want nil", err)
	}
	return scraped
}

func seriesOf(ctx context.Context, t *testing.T, at suite, name string, labels map[string]string) float64 {
	t.Helper()
	value, _ := scrapeOf(ctx, t, at).Value(name, labels)
	return value
}

// awaitSeries polls the series until it reaches the floor: the consumer counts
// after the commit, and the queue reads empty a moment before that.
func awaitSeries(ctx context.Context, t *testing.T, at suite, name string, labels map[string]string, floor float64) {
	t.Helper()
	until(t, name+" to reach its floor", func() bool {
		return seriesOf(ctx, t, at, name, labels) >= floor
	})
}
