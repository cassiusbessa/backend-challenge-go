package wagerqueue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/junglegaming/backend-challenge-go/internal/app/receivewager"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/authz"
)

// quick is the timing of a case: short enough that nothing waits, and still
// ordered the way the invariant asks — the invisibility covers the poll plus the
// decision.
var quick = Timing{Poll: 20 * time.Millisecond, Visibility: 60 * time.Millisecond, Timeout: 30 * time.Millisecond}

func TestDecide_removesTheMessageOfACommittedOutcome(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{}
	consumer, _, _ := consumerOver(t, queue, &fakeReceiver{status: wager.Processed})
	consumer.decide(context.Background(), arrived(1))
	if queue.deleted != 1 {
		t.Fatalf("deletes = %d, want 1", queue.deleted)
	}
	if queue.released != 0 || queue.abandoned != 0 {
		t.Fatalf("releases and copies = %d and %d, want 0 and 0", queue.released, queue.abandoned)
	}
}

// A rule refusing the operation wrote its row with the token. The decision is
// durable, so the message leaves rather than coming back.
func TestDecide_removesTheMessageOfABusinessRejection(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{}
	refused := &fakeReceiver{refuse: wager.NewRejection(wager.InsufficientFunds, nil)}
	consumer, _, _ := consumerOver(t, queue, refused)
	consumer.decide(context.Background(), arrived(1))
	if queue.deleted != 1 {
		t.Fatalf("deletes = %d, want 1: a rejection is a decision already recorded", queue.deleted)
	}
	if queue.abandoned != 0 {
		t.Fatalf("copies to the dead-letter queue = %d, want 0", queue.abandoned)
	}
}

// The window grows by a factor of two from one second and stops at sixty, and the
// message is not taken out of the queue.
func TestDecide_returnsTheMessageWithTheBackoffOfItsDeliveryCount(t *testing.T) {
	t.Parallel()
	cases := []struct {
		deliveries int64
		window     time.Duration
	}{
		{deliveries: 1, window: time.Second},
		{deliveries: 2, window: 2 * time.Second},
		{deliveries: 3, window: 4 * time.Second},
		{deliveries: 4, window: 8 * time.Second},
	}
	for _, tc := range cases {
		t.Run("delivery "+itoa(tc.deliveries)+" waits its window", func(t *testing.T) {
			queue := &fakeQueue{}
			consumer, _, _ := consumerOver(t, queue, &fakeReceiver{refuse: errors.New("connection reset")})
			consumer.decide(context.Background(), arrived(tc.deliveries))
			if queue.released != 1 {
				t.Fatalf("releases = %d, want 1", queue.released)
			}
			if queue.window != tc.window {
				t.Fatalf("window = %s, want %s", queue.window, tc.window)
			}
			if queue.deleted != 0 {
				t.Fatalf("deletes = %d, want 0: a transient failure keeps the message", queue.deleted)
			}
		})
	}
}

// The ceiling is the rule and not the doubling: a message around for a long time
// waits sixty seconds and never more.
func TestBackoff_stopsAtItsCeiling(t *testing.T) {
	t.Parallel()
	if got := Backoff(7); got != 60*time.Second {
		t.Fatalf("Backoff of the seventh delivery = %s, want 60s", got)
	}
	if got := Backoff(700); got != 60*time.Second {
		t.Fatalf("Backoff far past the ceiling = %s, want 60s", got)
	}
	// The first delivery has had no failure behind it, so it waits the base.
	if got := Backoff(1); got != time.Second {
		t.Fatalf("Backoff of the first delivery = %s, want 1s", got)
	}
}

// The fifth delivery is the one this consumer gives up on, and it leaves before the
// broker discards the messages behind it in its group. The fourth is still tried.
func TestDecide_givesUpOnTheFifthDeliveryAndNotOnTheFourth(t *testing.T) {
	t.Parallel()
	fifth := &fakeQueue{}
	consumer, _, _ := consumerOver(t, fifth, &fakeReceiver{status: wager.Processed})
	consumer.decide(context.Background(), arrived(5))
	if fifth.abandoned != 1 {
		t.Fatalf("copies to the dead-letter queue on the fifth delivery = %d, want 1", fifth.abandoned)
	}
	if fifth.deleted != 1 {
		t.Fatalf("deletes on the fifth delivery = %d, want 1", fifth.deleted)
	}

	fourth := &fakeQueue{}
	consumer, _, _ = consumerOver(t, fourth, &fakeReceiver{refuse: errors.New("connection reset")})
	consumer.decide(context.Background(), arrived(4))
	if fourth.abandoned != 0 {
		t.Fatalf("copies on the fourth delivery = %d, want 0", fourth.abandoned)
	}
	if fourth.released != 1 {
		t.Fatalf("releases on the fourth delivery = %d, want 1", fourth.released)
	}
}

// The limit of the application is below the maxReceiveCount the queue is
// provisioned with, which is what keeps the broker from discarding the message
// behind a poisoned head before its time.
func TestDeliveryLimit_staysBelowTheRedriveOfTheQueue(t *testing.T) {
	t.Parallel()
	// The redrive of wager-transactions.fifo, in deploy/terraform/localstack.
	const maxReceiveCount = 15
	if deliveryLimit >= maxReceiveCount {
		t.Fatalf("limit of the application = %d, want it below the %d of the queue", deliveryLimit, maxReceiveCount)
	}
}

// The copy comes before the delete: a message taken out first and copied after
// would be a message lost when the copy fails.
func TestDecide_keepsTheMessageWhenTheCopyToTheDeadLetterQueueFails(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{deadLetterErr: errors.New("connection refused")}
	consumer, _, _ := consumerOver(t, queue, &fakeReceiver{status: wager.Processed})
	consumer.decide(context.Background(), arrived(5))
	if queue.deleted != 0 {
		t.Fatalf("deletes = %d, want 0: a copy that failed must not take the message out", queue.deleted)
	}
}

// An invalid body never reaches the use case: the refusal of the decode is what
// abandons it, and no wallet is touched.
func TestDecide_abandonsAnInvalidBodyWithoutReachingTheUseCase(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{}
	receiver := &fakeReceiver{status: wager.Processed}
	consumer, logs, metrics := consumerOver(t, queue, receiver)
	broken := arrived(1)
	broken.Body = []byte("{this is not json")
	consumer.decide(context.Background(), broken)
	if receiver.calls != 0 {
		t.Fatalf("submissions = %d, want 0", receiver.calls)
	}
	if queue.abandoned != 1 || queue.deleted != 1 {
		t.Fatalf("copies and deletes = %d and %d, want 1 and 1", queue.abandoned, queue.deleted)
	}
	assertCounted(t, metrics, reasonInvalidBody, 1)
	line := lineWith(t, logs, "abandoned")
	assertNoSecrets(t, line)
	if line["reason"] != reasonInvalidBody {
		t.Fatalf("reason = %v, want %s", line["reason"], reasonInvalidBody)
	}
	if line["messageId"] != deduplicationID {
		t.Fatalf("messageId = %v, want the %s the broker registered", line["messageId"], deduplicationID)
	}
}

// The refusal by sender records the observed identity: a map naming the wrong one
// sends every legitimate message here, and this line is the only place the true
// value can be read from.
func TestDecide_recordsTheObservedSenderWhenTheMapRefusesIt(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{}
	refused := &fakeReceiver{refuse: authz.ErrUnmappedSender}
	consumer, logs, metrics := consumerOver(t, queue, refused)
	consumer.decide(context.Background(), arrived(1))
	if queue.abandoned != 1 {
		t.Fatalf("copies to the dead-letter queue = %d, want 1", queue.abandoned)
	}
	assertCounted(t, metrics, reasonRefusedSender, 1)
	line := lineWith(t, logs, "abandoned")
	if line["sender"] != observedSender {
		t.Fatalf("sender = %v, want the observed %s", line["sender"], observedSender)
	}
	assertNoSecrets(t, line)
}

// The identity of the message is on every line of that operation, and the
// correlation of an envelope that carried none is that same identity.
func TestDecide_namesTheMessageOnEveryLineAndCorrelatesByItWhenThereIsNoneInTheEnvelope(t *testing.T) {
	t.Parallel()
	consumer, logs, _ := consumerOver(t, &fakeQueue{}, &fakeReceiver{status: wager.Processed})
	delivery := arrived(1)
	delivery.Body = message(map[string]any{"correlationId": nil})
	consumer.decide(context.Background(), delivery)
	line := lineWith(t, logs, "settled")
	if line["messageId"] != messageID {
		t.Fatalf("messageId = %v, want %s", line["messageId"], messageID)
	}
	if line["correlationId"] != messageID {
		t.Fatalf("correlationId = %v, want the identity of the message %s", line["correlationId"], messageID)
	}
}

func TestDecide_correlatesByTheEnvelopeWhenItCarriesOne(t *testing.T) {
	t.Parallel()
	consumer, logs, _ := consumerOver(t, &fakeQueue{}, &fakeReceiver{status: wager.Processed})
	consumer.decide(context.Background(), arrived(1))
	line := lineWith(t, logs, "settled")
	if line["correlationId"] != correlationID {
		t.Fatalf("correlationId = %v, want the %s of the envelope", line["correlationId"], correlationID)
	}
	if line["messageId"] != messageID {
		t.Fatalf("messageId = %v, want %s", line["messageId"], messageID)
	}
}

// The span of the consumer belongs to the trace the sender propagated, rather than
// opening one cut off from the origin.
func TestDecide_continuesTheTraceTheMessageCarried(t *testing.T) {
	t.Parallel()
	consumer, _, _ := consumerOver(t, &fakeQueue{}, &fakeReceiver{status: wager.Processed})
	spans := recorded(t, consumer)
	delivery := arrived(1)
	delivery.Trace = map[string]string{"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}
	consumer.decide(context.Background(), delivery)
	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("spans = %d, want 1", len(ended))
	}
	if got := ended[0].SpanContext().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace = %s, want the one of the origin", got)
	}
}

func TestDecide_opensATraceOfItsOwnForAMessageWithNoPropagation(t *testing.T) {
	t.Parallel()
	consumer, _, _ := consumerOver(t, &fakeQueue{}, &fakeReceiver{status: wager.Processed})
	spans := recorded(t, consumer)
	consumer.decide(context.Background(), arrived(1))
	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("spans = %d, want 1", len(ended))
	}
	if !ended[0].SpanContext().TraceID().IsValid() {
		t.Fatalf("trace = %s, want one of its own", ended[0].SpanContext().TraceID())
	}
	if ended[0].Parent().IsValid() {
		t.Fatalf("parent = %s, want none", ended[0].Parent().SpanID())
	}
}

// The decision is bounded by a deadline of its own, below the invisibility, so a
// message that volunteered more work than that does not spend its delivery.
func TestDecide_boundsTheDecisionByTheTimeoutOfTheConfiguration(t *testing.T) {
	t.Parallel()
	slow := &fakeReceiver{hold: 200 * time.Millisecond}
	queue := &fakeQueue{}
	consumer, _, _ := consumerOver(t, queue, slow)
	consumer.decide(context.Background(), arrived(1))
	if slow.deadline == 0 {
		t.Fatalf("deadline handed to the use case = none, want the timeout of the configuration")
	}
	if slow.deadline > quick.Timeout {
		t.Fatalf("deadline = %s, want it no wider than the timeout %s", slow.deadline, quick.Timeout)
	}
	if queue.deleted != 0 {
		t.Fatalf("deletes = %d, want 0: nothing was committed", queue.deleted)
	}
	if queue.released != 1 {
		t.Fatalf("releases = %d, want 1", queue.released)
	}
}

// The signal stops the fetching and the loop leaves. What is in hand is not cut by
// the signal: only the shutdown deadline is allowed to do that.
func TestStop_fetchesNoNewMessageAfterTheSignal(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{fetched: make(chan struct{})}
	consumer, _, _ := consumerOver(t, queue, &fakeReceiver{status: wager.Processed})
	if err := consumer.Start(context.Background()); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	queue.awaitFetch(t)
	during := queue.fetches()
	// Stop answers nil only once the loop has left, so the count cannot grow after
	// it: there is nobody left to fetch. That is the assertion, and it needs no
	// waiting — a sleep here would only be a slower way of reading the same thing.
	if err := consumer.Stop(context.Background()); err != nil {
		t.Fatalf("Stop = %v, want nil", err)
	}
	if got := queue.fetches(); got != during {
		t.Fatalf("fetches after the signal = %d, want the %d it had stopped at", got, during)
	}
	// The poll in flight is cancelled by the signal, which is what makes the loop
	// leave inside the deadline instead of waiting out the long poll.
	if !queue.cancelled() {
		t.Fatalf("poll cancelled by the signal = false, want true")
	}
}

// A message the shutdown deadline cut goes back at once rather than waiting out a
// window nobody is left to serve, and it is not taken out of the queue.
func TestDecide_handsTheMessageBackAtOnceWhenTheShutdownCutTheDecision(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{}
	consumer, _, _ := consumerOver(t, queue, &fakeReceiver{refuse: context.Canceled})
	work, cut := context.WithCancel(context.Background())
	cut()
	consumer.decide(work, arrived(1))
	if queue.released != 1 {
		t.Fatalf("releases = %d, want 1", queue.released)
	}
	if queue.window != 0 {
		t.Fatalf("window = %s, want 0", queue.window)
	}
	if queue.deleted != 0 {
		t.Fatalf("deletes = %d, want 0: nothing was committed", queue.deleted)
	}
}

// Stop before Start has nothing to end, which is what a graph that failed to come
// up calls.
func TestStop_answersNilWhenTheConsumerNeverStarted(t *testing.T) {
	t.Parallel()
	consumer, _, _ := consumerOver(t, &fakeQueue{}, &fakeReceiver{})
	if err := consumer.Stop(context.Background()); err != nil {
		t.Fatalf("Stop of a consumer that never started = %v, want nil", err)
	}
}

// A broker that refuses the answer is reported and nothing else: the message stays
// where it is, and the window it is already held under runs out on its own.
func TestDecide_reportsTheBrokerRefusingTheAnswerAndLeavesTheMessage(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection refused")
	cases := []struct {
		name     string
		queue    *fakeQueue
		receiver *fakeReceiver
		reported string
	}{
		{
			// The decision is committed and the message stays in the queue. The
			// inbox is what makes the delivery that follows change nothing.
			name:     "a delete that failed on a settled message",
			queue:    &fakeQueue{deleteErr: broken},
			receiver: &fakeReceiver{status: wager.Processed},
			reported: "delete a settled message",
		},
		{
			name:     "a delete that failed on an abandoned message",
			queue:    &fakeQueue{deleteErr: broken},
			receiver: &fakeReceiver{refuse: authz.ErrUnmappedSender},
			reported: "delete an abandoned message",
		},
		{
			name:     "a release that failed",
			queue:    &fakeQueue{releaseErr: broken},
			receiver: &fakeReceiver{refuse: errors.New("connection reset")},
			reported: "release a message of the ingress queue",
		},
		{
			name:     "a copy to the dead-letter queue that failed",
			queue:    &fakeQueue{deadLetterErr: broken},
			receiver: &fakeReceiver{refuse: authz.ErrUnmappedSender},
			reported: "copy a message to the dead-letter queue",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is reported", func(t *testing.T) {
			consumer, logs, _ := consumerOver(t, tc.queue, tc.receiver)
			consumer.decide(context.Background(), arrived(1))
			line := lineWith(t, logs, tc.reported)
			if line["messageId"] != messageID {
				t.Fatalf("messageId = %v, want %s", line["messageId"], messageID)
			}
			if _, carried := line["stack"]; !carried {
				t.Fatalf("line carries no stack, want the frames of the failure: %v", line)
			}
		})
	}
}

// A poll that failed does not spin: the loop reports it and waits one base window
// before asking again, because the wait of the long poll is no longer what paces it.
func TestRun_reportsAPollThatFailedAndDoesNotSpin(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{receiveErr: errors.New("connection refused"), fetched: make(chan struct{})}
	consumer, logs, _ := consumerOver(t, queue, &fakeReceiver{})
	if err := consumer.Start(context.Background()); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	queue.awaitFetch(t)
	if err := consumer.Stop(context.Background()); err != nil {
		t.Fatalf("Stop = %v, want nil", err)
	}
	// The pause is a whole base window, so a loop that spun would have fetched far
	// more than a handful of times before the signal landed.
	if got := queue.fetches(); got > 2 {
		t.Fatalf("fetches = %d, want at most 2: a poll that failed waits before the next", got)
	}
	lineWith(t, logs, "receive from the ingress queue")
}

func TestMeasure_movesTheDepthOfTheDeadLetterQueueIntoTheMetric(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{depth: 4}
	consumer, _, metrics := consumerOver(t, queue, &fakeReceiver{})
	consumer.measure(context.Background())
	if got := gaugeOf(t, metrics); got != 4 {
		t.Fatalf("depth = %v, want 4", got)
	}
}

// A read that failed leaves the last value: a gauge that went to zero because the
// broker was out would read as a queue that drained.
func TestMeasure_keepsTheLastDepthWhenTheBrokerIsOut(t *testing.T) {
	t.Parallel()
	queue := &fakeQueue{depth: 4}
	consumer, _, metrics := consumerOver(t, queue, &fakeReceiver{})
	consumer.measure(context.Background())
	queue.depthErr = errors.New("connection refused")
	consumer.measure(context.Background())
	if got := gaugeOf(t, metrics); got != 4 {
		t.Fatalf("depth = %v, want the 4 of the last read", got)
	}
}

// The values one arrival carries. A case overrides only what it is about.
const (
	observedSender  = "000000000000"
	deduplicationID = "message-1"
)

func arrived(deliveries int64) Delivery {
	return Delivery{
		Body:          message(nil),
		Receipt:       "receipt-1",
		Sender:        observedSender,
		Deliveries:    deliveries,
		Group:         "wallet-1",
		Deduplication: deduplicationID,
	}
}

func message(top map[string]any) []byte {
	return envelopeWith(top, nil)
}

func consumerOver(t *testing.T, queue Queue, receiver Receiver) (*Consumer, *bytes.Buffer, *Metrics) {
	t.Helper()
	logs := &bytes.Buffer{}
	metrics := NewMetrics(prometheus.NewRegistry())
	reporter := NewReporter(slog.New(slog.NewJSONHandler(logs, nil)), quietTracer(), metrics)
	return NewConsumer(queue, receiver, reporter, quick), logs, metrics
}

// recorded swaps the tracer of the reporter for one that keeps the spans it ended.
func recorded(t *testing.T, consumer *Consumer) *tracetest.SpanRecorder {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	consumer.reporter.tracer = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test")
	return spans
}

// quietTracer stands for the tracer of the process in the cases that are not about
// the trace. It is the one of the default provider, which records nothing.
func quietTracer() trace.Tracer {
	return noop.NewTracerProvider().Tracer("wagerqueue")
}

// fakeQueue is the broker as the consumer works it, and keeps what reached it.
type fakeQueue struct {
	mu            sync.Mutex
	polls         int
	deleted       int
	released      int
	abandoned     int
	window        time.Duration
	depth         int64
	depthErr      error
	deadLetterErr error
	deleteErr     error
	releaseErr    error
	receiveErr    error
	fetched       chan struct{}
	once          sync.Once
	cut           bool
}

func (q *fakeQueue) Receive(ctx context.Context, wait, _ time.Duration) ([]Delivery, error) {
	q.mu.Lock()
	q.polls++
	q.mu.Unlock()
	q.once.Do(func() {
		if q.fetched != nil {
			close(q.fetched)
		}
	})
	if q.receiveErr != nil {
		return nil, q.receiveErr
	}
	// The poll blocks the way a long poll does, and answers nothing: every case
	// about a message drives decide directly.
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil, nil
	case <-ctx.Done():
		q.mu.Lock()
		q.cut = true
		q.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (q *fakeQueue) cancelled() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.cut
}

// The three answers back to the broker honour the context, the way a real call
// would: an answer taken on a context that is already done reaches nobody, which is
// what makes the window the consumer chooses for them observable.
func (q *fakeQueue) Delete(ctx context.Context, _ string) error {
	if err := reachable(ctx); err != nil {
		return err
	}
	if q.deleteErr != nil {
		return q.deleteErr
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.deleted++
	return nil
}

func (q *fakeQueue) Release(ctx context.Context, _ string, after time.Duration) error {
	if err := reachable(ctx); err != nil {
		return err
	}
	if q.releaseErr != nil {
		return q.releaseErr
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.released++
	q.window = after
	return nil
}

func (q *fakeQueue) DeadLetter(ctx context.Context, _ Delivery) error {
	if err := reachable(ctx); err != nil {
		return err
	}
	if q.deadLetterErr != nil {
		return q.deadLetterErr
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.abandoned++
	return nil
}

func reachable(ctx context.Context) error {
	return ctx.Err()
}

func (q *fakeQueue) DeadLetterDepth(context.Context) (int64, error) {
	if q.depthErr != nil {
		return 0, q.depthErr
	}
	return q.depth, nil
}

func (q *fakeQueue) fetches() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.polls
}

// awaitFetch waits for the loop to have reached the poll, so a case about the
// signal is not racing the start.
func (q *fakeQueue) awaitFetch(t *testing.T) {
	t.Helper()
	select {
	case <-q.fetched:
	case <-time.After(time.Second):
		t.Fatalf("the consumer did not reach the poll within a second")
	}
}

// fakeReceiver stands for the use case that authorizes and settles one message.
type fakeReceiver struct {
	status wager.Status
	refuse error
	// hold is how long it pretends to work, which is what makes the deadline of
	// the decision observable.
	hold     time.Duration
	deadline time.Duration
	calls    int
}

func (f *fakeReceiver) Receive(ctx context.Context, _ receivewager.Delivery) (submitwager.Result, error) {
	f.calls++
	if deadline, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(deadline)
	}
	if f.hold > 0 {
		timer := time.NewTimer(f.hold)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return submitwager.Result{}, ctx.Err()
		}
	}
	if f.refuse != nil {
		return submitwager.Result{}, f.refuse
	}
	return submitwager.Result{Status: f.status, Kind: wager.KindBet}, nil
}

// lineWith answers the log line whose message contains that word, so a case reads
// the line it is about instead of the first one written.
func lineWith(t *testing.T, logs *bytes.Buffer, word string) map[string]any {
	t.Helper()
	for _, raw := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			continue
		}
		if text, ok := line["msg"].(string); ok && strings.Contains(text, word) {
			return line
		}
	}
	t.Fatalf("no log line about %q in:\n%s", word, logs.String())
	return nil
}

// assertNoSecrets pins what a line of the ingress must never carry: the body, an
// amount, a balance, the idempotency key or a credential.
func assertNoSecrets(t *testing.T, line map[string]any) {
	t.Helper()
	for _, forbidden := range []string{"body", "amount", "balance", "idempotencyKey", "money", "authorization"} {
		if _, found := line[forbidden]; found {
			t.Fatalf("line carries %q, want it absent: %v", forbidden, line)
		}
	}
}

func assertCounted(t *testing.T, metrics *Metrics, reason string, want float64) {
	t.Helper()
	if got := counterOf(t, metrics, reason); got != want {
		t.Fatalf("abandonments counted for %s = %v, want %v", reason, got, want)
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

// counterOf and gaugeOf read one series out of the registry, which is the only way
// to see a Prometheus value without scraping the endpoint.
func counterOf(t *testing.T, metrics *Metrics, reason string) float64 {
	t.Helper()
	var out dto.Metric
	if err := metrics.Abandoned.WithLabelValues(reason).Write(&out); err != nil {
		t.Fatalf("read the counter = %v, want nil", err)
	}
	return out.GetCounter().GetValue()
}

func gaugeOf(t *testing.T, metrics *Metrics) float64 {
	t.Helper()
	var out dto.Metric
	if err := metrics.DeadLetterDepth.Write(&out); err != nil {
		t.Fatalf("read the gauge = %v, want nil", err)
	}
	return out.GetGauge().GetValue()
}
