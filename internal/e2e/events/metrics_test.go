//go:build integration

// The series the outbox moves: the two gauges the runner reads off the backlog
// once per turn, and the counters the relay tells its count about.
//
// The suite shares the database and other cases leave pending rows behind, so
// the gauges are read as differences over what was already pending; the
// counters are on a registry of the case and are exact.
package events

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/junglegaming/backend-challenge-go/internal/app/relayoutbox"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/platform/clock"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
	"github.com/junglegaming/backend-challenge-go/internal/platform/outboxrelay"
	"github.com/junglegaming/backend-challenge-go/internal/platform/postgres"
)

// Between the commit and the publication the rows are pending and the oldest
// has an age; after the relay publishes the wallet, the backlog falls by
// exactly the rows of that wallet.
func TestRelay_measuresThePendingRowsAndTheirAgeBetweenTheCommitAndThePublication(t *testing.T) {
	ctx, stack := stack(t)
	series := metrics.New(prometheus.NewRegistry())
	backlog := startMeasuring(ctx, t, stack, series)
	before := backlog.settled(t, series)

	owner := stack.openWallet(ctx, t)
	stack.bet(ctx, t, owner)
	written := float64(len(stack.rows(ctx, t, owner.WalletID)))
	if pending := backlog.settled(t, series); pending != before+written {
		t.Fatalf("pending rows after the commits = %v, want the %v of before plus the %v written", pending, before, written)
	}
	if age := testutil.ToFloat64(series.OutboxOldestAge); age <= 0 {
		t.Fatalf("age of the oldest pending row = %v, want it measured from a commit in the past", age)
	}

	stack.drain(ctx, t, stack.relay(t, publisher(ctx, t)), owner.WalletID)
	if after := backlog.settled(t, series); after != before {
		t.Fatalf("pending rows after the publication = %v, want the %v of before the commits", after, before)
	}
}

// The count is told every outcome of a send: a transient refusal is a retry, a
// permanent one is a refusal, and the tenth permanent one is a dead row — which
// leaves the pending set.
func TestRelay_countsTheRefusalsAndTheDeathOfARow(t *testing.T) {
	ctx, stack := stack(t)
	series := metrics.New(prometheus.NewRegistry())
	owner := stack.openWallet(ctx, t)
	refused := stack.rows(ctx, t, owner.WalletID)[0]

	transient := stack.countingRelay(t, &refusing{}, series)
	stack.attempt(ctx, t, transient, owner.WalletID, refused.EventID)
	if got := testutil.ToFloat64(series.Retries.WithLabelValues("outbox", "transient")); got != 1 {
		t.Fatalf("retries{outbox,transient} = %v, want 1", got)
	}

	backlog := startMeasuring(ctx, t, stack, series)
	beforeTheDeath := backlog.settled(t, series)
	permanent := stack.countingRelay(t, &refusing{permanent: true}, series)
	for range 10 {
		stack.attempt(ctx, t, permanent, owner.WalletID, refused.EventID)
	}
	if got := testutil.ToFloat64(series.OutboxDead); got != 1 {
		t.Fatalf("dead events = %v, want the 1 of the tenth refusal", got)
	}
	if got := testutil.ToFloat64(series.Retries.WithLabelValues("outbox", "refused")); got != 9 {
		t.Fatalf("retries{outbox,refused} = %v, want the 9 refusals before the death", got)
	}
	if after := backlog.settled(t, series); after != beforeTheDeath-1 {
		t.Fatalf("pending rows after the death = %v, want the dead row out of the %v", after, beforeTheDeath)
	}
}

// countingRelay builds one replica of the relay whose count moves the series
// of the case.
func (s *settlement) countingRelay(t *testing.T, sender relayoutbox.Publisher, series *metrics.Settlement) *relayoutbox.Service {
	t.Helper()
	return relayoutbox.New(s.queue, sender, noSpan, outboxrelay.Counting(series), clock.UTC{}, quiet(), lease)
}

// measuring is the queue as the runner of the case scans it: the real one,
// counting the turns that measured the backlog, so a case waits for a fresh
// measurement instead of reading a gauge nothing has moved yet.
type measuring struct {
	*postgres.OutboxQueue
	turns atomic.Int64
}

func (m *measuring) Backlog(ctx context.Context) (storage.Backlog, error) {
	measured, err := m.OutboxQueue.Backlog(ctx)
	if err == nil {
		m.turns.Add(1)
	}
	return measured, err
}

// settled waits for one measurement taken after it was called and answers
// the pending gauge as that turn left it.
func (m *measuring) settled(t *testing.T, series *metrics.Settlement) float64 {
	t.Helper()
	start := m.turns.Load()
	deadline := time.Now().Add(receiveWait)
	for time.Now().Before(deadline) {
		if m.turns.Load() > start {
			return testutil.ToFloat64(series.OutboxPending)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no measurement of the backlog in %s, want the runner measuring every turn", receiveWait)
	return 0
}

// startMeasuring starts the runner of the relay over a relayer that publishes
// nothing, so the only thing it does each turn is read the backlog into the
// gauges: the cases here publish through a relay of their own, and a runner
// that published would be a second replica taking their rows.
func startMeasuring(ctx context.Context, t *testing.T, stack *settlement, series *metrics.Settlement) *measuring {
	t.Helper()
	backlog := &measuring{OutboxQueue: stack.queue}
	runner := outboxrelay.New(backlog, idleRelayer{}, quiet(), series, 50*time.Millisecond)
	if err := runner.Start(ctx); err != nil {
		t.Fatalf("Start the measuring runner = %v, want nil", err)
	}
	t.Cleanup(func() {
		stopping, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = runner.Stop(stopping)
	})
	return backlog
}

// idleRelayer is a relayer that takes nothing: the runner over it only
// measures.
type idleRelayer struct{}

func (idleRelayer) Relay(context.Context, storage.OutboxCandidate) error { return nil }
