package wagerqueue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

// A rule refusing the operation leaves the span ok: go-observability reserves the
// error of a span for infrastructure, a version conflict and the dead-letter queue,
// and a rejection is none of the three.
func TestReceiving_leavesTheSpanOkForARuleThatRefusedTheOperation(t *testing.T) {
	t.Parallel()
	reporter, spans := reporterOver(t)
	_, closeSpan := reporter.Receiving(context.Background(), arrived(1), decodedOf(t))
	closeSpan(wager.NewRejection(wager.InsufficientFunds, nil))
	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("spans = %d, want 1", len(ended))
	}
	if got := ended[0].Status().Code; got != codes.Unset {
		t.Fatalf("span status = %v, want it unset", got)
	}
}

func TestReceiving_marksTheSpanOfAFailureOfInfrastructure(t *testing.T) {
	t.Parallel()
	reporter, spans := reporterOver(t)
	_, closeSpan := reporter.Receiving(context.Background(), arrived(1), decodedOf(t))
	closeSpan(fault.Wrap("acquire connection", errors.New("connection refused")))
	ended := spans.Ended()
	if got := ended[0].Status().Code; got != codes.Error {
		t.Fatalf("span status = %v, want an error", got)
	}
	if len(ended[0].Events()) == 0 {
		t.Fatalf("span events = 0, want the failure recorded on it")
	}
}

// A turn the shutdown cut is not a failure, and it is read off the error rather
// than off the context: once the deadline has cancelled the work, a context test
// would drop every failure that merely raced it.
func TestFailed_writesNoLineForATurnTheShutdownCut(t *testing.T) {
	t.Parallel()
	reporter, logs := loggingReporter(t)
	reporter.Failed(context.Background(), "settle a message of the ingress queue", context.Canceled)
	reporter.Failed(context.Background(), "settle a message of the ingress queue", nil)
	if logs.Len() != 0 {
		t.Fatalf("lines written = %q, want none", logs.String())
	}
}

// The frames come from the chain when it already carries them, so one failure keeps
// one stack rather than gaining a second capture at this border.
func TestFailed_keepsTheFramesTheChainAlreadyCarries(t *testing.T) {
	t.Parallel()
	reporter, logs := loggingReporter(t)
	captured := fault.Wrap("acquire connection", errors.New("connection refused"))
	reporter.Failed(context.Background(), "settle a message of the ingress queue", captured)
	line := lineWith(t, logs, "settle a message")
	frames, carried := line["stack"].([]any)
	if !carried || len(frames) == 0 {
		t.Fatalf("stack = %v, want the frames of the chain", line["stack"])
	}
	if len(frames) != len(fault.Stack(captured)) {
		t.Fatalf("frames = %d, want the %d of the chain", len(frames), len(fault.Stack(captured)))
	}
}

// A failure that crossed no boundary has no frames in its chain, and this border is
// where it is first seen as one.
func TestFailed_capturesTheFramesOfAFailureThatCarriesNone(t *testing.T) {
	t.Parallel()
	reporter, logs := loggingReporter(t)
	reporter.Failed(context.Background(), "release a message of the ingress queue", errors.New("connection reset"))
	line := lineWith(t, logs, "release a message")
	if frames, carried := line["stack"].([]any); !carried || len(frames) == 0 {
		t.Fatalf("stack = %v, want frames captured here", line["stack"])
	}
}

// A line outside any message carries no identity: the poll failing is not about an
// operation, and an empty attribute would read as one whose value was lost.
func TestFailed_namesNoMessageForALineOutsideAnyOperation(t *testing.T) {
	t.Parallel()
	reporter, logs := loggingReporter(t)
	reporter.Failed(context.Background(), "receive from the ingress queue", errors.New("connection refused"))
	line := lineWith(t, logs, "receive from the ingress queue")
	if _, named := line["messageId"]; named {
		t.Fatalf("line carries a messageId, want none: %v", line)
	}
}

// The correlation of the envelope is taken only when it is a short opaque token,
// the rule of the header over HTTP: anything else reaches the log and the outbox,
// whose text column refuses NUL, so the message correlates the operation instead.
func TestReceiving_takesTheCorrelationOnlyWhenItIsAShortOpaqueToken(t *testing.T) {
	t.Parallel()
	const identity = "message-of-the-envelope"
	cases := map[string]struct {
		carried string
		want    string
	}{
		"a short opaque token is kept":             {carried: "request-7.a_b:c", want: "request-7.a_b:c"},
		"no correlation falls back to the message": {carried: "", want: identity},
		"one past 64 characters falls back":        {carried: strings.Repeat("c", 65), want: identity},
		"one with a space falls back":              {carried: "request 7", want: identity},
		"one with a NUL falls back":                {carried: "request-\x00", want: identity},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			reporter, _ := loggingReporter(t)
			decoded := Message{MessageID: identity, CorrelationID: tc.carried}
			ctx, closeSpan := reporter.Receiving(context.Background(), arrived(1), decoded)
			defer closeSpan(nil)
			if got := telemetry.Correlation(ctx); got != tc.want {
				t.Fatalf("correlation of %q = %q, want %q", tc.carried, got, tc.want)
			}
		})
	}
}

// The identity of a body nobody could decode is the deduplication the broker
// registered, which go-sqs-ingress fixes as the identity of the envelope.
func TestIdentityOf_fallsBackToTheDeduplicationOfTheBroker(t *testing.T) {
	t.Parallel()
	if got := identityOf(arrived(1), Message{}); got != deduplicationID {
		t.Fatalf("identity of an undecoded body = %q, want %q", got, deduplicationID)
	}
	if got := identityOf(arrived(1), Message{MessageID: "message-of-the-envelope"}); got != "message-of-the-envelope" {
		t.Fatalf("identity = %q, want the one of the envelope", got)
	}
}

func decodedOf(t *testing.T) Message {
	t.Helper()
	decoded, err := Decode(message(nil))
	if err != nil {
		t.Fatalf("Decode = %v, want nil", err)
	}
	return decoded
}

// reporterOver builds a reporter whose spans are kept, for the cases about the
// span of one message.
func reporterOver(t *testing.T) (*Reporter, *tracetest.SpanRecorder) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test")
	return NewReporter(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), tracer, metrics.New(prometheus.NewRegistry())), spans
}

// loggingReporter builds a reporter whose lines are kept, for the cases about what
// is written.
func loggingReporter(t *testing.T) (*Reporter, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	reporter := NewReporter(slog.New(slog.NewJSONHandler(logs, nil)), quietTracer(), metrics.New(prometheus.NewRegistry()))
	return reporter, logs
}

// countingReporter builds a reporter over a registry of its own, so a case
// reads the series it moved and nothing another case moved.
func countingReporter() (*Reporter, *metrics.Settlement) {
	series := metrics.New(prometheus.NewRegistry())
	return NewReporter(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), quietTracer(), series), series
}

func processedBet(t *testing.T) submitwager.Result {
	t.Helper()
	return submitwager.Result{TransactionID: transactionOf(t), Kind: wager.KindBet, Status: wager.Processed}
}

// refusalOf answers the rule as the consumer hands it to the reporter: the
// rejection reached with errors.As, not the error around it.
func refusalOf(t *testing.T, code wager.FailureCode) wager.Rejection {
	t.Helper()
	refusal, refused := rejectionOf(wager.NewRejection(code, nil))
	if !refused {
		t.Fatalf("rejectionOf = %t, want the rejection of %s", refused, code)
	}
	return refusal
}

func TestSettled_countsTheOutcomeUnderTheOriginOfTheQueue(t *testing.T) {
	t.Parallel()
	reporter, series := countingReporter()
	reporter.Settled(context.Background(), processedBet(t))
	if got := testutil.ToFloat64(series.Settlements.WithLabelValues("sqs", "BET", "PROCESSED")); got != 1 {
		t.Fatalf("settlements{sqs,BET,PROCESSED} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(series.Settlements.WithLabelValues("http", "BET", "PROCESSED")); got != 0 {
		t.Fatalf("settlements{http,BET,PROCESSED} after a message = %v, want 0", got)
	}
}

// A redelivery the inbox deduced and a resend under the same key are two
// reasons of one series, and neither is a settlement: nothing new was decided.
func TestSettled_countsARedeliveryAndAReplayAsDuplicatesUnderTheirOwnReasons(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result submitwager.Result
		reason string
	}{
		{name: "a redelivery the inbox deduced", result: submitwager.Result{Kind: wager.KindBet, Status: wager.Processed, IdempotentReplay: true, Redelivered: true}, reason: "redelivery"},
		{name: "a resend under the same key", result: submitwager.Result{Kind: wager.KindBet, Status: wager.Processed, IdempotentReplay: true}, reason: "replay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reporter, series := countingReporter()
			reporter.Settled(context.Background(), tc.result)
			if got := testutil.ToFloat64(series.Duplicates.WithLabelValues("sqs", tc.reason)); got != 1 {
				t.Fatalf("duplicates{sqs,%s} = %v, want 1", tc.reason, got)
			}
			if got := testutil.CollectAndCount(series.Settlements); got != 0 {
				t.Fatalf("settlement series moved by %s = %d, want none", tc.name, got)
			}
		})
	}
}

func TestRejected_countsTheRowAsASettlementThatEndedRejectedAndByItsToken(t *testing.T) {
	t.Parallel()
	reporter, series := countingReporter()
	row := submitwager.Result{TransactionID: transactionOf(t), Kind: wager.KindBet, Status: wager.Rejected}
	reporter.Rejected(context.Background(), row, refusalOf(t, wager.InsufficientFunds))
	if got := testutil.ToFloat64(series.Settlements.WithLabelValues("sqs", "BET", "REJECTED")); got != 1 {
		t.Fatalf("settlements{sqs,BET,REJECTED} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(series.Rejections.WithLabelValues("sqs", "INSUFFICIENT_FUNDS")); got != 1 {
		t.Fatalf("rejections{sqs,INSUFFICIENT_FUNDS} = %v, want 1", got)
	}
}

// The recorded refusal delivered again is a redelivery, not a second rejection.
func TestRejected_countsARedeliveredRefusalAsADuplicate(t *testing.T) {
	t.Parallel()
	reporter, series := countingReporter()
	again := submitwager.Result{TransactionID: transactionOf(t), Kind: wager.KindBet, Status: wager.Rejected, IdempotentReplay: true, Redelivered: true}
	reporter.Rejected(context.Background(), again, refusalOf(t, wager.InsufficientFunds))
	if got := testutil.ToFloat64(series.Duplicates.WithLabelValues("sqs", "redelivery")); got != 1 {
		t.Fatalf("duplicates{sqs,redelivery} = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(series.Rejections); got != 0 {
		t.Fatalf("rejection series moved by a redelivery = %d, want none", got)
	}
}

// The two conflicts of idempotency wrote no row and are duplicates under their
// own reasons; a refusal without a row that is neither is not counted at all.
func TestRejected_countsTheConflictsAsDuplicatesAndNothingForARefusalWithoutARow(t *testing.T) {
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
			reporter, series := countingReporter()
			reporter.Rejected(context.Background(), submitwager.Result{}, refusalOf(t, tc.code))
			if got := testutil.ToFloat64(series.Duplicates.WithLabelValues("sqs", tc.reason)); got != 1 {
				t.Fatalf("duplicates{sqs,%s} after a refusal without a row = %v, want 1", tc.reason, got)
			}
		})
	}
	reporter, series := countingReporter()
	reporter.Rejected(context.Background(), submitwager.Result{}, refusalOf(t, wager.WalletNotFound))
	if got := testutil.CollectAndCount(series.Settlements) + testutil.CollectAndCount(series.Rejections) + testutil.CollectAndCount(series.Duplicates); got != 0 {
		t.Fatalf("series moved by a refusal without a row = %d, want none", got)
	}
}

// A message handed back is counted by the reason off the chain, and the write
// that missed the lock has a reason of its own.
func TestReturned_countsTheRetryByTheReasonOffTheChain(t *testing.T) {
	t.Parallel()
	reporter, series := countingReporter()
	reporter.Returned(fmt.Errorf("receive wager: %w", storage.ErrLostWrite))
	reporter.Returned(fault.Wrap("acquire connection", errors.New("connection refused")))
	reporter.Returned(context.Canceled)
	if got := testutil.ToFloat64(series.Retries.WithLabelValues("sqs", "version_conflict")); got != 1 {
		t.Fatalf("retries{sqs,version_conflict} = %v, want 1", got)
	}
	if got := testutil.ToFloat64(series.Retries.WithLabelValues("sqs", "transient")); got != 1 {
		t.Fatalf("retries{sqs,transient} = %v, want 1: the shutdown is not a retry", got)
	}
}

func TestDepth_movesBothGauges(t *testing.T) {
	t.Parallel()
	reporter, series := countingReporter()
	reporter.Depth(3, 2)
	if got := testutil.ToFloat64(series.IngressDepth); got != 3 {
		t.Fatalf("ingress depth = %v, want 3", got)
	}
	if got := testutil.ToFloat64(series.DeadLetterDepth); got != 2 {
		t.Fatalf("dead-letter depth = %v, want 2", got)
	}
}

// The reporter writes through the handler it was given and moves the series it
// was given, and nothing else of the process.
func TestNewReporter_logsAndCountsThroughWhatItWasGiven(t *testing.T) {
	t.Parallel()
	logs := &bytes.Buffer{}
	series := metrics.New(prometheus.NewRegistry())
	reporter := NewReporter(slog.New(slog.NewJSONHandler(logs, nil)), quietTracer(), series)
	reporter.Settled(context.Background(), processedBet(t))
	if logs.Len() == 0 {
		t.Fatalf("log of the reporter = %q, want the line in the handler it was given", logs.String())
	}
	if got := testutil.ToFloat64(series.Settlements.WithLabelValues("sqs", "BET", "PROCESSED")); got != 1 {
		t.Fatalf("settlements{sqs,BET,PROCESSED} on the series it was given = %v, want 1", got)
	}
}

// countDuplicate answers whether the arrival was one, and a redelivery is told
// apart from a replay even though both are replays under the key.
func TestCountDuplicate_answersWhetherTheArrivalWasOneAndUnderWhichReason(t *testing.T) {
	t.Parallel()
	reporter, series := countingReporter()
	answers := map[string]struct {
		result submitwager.Result
		want   bool
	}{
		"a first outcome": {result: processedBet(t), want: false},
		"a redelivery":    {result: submitwager.Result{IdempotentReplay: true, Redelivered: true}, want: true},
		"a replay":        {result: submitwager.Result{IdempotentReplay: true}, want: true},
	}
	for name, each := range answers {
		if got := reporter.countDuplicate(each.result); got != each.want {
			t.Fatalf("countDuplicate of %s = %t, want %t", name, got, each.want)
		}
	}
	if got := testutil.ToFloat64(series.Duplicates.WithLabelValues("sqs", "redelivery")) + testutil.ToFloat64(series.Duplicates.WithLabelValues("sqs", "replay")); got != 2 {
		t.Fatalf("duplicates{sqs,redelivery} plus duplicates{sqs,replay} = %v, want one of each", got)
	}
}

// countRejected counts the row a rule wrote as a settlement that ended
// REJECTED and by its token, and nothing for a rule that wrote no row.
func TestCountRejected_countsTheRowTheRuleWroteAndNothingElse(t *testing.T) {
	t.Parallel()
	reporter, series := countingReporter()
	row := submitwager.Result{TransactionID: transactionOf(t), Kind: wager.KindBet, Status: wager.Rejected}
	reporter.countRejected(row, wager.CurrencyMismatch)
	reporter.countRejected(submitwager.Result{}, wager.OpeningNotAllowed)
	if got := testutil.ToFloat64(series.Rejections.WithLabelValues("sqs", "CURRENCY_MISMATCH")); got != 1 {
		t.Fatalf("rejections{sqs,CURRENCY_MISMATCH} = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(series.Rejections) + testutil.CollectAndCount(series.Settlements); got != 2 {
		t.Fatalf("series moved = %d, want the settlement and the rejection of the row alone", got)
	}
}
