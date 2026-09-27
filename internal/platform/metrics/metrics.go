// Package metrics registers every business series the process exposes on
// /metrics, in one place, and hands the instruments to whoever moves them.
//
// One package rather than one registration per adapter because a series is
// moved from more than one place — the settlements by two borders and one
// worker — and a CounterVec registers once. It is also where the names are
// fixed: they are the contract between the process and the dashboard and the
// alert rules, and a name that exists here is a name a panel can query.
//
// No label carries an identity. Every value of every label comes from a set
// the code fixes: origin, component, kind, status, token of the catalog,
// reason, state of the pool. go-observability keeps the wallet and the
// provider out, and a label built from the traffic would open the cardinality
// to whoever sends the traffic.
package metrics

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// The origins an operation is decided from, and the origins a reconciliation
// verdict is produced from. They are label values, so they are short tokens.
const (
	OriginHTTP      = "http"
	OriginSQS       = "sqs"
	OriginReference = "reference"
	OriginWatch     = "watch"
)

// The components that send work back for another attempt.
const (
	ComponentHTTP      = "http"
	ComponentSQS       = "sqs"
	ComponentOutbox    = "outbox"
	ComponentReference = "reference"
)

// The reasons an arrival is a duplicate of something already decided.
const (
	ReasonReplay            = "replay"
	ReasonKeyConflict       = "key_conflict"
	ReasonExternalDuplicate = "external_duplicate"
	ReasonRedelivery        = "redelivery"
)

// The reasons work goes back for another attempt.
const (
	RetryVersionConflict  = "version_conflict"
	RetryOutcomeInFlight  = "outcome_in_flight"
	RetryRaceUnresolved   = "race_unresolved"
	RetryTransient        = "transient"
	RetryRefused          = "refused"
	RetryReferencePending = "reference_pending"
)

// The reasons a message is abandoned to the dead-letter queue, as the log and
// the series name them.
const (
	AbandonInvalidBody   = "invalid_body"
	AbandonRefusedSender = "refused_sender"
	AbandonBodyDiffers   = "body_differs"
	AbandonDeliveryLimit = "delivery_limit"
)

// settledAs pairs each origin with the kinds and statuses it records a
// settlement under: a border answers the wait it wrote, and the worker only ever
// closes one, which is also why it sees only the kinds that cite.
var settledAs = []struct {
	origin   string
	kinds    []wager.Kind
	statuses []wager.Status
}{
	{OriginHTTP, external, []wager.Status{wager.Processed, wager.Rejected}},
	{OriginHTTP, citing, []wager.Status{wager.PendingReference}},
	{OriginSQS, external, []wager.Status{wager.Processed, wager.Rejected}},
	{OriginSQS, citing, []wager.Status{wager.PendingReference}},
	{OriginReference, citing, []wager.Status{wager.Processed, wager.Rejected}},
}

// external are the kinds a provider sends. OPENING is internal, and settles
// through neither border nor the worker.
var external = []wager.Kind{wager.KindBet, wager.KindWin, wager.KindLoss, wager.KindRefund, wager.KindRollback}

// citing are the kinds that may cite another operation, the only ones that wait.
var citing = []wager.Kind{wager.KindWin, wager.KindRefund, wager.KindRollback}

// duplicatedBy is the reasons each origin counts a duplicate for. Only the queue
// tells a redelivery of the message from a replay of the operation.
var duplicatedBy = map[string][]string{
	OriginHTTP: {ReasonReplay, ReasonKeyConflict, ReasonExternalDuplicate},
	OriginSQS:  {ReasonReplay, ReasonKeyConflict, ReasonExternalDuplicate, ReasonRedelivery},
}

// retriedBy is the reasons each component sends work back for: the two borders
// answer whatever RetryReason reads off the chain, and the relay and the worker
// each have their own.
var retriedBy = map[string][]string{
	ComponentHTTP:      {RetryVersionConflict, RetryOutcomeInFlight, RetryRaceUnresolved, RetryTransient},
	ComponentSQS:       {RetryVersionConflict, RetryOutcomeInFlight, RetryRaceUnresolved, RetryTransient},
	ComponentOutbox:    {RetryTransient, RetryRefused},
	ComponentReference: {RetryReferencePending},
}

var abandonReasons = []string{AbandonInvalidBody, AbandonRefusedSender, AbandonBodyDiffers, AbandonDeliveryLimit}

// Settlement is every business series of the process. The zero value holds
// no instrument: New is the only constructor, and it registers what it builds.
type Settlement struct {
	Settlements *prometheus.CounterVec
	Rejections  *prometheus.CounterVec
	Duplicates  *prometheus.CounterVec
	Retries     *prometheus.CounterVec
	Abandoned   *prometheus.CounterVec

	IngressDepth           prometheus.Gauge
	DeadLetterDepth        prometheus.Gauge
	OutboxPending          prometheus.Gauge
	OutboxOldestAge        prometheus.Gauge
	OutboxDead             prometheus.Counter
	ReferenceWaitOldestAge prometheus.Gauge

	WalletsChecked         *prometheus.CounterVec
	Divergences            *prometheus.CounterVec
	ReconciliationFailures *prometheus.CounterVec
}

// New builds every instrument and registers it on the registry of the process.
// Registering twice on one registry panics, which is what the library does for
// a name registered twice: one process, one registry, one call.
func New(reg prometheus.Registerer) *Settlement {
	s := &Settlement{
		Settlements: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_settlements_total",
			Help: "Operations that reached a recorded outcome, by origin, kind and status.",
		}, []string{"origin", "kind", "status"}),
		Rejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_rejections_total",
			Help: "Operations recorded as REJECTED, by origin and failure code.",
		}, []string{"origin", "failure_code"}),
		Duplicates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_duplicates_total",
			Help: "Arrivals of an operation already decided, by origin and reason.",
		}, []string{"origin", "reason"}),
		Retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_retries_total",
			Help: "Work sent back for another attempt, by component and reason.",
		}, []string{"component", "reason"}),
		Abandoned: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_ingress_messages_abandoned_total",
			Help: "Messages copied to the dead-letter queue, by reason.",
		}, []string{"reason"}),
		IngressDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "wager_ingress_queue_depth",
			Help: "Messages waiting in the ingress queue.",
		}),
		DeadLetterDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "wager_ingress_dead_letter_depth",
			Help: "Messages waiting in the dead-letter queue.",
		}),
		OutboxPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "wager_outbox_pending_events",
			Help: "Outbox rows neither published nor dead.",
		}),
		OutboxOldestAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "wager_outbox_oldest_pending_age_seconds",
			Help: "Age of the oldest pending outbox row, by the clock of the database.",
		}),
		OutboxDead: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "wager_outbox_dead_events_total",
			Help: "Outbox rows given up on after repeated permanent refusals.",
		}),
		ReferenceWaitOldestAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "wager_reference_wait_oldest_age_seconds",
			Help: "Age of the oldest transaction waiting for the operation it cites.",
		}),
		WalletsChecked: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_reconciliation_wallets_checked_total",
			Help: "Reconciliation verdicts produced, by origin.",
		}, []string{"origin"}),
		Divergences: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_reconciliation_divergences_total",
			Help: "Divergences found by a reconciliation verdict, by origin and token.",
		}, []string{"origin", "divergence"}),
		ReconciliationFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_reconciliation_failures_total",
			Help: "Reconciliations that produced no verdict, by origin.",
		}, []string{"origin"}),
	}
	s.prime()
	reg.MustRegister(
		s.Settlements, s.Rejections, s.Duplicates, s.Retries, s.Abandoned,
		s.IngressDepth, s.DeadLetterDepth, s.OutboxPending, s.OutboxOldestAge, s.OutboxDead,
		s.ReferenceWaitOldestAge, s.WalletsChecked, s.Divergences, s.ReconciliationFailures,
	)
	return s
}

// prime creates at zero every series a panel or an alert rates, before any
// traffic moves them.
//
// rate() and increase() read the first sample of a series as its baseline, not
// as a rise, and a child is born at the count it reached by the first scrape: a
// burst between two scrapes, or the first operation of each kind on a replica
// that just came up, would never show. Every value of every label is a closed
// set, so the whole set is small and known here.
func (s *Settlement) prime() {
	s.primeSettlements()
	s.primeRejections()
	primeEach(s.Duplicates, duplicatedBy)
	primeEach(s.Retries, retriedBy)
	for _, reason := range abandonReasons {
		s.Abandoned.WithLabelValues(reason)
	}
	s.primeReconciliation()
}

func (s *Settlement) primeSettlements() {
	for _, set := range settledAs {
		for _, kind := range set.kinds {
			for _, status := range set.statuses {
				s.Settlements.WithLabelValues(set.origin, kind.String(), status.String())
			}
		}
	}
}

// primeRejections creates every token of the catalog for every origin, but the
// two conflicts of idempotency: those write no row, and DuplicateReason sends
// them to the series of duplicates.
func (s *Settlement) primeRejections() {
	for _, code := range wager.Catalog() {
		if _, duplicate := DuplicateReason(code); duplicate {
			continue
		}
		for _, origin := range []string{OriginHTTP, OriginSQS, OriginReference} {
			s.Rejections.WithLabelValues(origin, code.String())
		}
	}
}

// primeEach creates every pair of the table on a vector of two labels.
func primeEach(vec *prometheus.CounterVec, table map[string][]string) {
	for first, seconds := range table {
		for _, second := range seconds {
			vec.WithLabelValues(first, second)
		}
	}
}

// primeReconciliation creates the reconciliation series at zero before any
// verdict moves them. The divergence alert asks increase() over a window, so a
// divergence found once by the route would otherwise never fire it.
func (s *Settlement) primeReconciliation() {
	for _, origin := range []string{OriginHTTP, OriginWatch} {
		s.WalletsChecked.WithLabelValues(origin)
		s.ReconciliationFailures.WithLabelValues(origin)
		for _, token := range reconcilewallet.Vocabulary() {
			s.Divergences.WithLabelValues(origin, token.String())
		}
	}
}

// Settled counts one operation that reached a recorded outcome.
func (s *Settlement) Settled(origin string, kind wager.Kind, status wager.Status) {
	s.Settlements.WithLabelValues(origin, kind.String(), status.String()).Inc()
}

// Rejected counts one operation recorded as REJECTED with that token.
func (s *Settlement) Rejected(origin string, code wager.FailureCode) {
	s.Rejections.WithLabelValues(origin, code.String()).Inc()
}

// Duplicate counts one arrival of something already decided.
func (s *Settlement) Duplicate(origin, reason string) {
	s.Duplicates.WithLabelValues(origin, reason).Inc()
}

// Retry counts one piece of work sent back for another attempt.
func (s *Settlement) Retry(component, reason string) {
	s.Retries.WithLabelValues(component, reason).Inc()
}

// Checked counts one reconciliation verdict, divergent or not.
func (s *Settlement) Checked(origin string) {
	s.WalletsChecked.WithLabelValues(origin).Inc()
}

// Diverged counts one token of one verdict that found the ledger and the
// balance in disagreement.
func (s *Settlement) Diverged(origin string, token reconcilewallet.Divergence) {
	s.Divergences.WithLabelValues(origin, token.String()).Inc()
}

// ReconciliationFailed counts one reconciliation that produced no verdict: the
// read failed, or the sum or the difference fell outside what Money holds. A
// refusal is not one — the missing wallet and the malformed identity answer.
func (s *Settlement) ReconciliationFailed(origin string) {
	s.ReconciliationFailures.WithLabelValues(origin).Inc()
}

// RetryReason names why a failure sends the work back, off the chain and never
// off a status number: two classes share the number of a retryable answer.
//
// The write that missed the lock and the two guards of the submission are the
// three conditions the design names as impossible; everything else is the
// infrastructure that is expected to come back.
func RetryReason(err error) string {
	switch {
	case errors.Is(err, storage.ErrLostWrite):
		return RetryVersionConflict
	case errors.Is(err, submitwager.ErrOutcomeInFlight):
		return RetryOutcomeInFlight
	case errors.Is(err, submitwager.ErrRaceUnresolved):
		return RetryRaceUnresolved
	}
	return RetryTransient
}

// DuplicateReason answers the reason of a rejection that wrote no row because
// the operation was already decided, and reports whether the token is one of
// the two. Neither counts as a rejection: the catalog names them, and the
// series of duplicates is where they go.
func DuplicateReason(code wager.FailureCode) (string, bool) {
	switch code {
	case wager.IdempotencyConflict:
		return ReasonKeyConflict, true
	case wager.DuplicateExternalTransaction:
		return ReasonExternalDuplicate, true
	}
	return "", false
}
