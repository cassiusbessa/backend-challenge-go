package relayoutbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

func TestRelay_publishesUnderTheTokenOfItsOwnClaim(t *testing.T) {
	t.Parallel()
	queue := queueWith(t)
	relay(t, queue, &publisher{})
	if queue.confirmed != token {
		t.Fatalf("confirmed under %q, want the token of the claim %q", queue.confirmed, token)
	}
	if queue.rescheduled || queue.killed {
		t.Fatalf("rescheduled = %t and killed = %t, want neither beside the confirmation", queue.rescheduled, queue.killed)
	}
}

// The deadline of the send fits inside the lease, so a replica is never still
// publishing a row its lease no longer covers.
//
// What is asserted is a window still open, and not an instant: a deadline is
// read against the clock of the runtime, so one built from the injected clock
// would be a send that arrives already expired and a case that never notices.
func TestRelay_givesTheSendLessTimeThanTheLease(t *testing.T) {
	t.Parallel()
	sender := &publisher{}
	before := time.Now()
	relay(t, queueWith(t), sender)
	window := sender.deadline.Sub(before)
	if window <= 0 || window > lease {
		t.Fatalf("send window = %s, want it open and no wider than the lease of %s", window, lease)
	}
	if sender.ctxErr != nil {
		t.Fatalf("context of the send = %v, want a live one", sender.ctxErr)
	}
}

// The publication went through and the confirmation did not: the row stays
// unpublished and another replica picks it up once the lease expires. At least
// once is the contract, and the identity of the event is what closes the gap.
func TestRelay_leavesTheRowPublishableWhenTheConfirmationDoesNotLand(t *testing.T) {
	t.Parallel()
	queue := queueWith(t)
	queue.confirmErr = storage.ErrLeaseLost
	relay(t, queue, &publisher{})
	if queue.rescheduled || queue.killed {
		t.Fatalf("rescheduled = %t and killed = %t after a lost lease, want nothing written", queue.rescheduled, queue.killed)
	}
}

func TestRelay_sendsTheRowBackOnTheBackoffWhenTheBrokerIsOut(t *testing.T) {
	t.Parallel()
	queue := queueWith(t)
	queue.claimed.Attempts = 2
	relay(t, queue, &publisher{refuse: errors.New("service unavailable")})
	if !queue.rescheduled || queue.killed {
		t.Fatalf("rescheduled = %t and killed = %t on a transient refusal, want it rescheduled alone", queue.rescheduled, queue.killed)
	}
	if want := frozen.Add(4 * time.Second); !queue.next.Equal(want) {
		t.Fatalf("next attempt = %s, want %s", queue.next, want)
	}
}

func TestRelay_killsTheRowOnTheTenthPermanentRefusalAndNotOnTheNinth(t *testing.T) {
	t.Parallel()
	ninth := queueWith(t)
	ninth.claimed.Refusals = 8
	relay(t, ninth, &publisher{refuse: errors.New("topic does not exist"), permanent: true})
	if ninth.killed || !ninth.refused {
		t.Fatalf("killed = %t and refused = %t on the ninth refusal, want the row set aside instead", ninth.killed, ninth.refused)
	}
	tenth := queueWith(t)
	tenth.claimed.Refusals = 9
	relay(t, tenth, &publisher{refuse: errors.New("topic does not exist"), permanent: true})
	if !tenth.killed || tenth.confirmed != "" {
		t.Fatalf("killed = %t and confirmed = %q on the tenth refusal, want it dead and unpublished", tenth.killed, tenth.confirmed)
	}
}

// A broker that was out and came back leaves a row with attempts behind it and
// no refusal at all. The first refusal it then meets is the first of ten, and
// counting the attempts instead would make it the last.
func TestRelay_doesNotCountTheAttemptsOfAnOutageTowardsTheDeathOfTheRow(t *testing.T) {
	t.Parallel()
	queue := queueWith(t)
	queue.claimed.Attempts = 9
	queue.claimed.Refusals = 0
	relay(t, queue, &publisher{refuse: errors.New("topic does not exist"), permanent: true})
	if queue.killed {
		t.Fatalf("killed = %t after nine attempts and one refusal, want the row set aside", queue.killed)
	}
	if !queue.refused {
		t.Fatalf("refused = %t, want the refusal counted apart from the attempts", queue.refused)
	}
}

// A transitory failure moves the backoff and nothing else: the row it leaves
// behind is one no refusal has ever been made on.
func TestRelay_countsNoRefusalForAFailureThatMayComeBack(t *testing.T) {
	t.Parallel()
	queue := queueWith(t)
	relay(t, queue, &publisher{refuse: errors.New("service unavailable")})
	if queue.refused {
		t.Fatalf("refused = %t on a transitory failure, want only the attempt counted", queue.refused)
	}
}

func TestRelay_leavesTheSpanOkWhenTheRowOnlyComesBackLater(t *testing.T) {
	t.Parallel()
	queue := queueWith(t)
	opened := &spans{}
	service := New(queue, &publisher{refuse: errors.New("service unavailable")}, opened.open, frozenClock{}, quietLogger(), lease)
	run(t, service)
	if opened.linkedTrace != "4bf92f3577b34da6a3ce929d0e0e4736" || opened.closedWith != nil {
		t.Fatalf("span linked to %q closed with %v, want the trace of the commit and no error", opened.linkedTrace, opened.closedWith)
	}
}

func TestRelay_logsEveryOutcomeWithIdentifiersOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		sender *publisher
		want   string
	}{
		{name: "a send that went through", sender: &publisher{}, want: "published"},
		{name: "a permanent refusal", sender: &publisher{refuse: errors.New("refused"), permanent: true}, want: "refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			written := &bytes.Buffer{}
			queue := queueWith(t)
			run(t, New(queue, tc.sender, noSpan, frozenClock{}, allowingLogger(written), lease))
			assertLine(t, written.String(), tc.want)
		})
	}
}

func TestRelay_logsTheDeathOfARowNobodyCanPublish(t *testing.T) {
	t.Parallel()
	written := &bytes.Buffer{}
	queue := queueWith(t)
	queue.claimed.Refusals = 9
	sender := &publisher{refuse: errors.New("refused"), permanent: true}
	run(t, New(queue, sender, noSpan, frozenClock{}, allowingLogger(written), lease))
	assertLine(t, written.String(), "dead")
}

// A candidate the claim does not hand over is not this replica's to publish, and
// it is not a failure: the scan chose it and the claim is what decides.
func TestRelay_doesNothingWithACandidateTheClaimDidNotHandOver(t *testing.T) {
	t.Parallel()
	queue := queueWith(t)
	queue.claimErr = storage.ErrOutboxEventNotFound
	sender := &publisher{}
	run(t, New(queue, sender, noSpan, frozenClock{}, quietLogger(), lease))
	if sender.sent != nil {
		t.Fatalf("message sent = %v, want no send at all for a row this replica does not hold", sender.sent)
	}
}

// A claim this replica could not even take is the failure of the turn: the
// relay answers it so the runner logs it once and the next tick tries again.
func TestRelay_answersTheFailureOfAClaimItCouldNotTake(t *testing.T) {
	t.Parallel()
	broken := errors.New("postgres: connection reset by peer")
	queue := queueWith(t)
	queue.claimErr = broken
	service := New(queue, &publisher{}, noSpan, frozenClock{}, quietLogger(), lease)
	err := service.Relay(context.Background(), candidate(t))
	if !errors.Is(err, broken) {
		t.Fatalf("Relay over a claim that failed = %v, want %v", err, broken)
	}
}

// The shutdown deadline landing between the send and the confirmation does not
// take the confirmation with it. The message is on the topic from the moment the
// broker took it, and a row left unconfirmed is one another replica publishes a
// second time.
func TestRelay_confirmsTheSendThatWentThroughEvenAfterTheWorkWasCancelled(t *testing.T) {
	t.Parallel()
	queue := queueWith(t)
	cut, cancel := context.WithCancel(context.Background())
	sender := &publisher{onSend: cancel}
	service := New(queue, sender, noSpan, frozenClock{}, quietLogger(), lease)
	if err := service.Relay(cut, candidate(t)); err != nil {
		t.Fatalf("Relay over a send the deadline cut after the broker took it = %v, want nil", err)
	}
	if queue.confirmed != token {
		t.Fatalf("confirmed under %q, want the row confirmed under %q", queue.confirmed, token)
	}
	if queue.confirmCtxErr != nil {
		t.Fatalf("context of the confirmation = %v, want one the cancellation did not reach", queue.confirmCtxErr)
	}
}

// A write of the turn that failed for anything other than a lost lease is the
// failure of the turn, and it names the write that could not be made.
func TestRelay_answersTheFailureOfTheWriteThatEndsTheTurn(t *testing.T) {
	t.Parallel()
	broken := errors.New("postgres: connection reset by peer")
	queue := queueWith(t)
	queue.confirmErr = broken
	service := New(queue, &publisher{}, noSpan, frozenClock{}, quietLogger(), lease)
	err := service.Relay(context.Background(), candidate(t))
	if !errors.Is(err, broken) {
		t.Fatalf("Relay over a confirmation that failed = %v, want %v", err, broken)
	}
	if !strings.Contains(err.Error(), "confirm outbox row") {
		t.Fatalf("failure = %v, want the write that could not be made named in the chain", err)
	}
}

// assertLine reads the single line the turn wrote: the outcome, the identifiers
// that are allowed, and nothing of the payload.
func assertLine(t *testing.T, line, status string) {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &out); err != nil {
		t.Fatalf("the turn wrote %q, want one JSON line", line)
	}
	if out["status"] != status {
		t.Fatalf("status = %v, want %s", out["status"], status)
	}
	assertIdentifiers(t, out)
	if strings.Contains(line, "100.00") || strings.Contains(line, "payload") {
		t.Fatalf("the line carries the payload or an amount: %s", line)
	}
}

func assertIdentifiers(t *testing.T, out map[string]any) {
	t.Helper()
	for _, field := range []string{"eventId", "walletId", "trace_id", "span_id"} {
		if out[field] == nil || out[field] == "" {
			t.Fatalf("%s = %v, want it on the line", field, out[field])
		}
	}
}

const (
	lease     = 30 * time.Second
	token     = "9b2f1c6e-3a44-4c2b-8d5e-0f1a2b3c4d5e"
	eventUUID = "019974a4-0000-7000-8000-00000000e001"
)

var frozen = time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)

type frozenClock struct{}

func (frozenClock) Now() time.Time { return frozen }

// noSpan is the report of a turn nothing is watching, which is what a case that
// is not about telemetry hands in.
func noSpan(ctx context.Context, _, _ string) (context.Context, func(error)) {
	return ctx, func(error) {}
}

type spans struct {
	linkedTrace string
	closedWith  error
}

func (s *spans) open(ctx context.Context, traceID, _ string) (context.Context, func(error)) {
	s.linkedTrace = traceID
	return ctx, func(err error) { s.closedWith = err }
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
}

// allowingLogger is the handler of the process, so a case reads the line exactly
// as it reaches the collector — with the fields the pipeline refuses left out.
func allowingLogger(into *bytes.Buffer) *slog.Logger {
	return slog.New(telemetry.Allow(slog.NewJSONHandler(into, nil)))
}

// queue is the publication queue in memory: it hands out one claimed row and
// records which of the three ends of the turn was written.
type queue struct {
	claimed       storage.OutboxRow
	claimErr      error
	confirmErr    error
	confirmCtxErr error
	confirmed     string
	rescheduled   bool
	refused       bool
	killed        bool
	next          time.Time
	claimedLease  time.Duration
}

func (q *queue) Due(context.Context, int) ([]storage.OutboxCandidate, error) {
	return nil, nil
}

func (q *queue) Claim(_ context.Context, _ identity.EventID, lease time.Duration) (storage.OutboxRow, error) {
	q.claimedLease = lease
	if q.claimErr != nil {
		return storage.OutboxRow{}, q.claimErr
	}
	return q.claimed, nil
}

func (q *queue) Confirm(ctx context.Context, _ identity.EventID, token string, _ time.Time) error {
	q.confirmCtxErr = ctx.Err()
	if q.confirmErr != nil {
		return q.confirmErr
	}
	q.confirmed = token
	return nil
}

func (q *queue) Reschedule(_ context.Context, _ identity.EventID, _ string, next time.Time) error {
	q.rescheduled = true
	q.next = next
	return nil
}

func (q *queue) Refuse(_ context.Context, _ identity.EventID, _ string, next time.Time) error {
	q.rescheduled, q.refused = true, true
	q.next = next
	return nil
}

func (q *queue) Kill(context.Context, identity.EventID, string, time.Time) error {
	q.killed = true
	return nil
}

func queueWith(t *testing.T) *queue {
	t.Helper()
	return &queue{claimed: storage.OutboxRow{
		EventID:    eventOf(t, eventUUID),
		WalletID:   walletOf(t),
		EventType:  "WalletBalanceChanged",
		Payload:    []byte(`{"data":{"balanceAfter":{"amount":"100.00"}}}`),
		TraceID:    "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanID:     "00f067aa0ba902b7",
		LeaseToken: token,
	}}
}

// publisher is the broker in memory: it keeps what was sent, the budget the
// send was given, and answers the refusal the case is about.
type publisher struct {
	sent      *Message
	deadline  time.Time
	ctxErr    error
	refuse    error
	permanent bool
	// onSend acts while the send is in flight, which is how a case puts the
	// signal exactly between the message leaving and the row being confirmed.
	onSend func()
}

func (p *publisher) Publish(ctx context.Context, message Message) error {
	p.sent = &message
	if deadline, ok := ctx.Deadline(); ok {
		p.deadline = deadline
	}
	// The adapter reads the context before it reaches the broker, so this one
	// does too: a fake that published under a context with no time left would
	// let a whole suite pass over sends the real client never makes.
	if err := ctx.Err(); err != nil {
		p.ctxErr = err
		return err
	}
	if p.onSend != nil {
		p.onSend()
	}
	return p.refuse
}

func (p *publisher) Permanent(error) bool { return p.permanent }

func relay(t *testing.T, rows *queue, sender *publisher) {
	t.Helper()
	run(t, New(rows, sender, noSpan, frozenClock{}, quietLogger(), lease))
}

// run takes one turn over the single candidate of these cases. Nothing the
// relay decides about a row is a failure of the turn, so every case here
// expects nil back.
func run(t *testing.T, service *Service) {
	t.Helper()
	if err := service.Relay(context.Background(), candidate(t)); err != nil {
		t.Fatalf("Relay = %v, want nil", err)
	}
}

func candidate(t *testing.T) storage.OutboxCandidate {
	t.Helper()
	return storage.OutboxCandidate{EventID: eventOf(t, eventUUID), WalletID: walletOf(t)}
}

func eventOf(t *testing.T, text string) identity.EventID {
	t.Helper()
	id, err := identity.ParseEventID(text)
	if err != nil {
		t.Fatalf("ParseEventID = %v, want nil", err)
	}
	return id
}

func walletOf(t *testing.T) identity.WalletID {
	t.Helper()
	id, err := identity.ParseWalletID("019974a4-0000-7000-8000-00000000a11e")
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	return id
}
