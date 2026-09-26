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

	WalletsChecked *prometheus.CounterVec
	Divergences    *prometheus.CounterVec
}

// New builds every instrument and registers it on the registry of the process.
// Registering twice on one registry panics, which is what the library does for
// a name registered twice: one process, one registry, one call.
func New(reg prometheus.Registerer) *Settlement {
	s := &Settlement{
		Settlements: counterVec("wager_settlements_total",
			"Operations that reached a recorded outcome, by origin, kind and status.", "origin", "kind", "status"),
		Rejections: counterVec("wager_rejections_total",
			"Operations recorded as REJECTED, by origin and failure code.", "origin", "failure_code"),
		Duplicates: counterVec("wager_duplicates_total",
			"Arrivals of an operation already decided, by origin and reason.", "origin", "reason"),
		Retries: counterVec("wager_retries_total",
			"Work sent back for another attempt, by component and reason.", "component", "reason"),
		Abandoned: counterVec("wager_ingress_messages_abandoned_total",
			"Messages copied to the dead-letter queue, by reason.", "reason"),
		IngressDepth:           gauge("wager_ingress_queue_depth", "Messages waiting in the ingress queue."),
		DeadLetterDepth:        gauge("wager_ingress_dead_letter_depth", "Messages waiting in the dead-letter queue."),
		OutboxPending:          gauge("wager_outbox_pending_events", "Outbox rows neither published nor dead."),
		OutboxOldestAge:        gauge("wager_outbox_oldest_pending_age_seconds", "Age of the oldest pending outbox row, by the clock of the database."),
		OutboxDead:             counter("wager_outbox_dead_events_total", "Outbox rows given up on after repeated permanent refusals."),
		ReferenceWaitOldestAge: gauge("wager_reference_wait_oldest_age_seconds", "Age of the oldest transaction waiting for the operation it cites."),
		WalletsChecked: counterVec("wager_reconciliation_wallets_checked_total",
			"Reconciliation verdicts produced, by origin.", "origin"),
		Divergences: counterVec("wager_reconciliation_divergences_total",
			"Divergences found by a reconciliation verdict, by origin and token.", "origin", "divergence"),
	}
	s.primeReconciliation()
	reg.MustRegister(
		s.Settlements, s.Rejections, s.Duplicates, s.Retries, s.Abandoned,
		s.IngressDepth, s.DeadLetterDepth, s.OutboxPending, s.OutboxOldestAge, s.OutboxDead,
		s.ReferenceWaitOldestAge, s.WalletsChecked, s.Divergences,
	)
	return s
}

// primeReconciliation creates the reconciliation series at zero before any
// verdict moves them.
//
// The divergence alert asks increase() over a window, and increase() over a
// series that appears for the first time at 1 answers 0: the first sample is
// the baseline, not a rise. A divergence found once by the route would then
// never fire it. Both origins and every token of the vocabulary are closed
// sets, so the whole set is small and known here.
func (s *Settlement) primeReconciliation() {
	for _, origin := range []string{OriginHTTP, OriginWatch} {
		s.WalletsChecked.WithLabelValues(origin)
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

func counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
}

func counter(name, help string) prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
}

func gauge(name, help string) prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
}
