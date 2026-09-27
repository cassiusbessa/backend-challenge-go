package wagerqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/receivewager"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

// deliveryLimit is the delivery this consumer gives up on. It is below the
// maxReceiveCount of the queue on purpose: the group of a message is the wallet,
// so a poisoned message holds up the operations of that wallet until it is out of
// the way, and taking it out ourselves is what keeps the broker from discarding
// the ones behind it.
const deliveryLimit = 5

// ErrDeliveryLimit is the message that has had every delivery this consumer
// grants it. It is the one door to the dead-letter queue with no refusal of its
// own to name, and it carries a sentinel anyway: go-observability marks the span
// of a message that leaves for the queue of the dead, and a door without an error
// would be the only one of the four to leave that span ok.
var ErrDeliveryLimit = errors.New("wagerqueue: message reached the delivery limit of this consumer")

// Delivery is one message as the broker hands it over.
//
// Sender is the identity the broker registered on it and Deliveries is the count
// of times it has been handed out, both asked for beside the body: the first is
// what authorizes the operation and the second is what ends a message no attempt
// can settle.
//
// Group and Deduplication are the two the FIFO queue requires, kept so the copy
// to the dead-letter queue carries them: a body this border could not read has no
// envelope to take them from.
type Delivery struct {
	Body          []byte
	Receipt       string
	Sender        string
	Deliveries    int64
	Group         string
	Deduplication string
	// Trace is the propagation the sender put in the attributes of the message,
	// and it is empty for a message that carried none.
	Trace map[string]string
}

// Queue is the ingress queue as the consumer works it.
//
// Receive asks for the sender and the delivery count together with the body:
// neither is in the payload, and a second call per message to fetch them would be
// a round trip against a broker for something the first one can answer.
type Queue interface {
	Receive(ctx context.Context, wait, visibility time.Duration) ([]Delivery, error)

	// Delete takes the message out of the queue, which is the answer to a decision
	// already committed.
	Delete(ctx context.Context, receipt string) error

	// Release makes the message visible again after that window, without taking it
	// out. A window of zero hands it back at once, which is what a shutdown that
	// could not finish the message answers.
	Release(ctx context.Context, receipt string, after time.Duration) error

	// DeadLetter copies the message to the dead-letter queue, with the group and
	// the deduplication it arrived under.
	DeadLetter(ctx context.Context, delivery Delivery) error

	// Depth and DeadLetterDepth answer how many messages each queue is holding,
	// which are the two series the dashboard watches. They are asked once per
	// turn rather than at every scrape, so a broker that is out cannot hold up
	// the metrics endpoint.
	Depth(ctx context.Context) (int64, error)
	DeadLetterDepth(ctx context.Context) (int64, error)
}

// Receiver is the use case that authorizes and settles one message.
type Receiver interface {
	Receive(ctx context.Context, delivery receivewager.Delivery) (submitwager.Result, error)
}

// Timing is what the three durations of the consumer are, and the relation between
// them is an invariant rather than three numbers: the invisibility of a message
// has to cover the wait of the long poll plus the time it takes to process it.
//
// Measured on the local broker: with an invisibility shorter than the wait of the
// poll, the fetch answers empty and still consumes a delivery — the message burns
// its budget of deliveries without ever having been processed, with no error and
// no log. Timeout is configured below the invisibility for the same reason, so a
// delivery is not spent on work the consumer has already given up on.
type Timing struct {
	Poll       time.Duration
	Visibility time.Duration
	Timeout    time.Duration
}

// Consumer is the third background component of the process. The zero value is not
// used: NewConsumer is the only constructor.
//
// It shares no abstraction with the reference worker and the outbox relay. Those
// two sweep on an interval; this turn is a blocking long poll with a decision per
// message, and it would have no use for the interval the two of them take.
type Consumer struct {
	queue    Queue
	receiver Receiver
	reporter *Reporter
	timing   Timing

	// fetching is closed by Stop and is the signal itself: the poll is cancelled
	// off it, and the loop takes no new message once it is closed. The Once is
	// because a lifecycle that stops twice must not close it twice.
	fetching chan struct{}
	stopOnce sync.Once
	// cut cancels the message in flight. Only the shutdown deadline reaches for
	// it: a message cancelled by the signal itself would be one given up on while
	// there was still time to finish it. It is nil before Start.
	cut context.CancelFunc
	// done is closed when the loop has left, which is what makes Stop wait for the
	// message in hand rather than return while it is still being decided.
	done chan struct{}
}

func NewConsumer(queue Queue, receiver Receiver, reporter *Reporter, timing Timing) *Consumer {
	return &Consumer{queue: queue, receiver: receiver, reporter: reporter, timing: timing}
}

// Start puts the consumer on its poll and answers at once: a queue with nothing
// in it must not hold the process back from listening.
func (c *Consumer) Start(starting context.Context) error {
	// The run outlives the start, so it takes the values of that context without
	// its deadline: a consumer cancelled by the startup timeout would stop the
	// moment the process finished coming up.
	work, cut := context.WithCancel(context.WithoutCancel(starting))
	c.cut = cut
	c.fetching = make(chan struct{})
	c.done = make(chan struct{})
	// The poll is cancelled by the signal and the work is not. A poll cut short
	// loses nothing — the messages stay in the queue — while a decision cut short
	// is a commit nobody is left to answer the broker for.
	polling, stopPolling := context.WithCancel(work)
	go func() {
		<-c.fetching
		stopPolling()
	}()
	go c.run(polling, work)
	return nil
}

// Stop fetches no new message and waits for the one in hand to be decided.
//
// The signal does not cancel that decision: the message has its own deadline to
// finish in, and one cut at the signal would be a commit whose outcome the broker
// never hears. What cancels it is the shutdown deadline, which is the promise this
// process made to the one that signalled it, and a message cut there goes back to
// the queue with no wait.
func (c *Consumer) Stop(ctx context.Context) error {
	if c.cut == nil {
		return nil
	}
	c.stopOnce.Do(func() { close(c.fetching) })
	select {
	case <-c.done:
		c.cut()
		return nil
	case <-ctx.Done():
		c.cut()
		return fault.Wrap("stop wager consumer", ctx.Err())
	}
}

// StopBudget is how long a stop of this consumer can legitimately take: the
// deadline of the message in hand plus the window its answer to the broker has.
//
// The lifecycle asks instead of restating it. A share written down over there would
// be right only until someone moved one of the two numbers that decide it, and
// nothing would fail when they did.
func (c *Consumer) StopBudget() time.Duration {
	return c.timing.Timeout + answerWindow
}

func (c *Consumer) run(polling, work context.Context) {
	defer close(c.done)
	for {
		if c.stopping(polling) {
			return
		}
		deliveries, err := c.queue.Receive(polling, c.timing.Poll, c.timing.Visibility)
		if err != nil {
			c.reporter.Failed(polling, "receive from the ingress queue", err)
			// A broker that is out would otherwise be asked again with no pause at
			// all: the poll answers at once when it fails, so the wait of the long
			// poll is no longer what paces the loop.
			if !c.pause(polling) {
				return
			}
			continue
		}
		c.turn(polling, work, deliveries)
		c.measure(polling)
	}
}

// measure reads the depth of the two queues into their gauges. A read that
// fails leaves the last values: a gauge that went to zero because the broker
// was out would read as a queue that drained.
func (c *Consumer) measure(polling context.Context) {
	ingress, err := c.queue.Depth(polling)
	if err != nil {
		c.reporter.Failed(polling, "read the depth of the ingress queue", err)
		return
	}
	dead, err := c.queue.DeadLetterDepth(polling)
	if err != nil {
		c.reporter.Failed(polling, "read the depth of the dead-letter queue", err)
		return
	}
	c.reporter.Depth(ingress, dead)
}

// pause waits one base window and reports whether the loop goes on.
func (c *Consumer) pause(polling context.Context) bool {
	timer := time.NewTimer(base)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-polling.Done():
		return false
	}
}

// stopping reports whether the consumer is on its way out.
//
// It asks the signal itself beside the context derived from it, because the
// cancellation of that context lands a moment later and one message would be
// taken inside that window. Asking both is one question and not two, and it holds
// if the poll ever gains a cancellation of its own — which today it has not: the
// only path to a cancelled poll goes through the signal.
func (c *Consumer) stopping(polling context.Context) bool {
	select {
	case <-c.fetching:
		return true
	case <-polling.Done():
		return true
	default:
		return false
	}
}

// turn decides the messages one fetch answered.
func (c *Consumer) turn(polling, work context.Context, deliveries []Delivery) {
	for _, delivery := range deliveries {
		if c.stopping(polling) {
			// The signal came mid batch. go-sqs-ingress hands a message nobody
			// is left to process straight back: the rest of the batch was never
			// decided, so it goes visible at once for another replica — or for
			// this one when it comes back — instead of waiting out a window no
			// process here is serving.
			c.release(work, delivery, 0)
			continue
		}
		c.decide(work, delivery)
	}
}

// decide takes one message through the use case and answers the broker.
//
// The body is decoded before the span is opened because the correlation of the
// operation is in the envelope: a message that carries none is correlated by its
// own identity, and that decision belongs to the reporter.
//
// The deadline of the message is its own and is shorter than the invisibility, so
// a decision the consumer has given up on does not spend the delivery it was
// handed.
func (c *Consumer) decide(work context.Context, delivery Delivery) {
	decoded, refusal := Decode(delivery.Body)
	ctx, cancel := context.WithTimeout(work, c.timing.Timeout)
	defer cancel()
	ctx, closeSpan := c.reporter.Receiving(ctx, delivery, decoded)
	if delivery.Deliveries >= deliveryLimit {
		// The message has had every delivery this consumer grants it. It leaves
		// before the broker discards the ones behind it in its group.
		closeSpan(ErrDeliveryLimit)
		c.abandon(ctx, delivery, metrics.AbandonDeliveryLimit, ErrDeliveryLimit)
		return
	}
	result, err := c.settle(ctx, delivery, decoded, refusal)
	closeSpan(err)
	c.answer(ctx, work, delivery, result, err)
}

// settle hands the decoded message to the use case, or answers the refusal the
// decode already decided.
//
// The hash is of the body that arrived, which is what the inbox records and what
// tells a redelivery of the same bytes from the same identifier re-presented with
// another content.
func (c *Consumer) settle(ctx context.Context, delivery Delivery, decoded Message, refusal error) (submitwager.Result, error) {
	if refusal != nil {
		return submitwager.Result{}, refusal
	}
	ctx, done := telemetry.Step(ctx, "receive wager")
	result, err := c.receiver.Receive(ctx, receivewager.Delivery{
		MessageID: decoded.MessageID,
		Sender:    delivery.Sender,
		BodyHash:  hashOf(delivery.Body),
		Command:   decoded.Command,
	})
	done(err)
	return result, err
}

func hashOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// answer tells the broker what the outcome means for the message.
//
// The work context is what the release and the delete are taken on, not the one
// of the decision: that one may well be the deadline that ended the decision, and
// a broker call on a cancelled context would leave the message with nobody
// answering for it.
func (c *Consumer) answer(ctx, work context.Context, delivery Delivery, result submitwager.Result, err error) {
	answer, reason := answerOf(err)
	switch answer {
	case Remove:
		// Both arms remove the message and they are not the same line: one settled
		// and a rule refused the other. go-observability asks the rejection to log
		// its token beside the row it wrote, so the reporter is told which of the
		// two happened and is given both halves of it.
		if refusal, refused := rejectionOf(err); refused {
			c.reporter.Rejected(ctx, result, refusal)
		} else {
			c.reporter.Settled(ctx, result)
		}
		c.remove(ctx, delivery)
	case Abandon:
		c.abandon(ctx, delivery, reason, err)
	case Return:
		c.reporter.Failed(ctx, "settle a message of the ingress queue", err)
		c.reporter.Returned(err)
		c.release(ctx, delivery, c.returned(work, delivery))
	}
}

// returned answers how long the message stays invisible before the delivery that
// follows.
//
// A message the shutdown deadline cut goes back at once: the process is on its way
// out, and another replica — or this one when it comes back — takes it without
// waiting out a window nobody is left to serve.
func (c *Consumer) returned(work context.Context, delivery Delivery) time.Duration {
	if work.Err() != nil {
		return 0
	}
	return Backoff(delivery.Deliveries)
}

func (c *Consumer) remove(ctx context.Context, delivery Delivery) {
	answering, cancel := c.answering(ctx)
	defer cancel()
	if err := c.queue.Delete(answering, delivery.Receipt); err != nil {
		// The decision is committed and the message stays in the queue. The inbox
		// is what makes the delivery that follows change nothing.
		c.reporter.Failed(answering, "delete a settled message", err)
	}
}

// abandon copies the message to the dead-letter queue and takes it out of the
// ingress one, in that order: a message deleted first and copied after would be a
// message lost when the copy fails.
func (c *Consumer) abandon(ctx context.Context, delivery Delivery, reason string, err error) {
	answering, cancel := c.answering(ctx)
	defer cancel()
	if failure := c.queue.DeadLetter(answering, delivery); failure != nil {
		c.reporter.Failed(answering, "copy a message to the dead-letter queue", failure)
		return
	}
	c.reporter.Abandoned(answering, delivery, reason, err)
	if failure := c.queue.Delete(answering, delivery.Receipt); failure != nil {
		c.reporter.Failed(answering, "delete an abandoned message", failure)
	}
}

func (c *Consumer) release(ctx context.Context, delivery Delivery, after time.Duration) {
	answering, cancel := c.answering(ctx)
	defer cancel()
	if err := c.queue.Release(answering, delivery.Receipt, after); err != nil {
		// The window the broker already holds the message under runs out on its
		// own, so the message comes back either way, just later.
		c.reporter.Failed(answering, "release a message of the ingress queue", err)
	}
}

// answering is the context of one call back to the broker. It survives the
// cancellation of the decision, because a message whose deadline ran out still has
// to be handed back, and it carries a window of its own so a broker that is out
// cannot hold the shutdown open.
//
// The values of the decision are kept, so the line that reports a broker refusing
// the answer still names the message it was about.
//
// Asking the decision alone is enough: it is derived from the work, so the shutdown
// cancelling the work cancels it too, and a second test of the work would answer
// the same thing twice.
func (c *Consumer) answering(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return context.WithTimeout(ctx, answerWindow)
	}
	return context.WithTimeout(context.WithoutCancel(ctx), answerWindow)
}

// answerWindow is how long a call back to the broker has once the shutdown
// deadline has cut the work. It is short on purpose: what it buys is the message
// going back at once instead of waiting out its invisibility.
const answerWindow = 2 * time.Second
