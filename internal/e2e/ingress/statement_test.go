//go:build integration

// The envelope of the challenge statement, taken as it is written: the type and
// the instant it carries are ignored and not refused, a key in the
// provider:external form is recorded as it arrived, and neither field enters
// the hash, so the same operation sent over HTTP first is a replay on the queue.
package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// providerSecret is the secret of provider-a in the local realm, the documented
// example value of the challenge and not a production credential.
const providerSecret = "provider-a-local"

// statementFormat is the example envelope of the challenge statement. What it
// fixes stays literal; the message, the operation, the key and the wallet are
// fresh per case, in the same form, so two runs never meet on a unique index.
const statementFormat = `{
  "messageId": %q,
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": %q,
    "idempotencyKey": %q,
    "playerId": %q,
    "walletId": %q,
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": {"amount": "25.00", "currency": "BRL"}
  }
}`

func TestIngress_settlesTheEnvelopeOfTheStatementAsWritten(t *testing.T) {
	ctx, at := start(t)
	conn := connect(ctx, t)
	holder := openWallet(ctx, t, at)
	external := "transaction-" + suiteenv.NewID()
	at.queues.send(ctx, t, mappedSender, holder.id, holder.statement(suiteenv.NewID(), external))
	at.queues.awaitEmpty(ctx, t)
	awaitEvents(ctx, t, conn, holder.id, openingEvents+2)

	if found := externalTransaction(ctx, t, conn, holder.id); found.status != "PROCESSED" {
		t.Fatalf("status of the envelope of the statement = %s, want PROCESSED", found.status)
	}
	if got := storedKey(ctx, t, conn, holder.id); got != statementKey(external) {
		t.Fatalf("key recorded = %q, want %q exactly as it arrived", got, statementKey(external))
	}
	if got := at.queues.depth(ctx, t, at.queues.dead); got != 0 {
		t.Fatalf("messages on the dead-letter queue after the envelope of the statement = %d, want 0", got)
	}
}

// The same operation arrives over HTTP first, and then on the queue in the
// envelope of the statement. The type and the instant are outside the hash, so
// the second arrival is the replay of the first, and a key conflict — which
// would also leave one transaction and remove the message — is told apart by the
// series it moves.
func TestIngress_replaysOnTheQueueWhatHTTPRecordedDespiteTheEnvelopeFields(t *testing.T) {
	ctx, at := start(t)
	conn := connect(ctx, t)
	holder := openWallet(ctx, t, at)
	external := "transaction-" + suiteenv.NewID()
	if status, body := holder.submitStatement(ctx, t, at, external); status != http.StatusCreated {
		t.Fatalf("the operation over HTTP = %d, want 201: %s", status, body)
	}
	at.queues.send(ctx, t, mappedSender, holder.id, holder.statement(suiteenv.NewID(), external))
	at.queues.awaitEmpty(ctx, t)
	awaitSeries(ctx, t, at, "wager_duplicates_total", map[string]string{"origin": "sqs", "reason": "replay"}, 1)

	if got := seriesOf(ctx, t, at, "wager_duplicates_total", map[string]string{"origin": "sqs", "reason": "key_conflict"}); got != 0 {
		t.Fatalf("key conflicts on the queue = %v, want 0: the envelope fields are not part of the hash", got)
	}
	if got := countExternal(ctx, t, conn, holder.id); got != 1 {
		t.Fatalf("transactions of the provider after both channels = %d, want 1", got)
	}
	if stored := storedWallet(ctx, t, conn, holder.id); stored.cents != 97500 || stored.version != 2 {
		t.Fatalf("balance and version after both channels = %d and %d, want the 97500 and 2 of one debit", stored.cents, stored.version)
	}
}

// statement is the envelope of the statement for this wallet, under that message
// and that external identifier.
func (o owner) statement(identity, external string) string {
	return fmt.Sprintf(statementFormat, identity, external, statementKey(external), o.player, o.id)
}

// statementKey is the key in the form the example of the statement uses.
func statementKey(external string) string {
	return mappedProvider + ":" + external
}

// submitStatement sends over HTTP the data of that same envelope, with the key in
// the header where HTTP carries it. The body is taken from the envelope itself,
// so the two channels carry the same business by construction.
func (o owner) submitStatement(ctx context.Context, t *testing.T, at suite, external string) (int, []byte) {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(o.statement(suiteenv.NewID(), external)), &envelope); err != nil {
		t.Fatalf("unmarshal the envelope of the statement = %v, want nil", err)
	}
	delete(envelope.Data, "idempotencyKey")
	payload, err := json.Marshal(envelope.Data)
	if err != nil {
		t.Fatalf("marshal the data of the statement = %v, want nil", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, at.base+"/wagering/transactions", strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("submission request = %v, want nil", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenFor(ctx, t, mappedProvider, providerSecret))
	req.Header.Set("Idempotency-Key", statementKey(external))
	return call(t, req)
}
