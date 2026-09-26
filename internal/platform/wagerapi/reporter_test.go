package wagerapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

func TestNewReporter_logsThroughTheHandlerItWasGiven(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	reporterWriting(&written).Settled(requestOf(t), settled(t, false))
	if written.Len() == 0 {
		t.Fatalf("log = %q, want the line of the settlement", written.String())
	}
}

// The log of a settlement is identifiers and the outcome. The amount and the
// balance the provider moved never reach a line.
func TestSettled_logsTheOutcomeWithNoAmountOrBalance(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	reporterWriting(&written).Settled(requestOf(t), settled(t, false))
	line := written.String()
	if !strings.Contains(line, `"status":"PROCESSED"`) || !strings.Contains(line, `"kind":"BET"`) {
		t.Fatalf("settlement line = %s, want the kind and the status of the outcome", line)
	}
	for _, banned := range []string{"25.00", observedBalance} {
		if strings.Contains(line, banned) {
			t.Fatalf("settlement line = %s, want it without %q", line, banned)
		}
	}
}

// A business rejection is an expected answer: it carries the token of the catalog
// and no stack, because nothing is broken.
func TestRefuse_logsTheTokenOfABusinessRejectionWithNoStack(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	rejected := fmt.Errorf("submit wager: %w", wager.NewRejection(wager.InsufficientFunds, nil))
	reporterWriting(&written).Refuse(httptest.NewRecorder(), requestOf(t), rejected)
	line := written.String()
	if !strings.Contains(line, `"failureCode":"INSUFFICIENT_FUNDS"`) {
		t.Fatalf("rejection line = %s, want the token of the catalog", line)
	}
	if strings.Contains(line, `"stack"`) {
		t.Fatalf("rejection line = %s, want no stack for a rule that answered", line)
	}
}

// An infrastructure failure is what should not happen: it carries the stack and
// never a token.
func TestRefuse_carriesTheStackOfAnInfrastructureFailureWithNoToken(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	broken := fault.Wrap("acquire connection", errors.New("connection refused"))
	reporterWriting(&written).Refuse(httptest.NewRecorder(), requestOf(t), broken)
	line := written.String()
	if !strings.Contains(line, `"stack"`) {
		t.Fatalf("failure line = %s, want the stack of the failure", line)
	}
	if strings.Contains(line, `"failureCode"`) {
		t.Fatalf("failure line = %s, want no token: no rule refused anything", line)
	}
}

func TestRefuse_logsNoAmountCredentialOrBody(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	request := requestOf(t)
	request.Header.Set("Authorization", "Bearer secret-access-token")
	request.Header.Set(idempotencyHeader, "key-1")
	reporterWriting(&written).Refuse(httptest.NewRecorder(), request, fmt.Errorf("submit wager: %w", storage.ErrLostWrite))
	line := written.String()
	for _, banned := range []string{"secret-access-token", "Bearer", "key-1", "25.00"} {
		if strings.Contains(line, banned) {
			t.Fatalf("refusal line = %s, want it without %q", line, banned)
		}
	}
}

// The class decides the span and the stack, and not the number: a retryable answer
// shares its number with an outage, so reading the number would mark the span of a
// request that only has to be sent again.
func TestRecord_carriesTheStackOnlyForABrokenClass(t *testing.T) {
	t.Parallel()
	broken := fault.Wrap("acquire connection", errors.New("connection refused"))
	for _, tc := range []struct {
		name   string
		class  problem.Class
		broken bool
	}{
		{name: "a business rejection", class: problem.BusinessRejection, broken: false},
		{name: "invalid input", class: problem.InvalidInput, broken: false},
		{name: "an absent credential", class: problem.Unauthenticated, broken: false},
		{name: "an identity without permission", class: problem.Unauthorized, broken: false},
		{name: "a retryable answer", class: problem.Retryable, broken: false},
		{name: "an outage", class: problem.Unavailable, broken: true},
		{name: "a defect", class: problem.Internal, broken: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marked, line := recorded(t, tc.class, broken)
			if got := strings.Contains(line, `"stack"`); got != tc.broken {
				t.Fatalf("stack present = %t, want %t for %s", got, tc.broken, tc.name)
			}
			if marked != tc.broken {
				t.Fatalf("span marked as an error = %t, want %t for %s", marked, tc.broken, tc.name)
			}
		})
	}
}

// recorded answers what record left behind for that class: whether the span was
// marked as an error, and the line that reached the handler.
//
// The span is read back from a recorder instead of from the call, because the
// class is what decides it and the number cannot: a retryable answer shares the
// number of an outage.
func recorded(t *testing.T, class problem.Class, err error) (bool, string) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() {
		if closed := provider.Shutdown(context.Background()); closed != nil {
			t.Fatalf("shutdown tracer = %v, want nil", closed)
		}
	})
	ctx, span := provider.Tracer("test").Start(context.Background(), "answer")
	var written bytes.Buffer
	reporterWriting(&written).record(requestOf(t).WithContext(ctx), identity.TransactionID{}, err, problem.Of(class))
	span.End()
	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want exactly 1", len(ended))
	}
	return ended[0].Status().Code == codes.Error, written.String()
}

// A defect never crossed an I/O boundary, so nothing captured its frames on the
// way up. This border is where it is first seen as a failure, and that is where
// the capture belongs.
func TestStackOf_capturesTheFramesOfAChainThatCarriesNone(t *testing.T) {
	t.Parallel()
	bare := errors.New("submitwager: kind is not settled by this use case")
	if frames := fault.Stack(bare); len(frames) > 0 {
		t.Fatalf("frames of a bare error = %d, want none before the border", len(frames))
	}
	if frames := stackOf(bare); len(frames) == 0 {
		t.Fatalf("frames captured at the border = %d, want the stack of the defect", len(frames))
	}
}

// One failure keeps one stack: a chain that already carries frames is not captured
// again here.
func TestStackOf_keepsTheStackTheChainAlreadyCarries(t *testing.T) {
	t.Parallel()
	wrapped := fault.Wrap("acquire connection", errors.New("connection refused"))
	carried, answered := fault.Stack(wrapped), stackOf(wrapped)
	if len(answered) != len(carried) {
		t.Fatalf("frames = %d, want the %d the chain already carried", len(answered), len(carried))
	}
}

// The line carries the status always, and the two optional attributes only when
// they hold something.
func TestRefused_leavesOutTheAttributesThatHoldNothing(t *testing.T) {
	t.Parallel()
	var bare bytes.Buffer
	reporterWriting(&bare).refused(requestOf(t), identity.TransactionID{}, problem.Details{Status: http.StatusBadRequest}, nil)
	if !strings.Contains(bare.String(), `"status":"400"`) {
		t.Fatalf("bare line = %s, want the status of the answer", bare.String())
	}
	for _, absent := range []string{`"failureCode"`, `"stack"`} {
		if strings.Contains(bare.String(), absent) {
			t.Fatalf("bare line = %s, want %s left out when it holds nothing", bare.String(), absent)
		}
	}
	var full bytes.Buffer
	reporterWriting(&full).refused(requestOf(t), identity.TransactionID{}, problem.Details{Status: 422, FailureCode: "INSUFFICIENT_FUNDS"}, []string{"frame"})
	for _, present := range []string{`"failureCode":"INSUFFICIENT_FUNDS"`, `"stack"`} {
		if !strings.Contains(full.String(), present) {
			t.Fatalf("full line = %s, want %s carried when it holds something", full.String(), present)
		}
	}
}

// reporterWriting logs through the same allow list the process uses, so what the
// test reads is what Loki would receive.
func reporterWriting(sink *bytes.Buffer) *Reporter {
	return NewReporter(slog.New(telemetry.Allow(slog.NewJSONHandler(sink, nil))), metrics.New(prometheus.NewRegistry()))
}

// reporterCounting builds a reporter over a registry of its own, so a case
// reads the series it moved and nothing another case moved.
func reporterCounting() (*Reporter, *metrics.Settlement) {
	series := metrics.New(prometheus.NewRegistry())
	return NewReporter(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), series), series
}

func requestOf(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(context.Background(), http.MethodPost, Route, strings.NewReader(submission(nil)))
}

// A rule that refused wrote a row of its own, and go-observability asks the
// rejection to log it: the token says which rule refused, and the identifier is
// what joins the line to the transaction the provider can read back.
func TestRejected_namesTheTransactionTheCommitWroteForTheRefusal(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	id := transactionOf(t)
	rejected := fmt.Errorf("submit wager: %w", wager.NewRejection(wager.InsufficientFunds, nil))
	reporterWriting(&written).Rejected(httptest.NewRecorder(), requestOf(t), rejectedRow(id), rejected)
	line := written.String()
	if !strings.Contains(line, `"transactionId":"`+id.String()+`"`) {
		t.Fatalf("rejected line = %s, want the transaction of the row it wrote", line)
	}
	if !strings.Contains(line, `"failureCode":"INSUFFICIENT_FUNDS"`) {
		t.Fatalf("rejected line = %s, want the token beside the transaction", line)
	}
}

// The two conflicts of idempotency refuse without writing a row, so the result
// they come back with names nothing and the attribute is left out. An empty one
// would read as a transaction whose value was lost.
func TestRejected_namesNoTransactionForARefusalThatWroteNoRow(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	conflict := fmt.Errorf("submit wager: %w", wager.NewRejection(wager.IdempotencyConflict, nil))
	reporterWriting(&written).Rejected(httptest.NewRecorder(), requestOf(t), submitwager.Result{}, conflict)
	line := written.String()
	if strings.Contains(line, `"transactionId"`) {
		t.Fatalf("conflict line = %s, want no transaction named: it wrote no row", line)
	}
	if !strings.Contains(line, `"failureCode":"IDEMPOTENCY_CONFLICT"`) {
		t.Fatalf("conflict line = %s, want the token of the conflict", line)
	}
}

// replayMarker is a recorded refusal answered again, as the use case marks it:
// the rejection stays reachable underneath, and the behaviour says it is a
// replay. It is a type of this file because the marker of the use case keeps
// its rejection private.
type replayMarker struct {
	error
}

func (m replayMarker) Unwrap() error { return m.error }

func (replayMarker) IdempotentReplay() bool { return true }

func replayedRefusal() error {
	return replayMarker{error: wager.NewRejection(wager.InsufficientFunds, nil)}
}

// rejectedRow is the result a rule that refused comes back with: the row it
// wrote, in REJECTED, with the kind of the operation.
func rejectedRow(id identity.TransactionID) submitwager.Result {
	return submitwager.Result{TransactionID: id, Kind: wager.KindBet, Status: wager.Rejected}
}

func TestSettled_countsTheOutcomeByKindAndStatus(t *testing.T) {
	t.Parallel()
	reporter, series := reporterCounting()
	reporter.Settled(requestOf(t), settled(t, false))
	if got := testutil.ToFloat64(series.Settlements.WithLabelValues("http", "BET", "PROCESSED")); got != 1 {
		t.Fatalf("settlements{http,BET,PROCESSED} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(series.Duplicates.WithLabelValues("http", "replay")); got != 0 {
		t.Fatalf("duplicates{http,replay} after a first outcome = %v, want 0", got)
	}
}

// A replay is not a new outcome: the series of settlements is what was decided,
// and what was answered again is a duplicate.
func TestSettled_countsAReplayAsADuplicateAndNotAsASettlement(t *testing.T) {
	t.Parallel()
	reporter, series := reporterCounting()
	reporter.Settled(requestOf(t), settled(t, true))
	if got := testutil.ToFloat64(series.Duplicates.WithLabelValues("http", "replay")); got != 1 {
		t.Fatalf("duplicates{http,replay} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(series.Settlements.WithLabelValues("http", "BET", "PROCESSED")); got != 0 {
		t.Fatalf("settlements{http,BET,PROCESSED} after a replay = %v, want 0", got)
	}
}

func TestRejected_countsTheRowAsASettlementThatEndedRejectedAndByItsToken(t *testing.T) {
	t.Parallel()
	reporter, series := reporterCounting()
	rejected := fmt.Errorf("submit wager: %w", wager.NewRejection(wager.InsufficientFunds, nil))
	reporter.Rejected(httptest.NewRecorder(), requestOf(t), rejectedRow(transactionOf(t)), rejected)
	if got := testutil.ToFloat64(series.Settlements.WithLabelValues("http", "BET", "REJECTED")); got != 1 {
		t.Fatalf("settlements{http,BET,REJECTED} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(series.Rejections.WithLabelValues("http", "INSUFFICIENT_FUNDS")); got != 1 {
		t.Fatalf("rejections{http,INSUFFICIENT_FUNDS} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(series.Retries.WithLabelValues("http", "transient")); got != 0 {
		t.Fatalf("retries{http,transient} after a business rejection = %v, want 0", got)
	}
}

// The recorded refusal answered again is a replay: nothing new was decided, so
// neither the settlement nor the rejection moves.
func TestRejected_countsAReplayedRefusalAsADuplicate(t *testing.T) {
	t.Parallel()
	reporter, series := reporterCounting()
	replayed := fmt.Errorf("submit wager: %w", replayedRefusal())
	reporter.Rejected(httptest.NewRecorder(), requestOf(t), rejectedRow(transactionOf(t)), replayed)
	if got := testutil.ToFloat64(series.Duplicates.WithLabelValues("http", "replay")); got != 1 {
		t.Fatalf("duplicates{http,replay} after a replayed refusal = %v, want 1", got)
	}
	if got := testutil.ToFloat64(series.Rejections.WithLabelValues("http", "INSUFFICIENT_FUNDS")); got != 0 {
		t.Fatalf("rejections{http,INSUFFICIENT_FUNDS} after a replay = %v, want 0", got)
	}
}

// A rule that refused without writing a row is answered and logged, and it is
// not a settlement: nothing was recorded to count.
func TestRejected_countsNoSettlementForARefusalThatWroteNoRow(t *testing.T) {
	t.Parallel()
	reporter, series := reporterCounting()
	absent := fmt.Errorf("submit wager: %w", wager.NewRejection(wager.WalletNotFound, nil))
	reporter.Rejected(httptest.NewRecorder(), requestOf(t), submitwager.Result{}, absent)
	if got := testutil.CollectAndCount(series.Settlements) + testutil.CollectAndCount(series.Rejections); got != 0 {
		t.Fatalf("series moved by a refusal without a row = %d, want none", got)
	}
}

// The two conflicts of idempotency wrote no row and are duplicates, each under
// its own reason; neither is a rejection.
func TestRefuse_countsTheTwoConflictsOfIdempotencyAsDuplicates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code   wager.FailureCode
		reason string
	}{
		{code: wager.IdempotencyConflict, reason: "key_conflict"},
		{code: wager.DuplicateExternalTransaction, reason: "external_duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.reason+" is counted", func(t *testing.T) {
			reporter, series := reporterCounting()
			reporter.Refuse(httptest.NewRecorder(), requestOf(t), fmt.Errorf("submit wager: %w", wager.NewRejection(tc.code, nil)))
			if got := testutil.ToFloat64(series.Duplicates.WithLabelValues("http", tc.reason)); got != 1 {
				t.Fatalf("duplicates{http,%s} = %v, want 1", tc.reason, got)
			}
			if got := testutil.CollectAndCount(series.Rejections); got != 0 {
				t.Fatalf("rejection series moved by %s = %d, want none", tc.code, got)
			}
		})
	}
}

// A retry is counted by the reason read off the chain, for both classes that
// share the number 503: the retryable answer and the outage.
func TestRefuse_countsARetryByTheReasonOffTheChain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		reason string
	}{
		{name: "a write that missed the lock", err: fmt.Errorf("submit wager: %w", storage.ErrLostWrite), reason: "version_conflict"},
		{name: "an outcome in flight", err: fmt.Errorf("submit wager: %w", submitwager.ErrOutcomeInFlight), reason: "outcome_in_flight"},
		{name: "a race nobody resolved", err: fmt.Errorf("submit wager: %w", submitwager.ErrRaceUnresolved), reason: "race_unresolved"},
		{name: "a database that is out", err: fault.Wrap("acquire connection", errors.New("connection refused")), reason: "transient"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reporter, series := reporterCounting()
			reporter.Refuse(httptest.NewRecorder(), requestOf(t), tc.err)
			if got := testutil.ToFloat64(series.Retries.WithLabelValues("http", tc.reason)); got != 1 {
				t.Fatalf("retries{http,%s} = %v, want 1", tc.reason, got)
			}
		})
	}
}

// What is neither a rule, a duplicate nor a retry moves nothing: invalid input
// and a defect are answered and logged, and no series is about them.
func TestRefuse_movesNoSeriesForInvalidInputOrADefect(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		fmt.Errorf("wagerapi: %w", problem.ErrInvalidInput),
		errors.New("submitwager: kind is not settled by this use case"),
	} {
		reporter, series := reporterCounting()
		reporter.Refuse(httptest.NewRecorder(), requestOf(t), err)
		moved := testutil.CollectAndCount(series.Retries) + testutil.CollectAndCount(series.Duplicates) +
			testutil.CollectAndCount(series.Settlements) + testutil.CollectAndCount(series.Rejections)
		if moved != 0 {
			t.Fatalf("series moved by %v = %d, want none", err, moved)
		}
	}
}

// countSettled tells the first outcome from the replay: the first is a
// settlement by kind and status, the replay a duplicate and nothing else.
func TestCountSettled_separatesTheFirstOutcomeFromTheReplay(t *testing.T) {
	t.Parallel()
	reporter, series := reporterCounting()
	reporter.countSettled(settled(t, false))
	reporter.countSettled(settled(t, true))
	if got := testutil.ToFloat64(series.Settlements.WithLabelValues("http", "BET", "PROCESSED")); got != 1 {
		t.Fatalf("settlements{http,BET,PROCESSED} of one outcome and one replay = %v, want 1", got)
	}
	if got := testutil.ToFloat64(series.Duplicates.WithLabelValues("http", "replay")); got != 1 {
		t.Fatalf("duplicates{http,replay} of one outcome and one replay = %v, want 1", got)
	}
}

// count reads the class and never the number: the two classes behind a 503
// retry, a replayed rule is a duplicate, and every other class moves nothing.
func TestCount_movesTheSeriesOfTheClassAndNoOther(t *testing.T) {
	t.Parallel()
	broken := fault.Wrap("acquire connection", errors.New("connection refused"))
	for _, class := range []problem.Class{problem.Retryable, problem.Unavailable} {
		reporter, series := reporterCounting()
		reporter.count(submitwager.Result{}, broken, problem.Of(class))
		if got := testutil.ToFloat64(series.Retries.WithLabelValues("http", "transient")); got != 1 {
			t.Fatalf("retries{http,transient} for class %d = %v, want 1", class, got)
		}
	}
	replayed, series := reporterCounting()
	details := problem.Of(problem.BusinessRejection)
	details.IdempotentReplay = true
	replayed.count(rejectedRow(transactionOf(t)), replayedRefusal(), details)
	if got := testutil.ToFloat64(series.Duplicates.WithLabelValues("http", "replay")); got != 1 {
		t.Fatalf("duplicates{http,replay} of a replayed rule = %v, want 1", got)
	}
	quiet, untouched := reporterCounting()
	quiet.count(submitwager.Result{}, broken, problem.Of(problem.InvalidInput))
	if got := testutil.CollectAndCount(untouched.Retries) + testutil.CollectAndCount(untouched.Duplicates); got != 0 {
		t.Fatalf("series moved by invalid input = %d, want none", got)
	}
}

// countRejection moves the two conflicts as duplicates, a rule with a row as a
// settlement that ended REJECTED and by its token, and nothing for a token it
// cannot read or a rule that wrote no row.
func TestCountRejection_movesTheSeriesTheTokenAndTheRowCallFor(t *testing.T) {
	t.Parallel()
	reporter, series := reporterCounting()
	reporter.countRejection(submitwager.Result{}, "IDEMPOTENCY_CONFLICT")
	reporter.countRejection(rejectedRow(transactionOf(t)), "INSUFFICIENT_FUNDS")
	reporter.countRejection(submitwager.Result{}, "WALLET_NOT_FOUND")
	reporter.countRejection(rejectedRow(transactionOf(t)), "NOT_A_TOKEN")
	if got := testutil.ToFloat64(series.Duplicates.WithLabelValues("http", "key_conflict")); got != 1 {
		t.Fatalf("duplicates{http,key_conflict} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(series.Rejections.WithLabelValues("http", "INSUFFICIENT_FUNDS")); got != 1 {
		t.Fatalf("rejections{http,INSUFFICIENT_FUNDS} = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(series.Rejections); got != 1 {
		t.Fatalf("rejection series = %d, want only the one of the rule that wrote a row", got)
	}
}
