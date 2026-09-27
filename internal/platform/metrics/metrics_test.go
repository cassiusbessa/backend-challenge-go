package metrics

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics/metricstest"
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
// gauges, the plain counters, and every vector, primed at zero before any
// traffic moves it.
func fixedNames() []string {
	return []string{
		"wager_settlements_total",
		"wager_rejections_total",
		"wager_duplicates_total",
		"wager_retries_total",
		"wager_ingress_messages_abandoned_total",
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
	s.Abandoned.WithLabelValues(AbandonInvalidBody).Inc()
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

// Every series a panel rates exists at zero once New returns, so the first
// operation a replica decides is a rise and not the baseline of a new series.
func TestPrime_createsEverySeriesOfTheTrafficAtZero(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	for _, vec := range []*prometheus.CounterVec{s.Settlements, s.Rejections, s.Duplicates, s.Retries, s.Abandoned} {
		vec.Reset()
	}
	s.prime()
	primed := map[string]int{
		"settlements": testutil.CollectAndCount(s.Settlements),
		"rejections":  testutil.CollectAndCount(s.Rejections),
		"duplicates":  testutil.CollectAndCount(s.Duplicates),
		"retries":     testutil.CollectAndCount(s.Retries),
		"abandoned":   testutil.CollectAndCount(s.Abandoned),
	}
	want := map[string]int{"settlements": 32, "rejections": 3 * (len(wager.Catalog()) - 2), "duplicates": 7, "retries": 11, "abandoned": 4}
	for vector, count := range want {
		if primed[vector] != count {
			t.Errorf("%s primed = %d, want %d", vector, primed[vector], count)
		}
	}
	moved := metricstest.Sum(t, s.Settlements) + metricstest.Sum(t, s.Rejections) + metricstest.Sum(t, s.Duplicates) +
		metricstest.Sum(t, s.Retries) + metricstest.Sum(t, s.Abandoned)
	if moved != 0 {
		t.Fatalf("total of the primed series = %v, want 0: priming moves nothing", moved)
	}
}

// A border records every kind processed or rejected and only a citing kind
// waiting; the worker only closes the wait of a citing kind.
func TestPrimeSettlements_createsOnlyWhatEachOriginRecords(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	s.Settlements.Reset()
	s.primeSettlements()
	children := childrenOf(t, s.Settlements)
	for _, never := range []string{
		"kind=BET,origin=http,status=PENDING_REFERENCE",
		"kind=LOSS,origin=sqs,status=PENDING_REFERENCE",
		"kind=BET,origin=reference,status=PROCESSED",
		"kind=WIN,origin=reference,status=PENDING_REFERENCE",
		"kind=OPENING,origin=http,status=PROCESSED",
	} {
		if slices.Contains(children, never) {
			t.Errorf("settlement primed %s, want only what the origin records", never)
		}
	}
	for _, recorded := range []string{
		"kind=LOSS,origin=http,status=PROCESSED",
		"kind=ROLLBACK,origin=sqs,status=PENDING_REFERENCE",
		"kind=REFUND,origin=reference,status=REJECTED",
	} {
		if !slices.Contains(children, recorded) {
			t.Errorf("settlements primed %v, want %s among them", children, recorded)
		}
	}
}

// The two conflicts of idempotency write no row, and their series is the one
// of duplicates, so they are the only tokens of the catalog left out.
func TestPrimeRejections_leavesTheTwoConflictsToTheDuplicates(t *testing.T) {
	t.Parallel()
	s := New(prometheus.NewRegistry())
	s.Rejections.Reset()
	s.primeRejections()
	children := childrenOf(t, s.Rejections)
	if got, want := len(children), 3*(len(wager.Catalog())-2); got != want {
		t.Fatalf("rejection children = %d, want %d: three origins by every token but the conflicts", got, want)
	}
	for _, child := range children {
		if strings.Contains(child, "IDEMPOTENCY_CONFLICT") || strings.Contains(child, "DUPLICATE_EXTERNAL_TRANSACTION") {
			t.Fatalf("rejection child %s, want no conflict of idempotency", child)
		}
	}
	if !slices.Contains(children, "failure_code=REFERENCE_NOT_FOUND,origin=reference") {
		t.Fatalf("rejection children %v, want the deadline of the worker among them", children)
	}
}

func TestPrimeEach_createsEveryPairOfTheTableAndNoOther(t *testing.T) {
	t.Parallel()
	vec := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "paired_total", Help: "Paired."}, []string{"first", "second"})
	primeEach(vec, map[string][]string{"a": {"x", "y"}, "b": {"z"}})
	children := childrenOf(t, vec)
	slices.Sort(children)
	if want := []string{"first=a,second=x", "first=a,second=y", "first=b,second=z"}; !slices.Equal(children, want) {
		t.Fatalf("pairs primed = %v, want %v", children, want)
	}
}

// childrenOf answers each child of the collector as its label pairs, in the
// order of the label names.
func childrenOf(t *testing.T, c prometheus.Collector) []string {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(c)
	gathered, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather of one collector = %v, want nil", err)
	}
	var children []string
	for _, family := range gathered {
		for _, sample := range family.GetMetric() {
			var pairs []string
			for _, label := range sample.GetLabel() {
				pairs = append(pairs, label.GetName()+"="+label.GetValue())
			}
			children = append(children, strings.Join(pairs, ","))
		}
	}
	return children
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
