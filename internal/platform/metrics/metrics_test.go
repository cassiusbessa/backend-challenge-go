package metrics

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// The names are the contract with the dashboard and the alert rules, so the
// gather of a fresh registry has to list each of them once and no other.
func TestNew_registersEverySeriesOfTheSpecificationOnce(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewPedanticRegistry()
	New(reg)
	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather of a fresh registry = %v, want nil", err)
	}
	var names []string
	for _, family := range gathered {
		names = append(names, family.GetName())
	}
	slices.Sort(names)
	if !slices.Equal(names, slices.Sorted(slices.Values(fixedNames()))) {
		t.Fatalf("families gathered = %v, want exactly %v", names, fixedNames())
	}
}

// fixedNames is what the specification fixes as always present after New: the
// gauges, the plain counters and the reconciliation series primed at zero. The
// vectors moved by the traffic only appear once a label set has been seen.
func fixedNames() []string {
	return []string{
		"wager_ingress_queue_depth",
		"wager_ingress_dead_letter_depth",
		"wager_outbox_pending_events",
		"wager_outbox_oldest_pending_age_seconds",
		"wager_outbox_dead_events_total",
		"wager_reference_wait_oldest_age_seconds",
		"wager_reconciliation_wallets_checked_total",
		"wager_reconciliation_divergences_total",
		"wager_reconciliation_failures_total",
	}
}

func TestNew_refusesASecondRegistrationOnTheSameRegistry(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	New(reg)
	if refused := panicOf(func() { New(reg) }); refused == nil {
		t.Fatalf("panic of a second New on one registry = %v, want the duplicate registration refused", refused)
	}
}

// panicOf runs the call and answers what it panicked with, or nil.
func panicOf(call func()) (recovered any) {
	defer func() { recovered = recover() }()
	call()
	return nil
}

// go-observability keeps every identity out of a label, and the identity of a
// message or an event is as much one as a wallet.
func TestNew_putsNoIdentityInALabel(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewPedanticRegistry()
	s := New(reg)
	moveEverything(s)
	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather after every series moved = %v, want nil", err)
	}
	for _, name := range labelNames(gathered) {
		if slices.Contains(forbiddenLabels, name) {
			t.Fatalf("a series carries the label %q, want no identity in a label", name)
		}
	}
}

var forbiddenLabels = []string{"walletId", "providerId", "transactionId", "messageId", "eventId"}

// labelNames answers every label name of every sample gathered.
func labelNames(gathered []*dto.MetricFamily) []string {
	var names []string
	for _, family := range gathered {
		for _, sample := range family.GetMetric() {
			for _, label := range sample.GetLabel() {
				names = append(names, label.GetName())
			}
		}
	}
	return names
}

// moveEverything touches one child of every vector, so the gather lists the
// labels of each family.
func moveEverything(s *Settlement) {
	s.Settled(OriginHTTP, wager.KindBet, wager.Processed)
	s.Rejected(OriginSQS, wager.InsufficientFunds)
	s.Duplicate(OriginHTTP, ReasonReplay)
	s.Retry(ComponentOutbox, RetryTransient)
	s.Abandoned.WithLabelValues("invalid_body").Inc()
	s.Checked(OriginWatch)
	s.Diverged(OriginWatch, reconcilewallet.BalanceMismatch)
	s.ReconciliationFailed(OriginHTTP)
}

// Each helper moves the child it names and no other, which is what lets a
// reporter be tested by the value of one series.
func TestSettled_movesTheChildOfTheOriginKindAndStatus(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	s.Settled(OriginHTTP, wager.KindBet, wager.Processed)
	s.Settled(OriginHTTP, wager.KindBet, wager.Processed)
	if got := testutil.ToFloat64(s.Settlements.WithLabelValues("http", "BET", "PROCESSED")); got != 2 {
		t.Fatalf("settlements{http,BET,PROCESSED} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(s.Settlements.WithLabelValues("sqs", "BET", "PROCESSED")); got != 0 {
		t.Fatalf("settlements{sqs,BET,PROCESSED} = %v, want 0: another origin", got)
	}
}

func TestRejected_movesTheChildOfTheOriginAndToken(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	s.Rejected(OriginReference, wager.ReferenceNotFound)
	if got := testutil.ToFloat64(s.Rejections.WithLabelValues("reference", "REFERENCE_NOT_FOUND")); got != 1 {
		t.Fatalf("rejections{reference,REFERENCE_NOT_FOUND} = %v, want 1", got)
	}
}

func TestDuplicate_movesTheChildOfTheOriginAndReason(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	s.Duplicate(OriginSQS, ReasonRedelivery)
	if got := testutil.ToFloat64(s.Duplicates.WithLabelValues("sqs", "redelivery")); got != 1 {
		t.Fatalf("duplicates{sqs,redelivery} = %v, want 1", got)
	}
}

func TestRetry_movesTheChildOfTheComponentAndReason(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	s.Retry(ComponentReference, RetryReferencePending)
	if got := testutil.ToFloat64(s.Retries.WithLabelValues("reference", "reference_pending")); got != 1 {
		t.Fatalf("retries{reference,reference_pending} = %v, want 1", got)
	}
}

// The reconciliation series exist at zero before any verdict: increase() over
// a series that first appears at 1 answers 0, and the divergence alert would
// miss the first divergence the route found.
func TestNew_primesTheReconciliationSeriesAtZeroForEveryOriginAndToken(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	if got := testutil.CollectAndCount(s.Divergences); got != 2*len(reconcilewallet.Vocabulary()) {
		t.Fatalf("divergence series primed = %d, want one per origin and token", got)
	}
	if got := testutil.CollectAndCount(s.WalletsChecked); got != 2 {
		t.Fatalf("wallets checked series primed = %d, want one per origin", got)
	}
	if got := testutil.ToFloat64(s.Divergences.WithLabelValues("http", "CHAIN_BREAK")); got != 0 {
		t.Fatalf("divergences{http,CHAIN_BREAK} before any verdict = %v, want 0", got)
	}
	if got := testutil.CollectAndCount(s.ReconciliationFailures); got != 2 {
		t.Fatalf("reconciliation failure series primed = %d, want one per origin", got)
	}
	if got := testutil.ToFloat64(s.ReconciliationFailures.WithLabelValues("http")) + testutil.ToFloat64(s.ReconciliationFailures.WithLabelValues("watch")); got != 0 {
		t.Fatalf("reconciliation failures of both origins before any verdict = %v, want 0", got)
	}
}

func TestChecked_movesTheVerdictsOfTheOrigin(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	s.Checked(OriginHTTP)
	if got := testutil.ToFloat64(s.WalletsChecked.WithLabelValues("http")); got != 1 {
		t.Fatalf("wallets_checked{http} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(s.WalletsChecked.WithLabelValues("watch")); got != 0 {
		t.Fatalf("wallets_checked{watch} after a verdict of the route = %v, want 0", got)
	}
}

func TestReconciliationFailed_movesTheFailuresOfTheOrigin(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	s.ReconciliationFailed(OriginWatch)
	if got := testutil.ToFloat64(s.ReconciliationFailures.WithLabelValues("watch")); got != 1 {
		t.Fatalf("reconciliation_failures{watch} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(s.ReconciliationFailures.WithLabelValues("http")); got != 0 {
		t.Fatalf("reconciliation_failures{http} after a failure of the watcher = %v, want 0", got)
	}
}

func TestDiverged_movesTheChildOfTheOriginAndToken(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	s.Diverged(OriginHTTP, reconcilewallet.SequenceGap)
	if got := testutil.ToFloat64(s.Divergences.WithLabelValues("http", "SEQUENCE_GAP")); got != 1 {
		t.Fatalf("divergences{http,SEQUENCE_GAP} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(s.Divergences.WithLabelValues("watch", "SEQUENCE_GAP")); got != 0 {
		t.Fatalf("divergences{watch,SEQUENCE_GAP} = %v, want 0: another origin", got)
	}
}

// Priming creates every origin and every token at zero on the vectors it is
// given, and moves nothing: the series exist so the first rise is a rise.
func TestPrimeReconciliation_createsEveryOriginAndTokenAtZero(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	s.WalletsChecked.Reset()
	s.Divergences.Reset()
	s.ReconciliationFailures.Reset()
	s.primeReconciliation()
	if got := testutil.CollectAndCount(s.Divergences); got != 2*len(reconcilewallet.Vocabulary()) {
		t.Fatalf("divergence series after priming = %d, want one per origin and token", got)
	}
	if got := testutil.CollectAndCount(s.ReconciliationFailures); got != 2 {
		t.Fatalf("reconciliation failure series after priming = %d, want one per origin", got)
	}
	if got := testutil.ToFloat64(s.Divergences.WithLabelValues("watch", "BALANCE_MISMATCH")) + testutil.ToFloat64(s.WalletsChecked.WithLabelValues("http")); got != 0 {
		t.Fatalf("sum of the primed series = %v, want 0: priming moves nothing", got)
	}
}

// The reason is read off the chain and never off a status number: the three
// named conditions each have a reason of their own, and everything else is the
// infrastructure that is expected to come back.
func TestRetryReason_namesTheConditionOffTheChain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "a write that missed the lock", err: fmt.Errorf("submit wager: %w", storage.ErrLostWrite), want: RetryVersionConflict},
		{name: "an outcome still in flight", err: fmt.Errorf("submit wager: %w", submitwager.ErrOutcomeInFlight), want: RetryOutcomeInFlight},
		{name: "a race nobody resolved", err: fmt.Errorf("submit wager: %w", submitwager.ErrRaceUnresolved), want: RetryRaceUnresolved},
		{name: "a database that is out", err: fault.Wrap("acquire connection", errors.New("connection refused")), want: RetryTransient},
		{name: "a bare failure", err: errors.New("something else"), want: RetryTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RetryReason(tc.err); got != tc.want {
				t.Fatalf("RetryReason = %q, want %q", got, tc.want)
			}
		})
	}
}

// The two conflicts of idempotency are duplicates and not rejections: they
// write no row, and the series of duplicates is where the catalog sends them.
func TestDuplicateReason_namesOnlyTheTwoConflictsOfIdempotency(t *testing.T) {
	t.Parallel()
	reasons := map[wager.FailureCode]string{
		wager.IdempotencyConflict:          ReasonKeyConflict,
		wager.DuplicateExternalTransaction: ReasonExternalDuplicate,
	}
	for _, code := range wager.Catalog() {
		reason, duplicate := DuplicateReason(code)
		if want, conflict := reasons[code]; duplicate != conflict || reason != want {
			t.Fatalf("DuplicateReason of %s = %q, %t, want %q, %t", code, reason, duplicate, want, conflict)
		}
	}
}
