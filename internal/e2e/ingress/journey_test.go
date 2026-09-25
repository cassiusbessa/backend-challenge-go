//go:build integration

// The journey of the ingress queue against the real stack: what one message
// commits, what a redelivery changes, and the four gates to the dead-letter queue.
package ingress

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// openingEvents is what the birth of the wallet leaves in the outbox: the outcome
// of the opening and the movement of its balance. A case counts from there, because
// the events it is about are the ones after those two.
const openingEvents = 2

func TestIngress_settlesABetThatArrivedOnTheQueue(t *testing.T) {
	ctx, at := start(t)
	conn := connect(ctx, t)
	holder := openWallet(ctx, t, at)
	identity := newID()
	at.queues.send(ctx, t, mappedSender, holder.id, holder.bet(identity, "25.00", nil))

	at.queues.awaitEmpty(ctx, t)
	recorded := awaitEvents(ctx, t, conn, holder.id, openingEvents+2)

	assertSettled(ctx, t, conn, holder, identity)
	assertCausedBy(t, recorded, identity)
}

func assertSettled(ctx context.Context, t *testing.T, conn *pgx.Conn, holder owner, identity string) {
	t.Helper()
	if stored := storedWallet(ctx, t, conn, holder.id); stored.cents != 97500 || stored.version != 2 {
		t.Fatalf("balance and version = %d and %d, want 97500 and 2", stored.cents, stored.version)
	}
	// Two entries: the credit of the birth and the debit of the bet.
	if got := countEntries(ctx, t, conn, holder.id); got != 2 {
		t.Fatalf("ledger entries = %d, want 2", got)
	}
	if got := countInbox(ctx, t, conn, identity); got != 1 {
		t.Fatalf("inbox rows = %d, want 1", got)
	}
}

// assertCausedBy pins the cause of each commit: the events the message caused name
// it, and the two of the opening — which no message caused — name nothing.
func assertCausedBy(t *testing.T, recorded []event, identity string) {
	t.Helper()
	for _, each := range recorded[openingEvents:] {
		if each.causationID != identity {
			t.Fatalf("causationId of %s = %q, want the message %s", each.eventType, each.causationID, identity)
		}
	}
	for _, each := range recorded[:openingEvents] {
		if each.causationID != "" {
			t.Fatalf("causationId of the opening %s = %q, want none", each.eventType, each.causationID)
		}
	}
}

// The same identifier with the same body is recognised by the row already there. It
// reapplies nothing and the message still leaves the queue.
func TestIngress_changesNothingWhenTheSameMessageIsRedelivered(t *testing.T) {
	ctx, at := start(t)
	conn := connect(ctx, t)
	holder := openWallet(ctx, t, at)
	identity := newID()
	body := holder.bet(identity, "25.00", nil)
	at.queues.send(ctx, t, mappedSender, holder.id, body)
	at.queues.awaitEmpty(ctx, t)
	awaitEvents(ctx, t, conn, holder.id, openingEvents+2)
	settled := storedWallet(ctx, t, conn, holder.id)

	at.queues.send(ctx, t, mappedSender, holder.id, body)
	at.queues.awaitEmpty(ctx, t)

	if again := storedWallet(ctx, t, conn, holder.id); again != settled {
		t.Fatalf("balance and version = %d and %d, want the %d and %d of the first delivery", again.cents, again.version, settled.cents, settled.version)
	}
	if got := countEntries(ctx, t, conn, holder.id); got != 2 {
		t.Fatalf("ledger entries = %d, want the 2 of the first delivery", got)
	}
	if got := countExternal(ctx, t, conn, holder.id); got != 1 {
		t.Fatalf("transactions of the provider = %d, want 1", got)
	}
	if got := countInbox(ctx, t, conn, identity); got != 1 {
		t.Fatalf("inbox rows = %d, want 1", got)
	}
	if got := at.queues.depth(ctx, t, at.queues.dead); got != 0 {
		t.Fatalf("messages on the dead-letter queue = %d, want 0", got)
	}
}

// The body declares a provider the sender is not mapped to. What the body claims
// authorizes nothing, so it is checked against the list of that sender and refused
// before any wallet is reached.
//
// This is the gate that depends least on the environment, which is why it is the
// one written first.
func TestIngress_abandonsAMessageWhoseBodyDeclaresAnotherProvider(t *testing.T) {
	ctx, at := start(t)
	conn := connect(ctx, t)
	holder := openWallet(ctx, t, at)
	identity := newID()
	body := holder.bet(identity, "25.00", map[string]any{"providerId": unmappedProvider})
	at.queues.send(ctx, t, mappedSender, holder.id, body)

	at.queues.awaitDeadLetter(ctx, t)
	at.queues.awaitEmpty(ctx, t)
	assertUntouched(ctx, t, conn, holder, identity)
}

// The sender is one the map does not name, which is also what a map configured with
// the wrong identity looks like for every legitimate message.
func TestIngress_abandonsAMessageFromASenderTheMapDoesNotName(t *testing.T) {
	ctx, at := start(t)
	conn := connect(ctx, t)
	holder := openWallet(ctx, t, at)
	identity := newID()
	at.queues.send(ctx, t, unmappedSender, holder.id, holder.bet(identity, "25.00", nil))

	at.queues.awaitDeadLetter(ctx, t)
	at.queues.awaitEmpty(ctx, t)
	assertUntouched(ctx, t, conn, holder, identity)
}

func TestIngress_abandonsABodyItCouldNotRead(t *testing.T) {
	ctx, at := start(t)
	conn := connect(ctx, t)
	holder := openWallet(ctx, t, at)
	identity := newID()
	broken := holder.bet(identity, "25.001", nil)
	at.queues.send(ctx, t, mappedSender, holder.id, broken)

	at.queues.awaitDeadLetter(ctx, t)
	at.queues.awaitEmpty(ctx, t)
	assertUntouched(ctx, t, conn, holder, identity)
}

// The same identifier with another body is not a redelivery of anything: the row
// already there answers with a different hash, and nothing new is written.
func TestIngress_abandonsARecordedIdentifierThatArrivesWithAnotherBody(t *testing.T) {
	ctx, at := start(t)
	conn := connect(ctx, t)
	holder := openWallet(ctx, t, at)
	identity := newID()
	at.queues.send(ctx, t, mappedSender, holder.id, holder.bet(identity, "25.00", nil))
	at.queues.awaitEmpty(ctx, t)
	awaitEvents(ctx, t, conn, holder.id, openingEvents+2)
	settled := storedWallet(ctx, t, conn, holder.id)

	at.queues.send(ctx, t, mappedSender, holder.id, holder.bet(identity, "40.00", nil))
	at.queues.awaitDeadLetter(ctx, t)
	at.queues.awaitEmpty(ctx, t)

	if again := storedWallet(ctx, t, conn, holder.id); again != settled {
		t.Fatalf("balance and version = %d and %d, want the %d and %d of the first delivery", again.cents, again.version, settled.cents, settled.version)
	}
	if got := countExternal(ctx, t, conn, holder.id); got != 1 {
		t.Fatalf("transactions of the provider = %d, want the 1 of the first delivery", got)
	}
	if got := countInbox(ctx, t, conn, identity); got != 1 {
		t.Fatalf("inbox rows = %d, want the 1 of the first delivery", got)
	}
}

// A wait recorded is a durable decision, so the message leaves the queue and the row
// stays for the reference worker to close. The deferred commit carries no cause: what
// fires it is the deadline, not the message.
func TestIngress_recordsTheWaitAndLeavesTheDeferredCommitWithNoCause(t *testing.T) {
	// The TTL is shortened so the worker closes the wait inside the case instead of
	// fifteen minutes from now.
	ctx, at := startWith(t, map[string]string{"REFERENCE_TTL": "2s"})
	conn := connect(ctx, t)
	holder := openWallet(ctx, t, at)
	identity := newID()
	waiting := holder.bet(identity, "25.00", map[string]any{
		"kind":                           "REFUND",
		"referenceExternalTransactionId": "external-" + newID(),
	})
	at.queues.send(ctx, t, mappedSender, holder.id, waiting)

	at.queues.awaitEmpty(ctx, t)
	recorded := awaitEvents(ctx, t, conn, holder.id, openingEvents+1)
	if got := recorded[openingEvents].causationID; got != identity {
		t.Fatalf("causationId of the recorded wait = %q, want the message %s", got, identity)
	}
	pending := externalTransaction(ctx, t, conn, holder.id)
	if pending.status != "PENDING_REFERENCE" {
		t.Fatalf("status = %s, want PENDING_REFERENCE", pending.status)
	}

	// The clock closes the wait. The commit that does it is the worker's, not the
	// message's, and the events of it name no cause.
	until(t, "the reference worker to close the wait", func() bool {
		return externalTransaction(ctx, t, conn, holder.id).status == "REJECTED"
	})
	deferred := awaitEvents(ctx, t, conn, holder.id, openingEvents+2)
	if got := deferred[openingEvents+1].causationID; got != "" {
		t.Fatalf("causationId of the deferred commit = %q, want none", got)
	}
	if got := deferred[openingEvents+1].correlationID; got != pending.id {
		t.Fatalf("correlationId of the deferred commit = %q, want the transaction %s", got, pending.id)
	}
}

// assertUntouched pins what every gate to the dead-letter queue has in common: no
// financial row of any kind, and no memory of the message either.
func assertUntouched(ctx context.Context, t *testing.T, conn *pgx.Conn, holder owner, identity string) {
	t.Helper()
	if stored := storedWallet(ctx, t, conn, holder.id); stored.cents != 100000 || stored.version != 1 {
		t.Fatalf("balance and version = %d and %d, want the untouched 100000 and 1", stored.cents, stored.version)
	}
	if got := countExternal(ctx, t, conn, holder.id); got != 0 {
		t.Fatalf("transactions of the provider = %d, want 0", got)
	}
	// One entry, the credit of the birth of the wallet.
	if got := countEntries(ctx, t, conn, holder.id); got != 1 {
		t.Fatalf("ledger entries = %d, want the 1 of the opening", got)
	}
	if got := countInbox(ctx, t, conn, identity); got != 0 {
		t.Fatalf("inbox rows = %d, want 0", got)
	}
}
