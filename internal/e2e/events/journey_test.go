//go:build integration

// The journeys of the outbox against the real stack: what one commit writes,
// what the relay moves out of it, and what reaches the topic.
package events

import (
	"errors"
	"slices"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// The whole path of one settled bet: the two rows of its commit, the relay
// marking them published, and the two messages on the topic deduplicated by the
// very identities the rows carry.
func TestRelay_carriesASettledBetFromItsCommitToTheTopic(t *testing.T) {
	ctx, stack := stack(t)
	reader := subscribe(ctx, t)
	owner := stack.openWallet(ctx, t)
	settled := stack.bet(ctx, t, owner)

	written := of(stack.rows(ctx, t, owner.WalletID), settled.TransactionID.String())
	assertTypes(t, written, event.TypeProcessed, event.TypeBalanceChanged)
	assertPublished(t, written, false)

	stack.drain(ctx, t, stack.relay(t, publisher(ctx, t)), owner.WalletID)
	assertPublished(t, of(stack.rows(ctx, t, owner.WalletID), settled.TransactionID.String()), true)

	// The wallet was opened with money, so its birth wrote two events of its
	// own ahead of the bet: four reach the topic and two of them are the bet's.
	assertOnTopic(t, reader.receive(ctx, t, 4), written)
}

func assertPublished(t *testing.T, rows []row, want bool) {
	t.Helper()
	for _, each := range rows {
		if each.Published != want {
			t.Fatalf("event %s published = %t, want %t", each.EventID, each.Published, want)
		}
	}
}

// assertOnTopic looks for each row among what the subscriber read, matched by
// the deduplication the send carried: the identity of the event itself.
func assertOnTopic(t *testing.T, onTopic []delivered, rows []row) {
	t.Helper()
	for _, each := range rows {
		if !slices.ContainsFunc(onTopic, func(got delivered) bool { return got.DeduplicationID == each.EventID }) {
			t.Fatalf("event %s did not reach the topic under its own identity", each.EventID)
		}
	}
}

// One wallet publishes one event at a time, in the order the commits wrote
// them, and the next one only goes once the one ahead has been confirmed.
func TestRelay_publishesTheEventsOfAWalletInOrderAndOneAtATime(t *testing.T) {
	ctx, stack := stack(t)
	owner := stack.openWallet(ctx, t)
	stack.bet(ctx, t, owner)
	written := stack.rows(ctx, t, owner.WalletID)
	if len(written) != 4 {
		t.Fatalf("events of the wallet = %d, want the 2 of the opening and the 2 of the bet", len(written))
	}

	service := stack.relay(t, publisher(ctx, t))
	for _, expected := range written {
		stack.publishTheOneAhead(ctx, t, service, owner.WalletID, expected.EventID)
	}
	if len(stack.due(ctx, t, owner.WalletID)) != 0 {
		t.Fatalf("the wallet still has candidates, want the outbox of it empty")
	}
}

// Two replicas over the same outbox publish each event once, and a wallet one
// of them is holding does not stop the other from working another.
func TestRelay_publishesEachEventOnceWhenTwoReplicasWorkTheSameOutbox(t *testing.T) {
	ctx, stack := stack(t)
	first, second := stack.openWallet(ctx, t), stack.openWallet(ctx, t)
	stack.bet(ctx, t, first)
	stack.bet(ctx, t, second)

	// Both replicas publish through the same recorder, so an event sent twice
	// is one the two of them both took.
	sent := publisher(ctx, t)
	one, two := stack.relay(t, sent), stack.relay(t, sent)

	held := stack.claimOne(ctx, t, first.WalletID)
	// The wallet the first replica holds is skipped rather than waited on, so
	// the second reaches the other wallet on the very same scan.
	due := stack.due(ctx, t, second.WalletID)
	if len(due) != 1 {
		t.Fatalf("candidates of the other wallet = %d, want one while the first is held", len(due))
	}
	if len(stack.due(ctx, t, first.WalletID)) != 0 {
		t.Fatalf("the held wallet is still a candidate, want it skipped")
	}
	if err := two.Relay(ctx, due[0]); err != nil {
		t.Fatalf("Relay of the other wallet = %v, want nil", err)
	}

	stack.release(ctx, t, held)
	stack.drain(ctx, t, one, first.WalletID)
	stack.drain(ctx, t, two, second.WalletID)
	assertPublishedOnce(t, sent.sent)
}

func assertPublishedOnce(t *testing.T, sent []string) {
	t.Helper()
	seen := map[string]int{}
	for _, each := range sent {
		seen[each]++
	}
	for identity, times := range seen {
		if times != 1 {
			t.Fatalf("event %s was published %d times, want once", identity, times)
		}
	}
}

// The lease expiring with a send in flight hands the row to another replica,
// and what the first one had in flight applies to nothing any more.
func TestRelay_leavesTheRowToAnotherReplicaWhenItsLeaseExpiredMidSend(t *testing.T) {
	ctx, stack := stack(t)
	owner := stack.openWallet(ctx, t)
	first := stack.claimOne(ctx, t, owner.WalletID)
	stack.expire(ctx, t, first.EventID)

	second, err := stack.queue.Claim(ctx, first.EventID, lease)
	if err != nil {
		t.Fatalf("Claim after the lease expired = %v, want nil", err)
	}
	if second.LeaseToken == first.LeaseToken {
		t.Fatalf("token of the second claim = %s, want one other than %s", second.LeaseToken, first.LeaseToken)
	}
	confirmed := stack.queue.Confirm(ctx, first.EventID, first.LeaseToken, timeOf(t))
	if !errors.Is(confirmed, storage.ErrLeaseLost) {
		t.Fatalf("Confirm under the token the lease lost = %v, want ErrLeaseLost", confirmed)
	}
	if stack.rowOf(ctx, t, owner.WalletID, first.EventID.String()).Published {
		t.Fatalf("the row was marked published by the replica that lost it, want it pending")
	}
}

// Ten permanent refusals give up on the row and release the wallet, so the
// events behind it go on. The row stays, with its identity and its payload.
func TestRelay_givesUpOnTheRowAtTheTenthPermanentRefusalAndReleasesTheWallet(t *testing.T) {
	ctx, stack := stack(t)
	owner := stack.openWallet(ctx, t)
	written := stack.rows(ctx, t, owner.WalletID)
	refused := written[0]

	broker := &refusing{permanent: true}
	service := stack.relay(t, broker)
	for range 10 {
		stack.attempt(ctx, t, service, owner.WalletID, refused.EventID)
	}
	assertDead(t, stack.rowOf(ctx, t, owner.WalletID, refused.EventID), refused)
	due := stack.due(ctx, t, owner.WalletID)
	if len(due) != 1 || due[0].EventID.String() != written[1].EventID {
		t.Fatalf("candidates after the death = %v, want the event behind the dead one", due)
	}
}

// The row stays in the database with the identity and the payload of the first
// attempt: it was given up on, not rewritten and not published.
func assertDead(t *testing.T, dead, first row) {
	t.Helper()
	if !dead.Dead || dead.Published {
		t.Fatalf("row after ten refusals: dead = %t and published = %t, want dead and unpublished", dead.Dead, dead.Published)
	}
	if dead.Payload != first.Payload || dead.EventID != first.EventID {
		t.Fatalf("the dead row no longer carries the identity and the bytes of the first attempt")
	}
}

// An operation the database refused leaves nothing of itself: no wallet row
// moved, no transaction, no entry and no event.
func TestSubmit_leavesNoEventBehindWhenTheCommitWasUndone(t *testing.T) {
	ctx, stack := stack(t)
	owner := stack.openWallet(ctx, t)
	first := command(t, owner, wager.KindBet, betAmount)
	if _, err := stack.wagers.Submit(ctx, first); err != nil {
		t.Fatalf("Submit of the bet = %v, want nil", err)
	}
	before := stack.rows(ctx, t, owner.WalletID)

	// The same external identifier under another key is the duplicate the
	// unique index refuses, and it aborts the transaction halfway: the balance
	// write, the row and the events of it are undone together.
	twin := first
	twin.IdempotencyKey = tokenOf(t, identity.ParseIdempotencyKey, "key-"+newID())
	_, err := stack.wagers.Submit(ctx, twin)
	if err == nil {
		t.Fatalf("Submit of the duplicate = nil, want DUPLICATE_EXTERNAL_TRANSACTION")
	}
	after := stack.rows(ctx, t, owner.WalletID)
	if len(after) != len(before) {
		t.Fatalf("events of the wallet = %d, want the %d of before the refused operation", len(after), len(before))
	}
	// Two operations went through — the opening and the bet — and each wrote one
	// transaction and one entry. The refused one added to neither.
	stack.assertUnchanged(ctx, t, owner.WalletID, afterTheBet, 2, 2)
}

func assertTypes(t *testing.T, rows []row, want ...event.Type) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("events of the commit = %d, want %d", len(rows), len(want))
	}
	for index, expected := range want {
		if rows[index].EventType != string(expected) {
			t.Fatalf("event %d = %s, want %s", index, rows[index].EventType, expected)
		}
	}
}
