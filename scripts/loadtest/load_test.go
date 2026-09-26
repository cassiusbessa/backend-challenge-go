package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Each answer of the wager route falls in one class: decided, failed and left
// to the resolution, or unexpected and failing the run.
func TestClassify_readsEachAnswerOfTheWagerRoute(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		code   int
		body   string
		err    error
		class  class
		status string
		replay bool
	}{
		{name: "the first conclusion", code: 201, body: `{"status":"PROCESSED","idempotentReplay":false}`, class: decided, status: statusProcessed},
		{name: "its replay", code: 200, body: `{"status":"PROCESSED","idempotentReplay":true}`, class: decided, status: statusProcessed, replay: true},
		{name: "a durable rejection", code: 422, body: `{"failureCode":"INSUFFICIENT_FUNDS","transactionId":"t-1"}`, class: decided, status: statusRejected},
		{name: "the replay of a durable rejection", code: 422, body: `{"failureCode":"INSUFFICIENT_FUNDS","transactionId":"t-1","idempotentReplay":true}`, class: decided, status: statusRejected, replay: true},
		{name: "a key reused with another body", code: 422, body: `{"failureCode":"IDEMPOTENCY_CONFLICT"}`, class: unexpected},
		{name: "an external identity under another key", code: 422, body: `{"failureCode":"DUPLICATE_EXTERNAL_TRANSACTION"}`, class: unexpected},
		{name: "a wait for a reference", code: 202, body: `{"status":"PENDING_REFERENCE"}`, class: unexpected},
		{name: "invalid input", code: 400, body: `{"title":"Request is not valid"}`, class: unexpected},
		{name: "an expired credential", code: 401, body: `{}`, class: unexpected},
		{name: "a refused permission", code: 403, body: `{}`, class: unexpected},
		{name: "a conclusion without a body", code: 201, body: `not json`, class: unexpected},
		{name: "a replica unavailable", code: 503, body: `{}`, class: failed},
		{name: "a defect of the service", code: 500, body: ``, class: failed},
		{name: "a connection reset", err: errors.New("connection reset by peer"), class: failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classify(tc.code, []byte(tc.body), tc.err)
			if got.class != tc.class || got.status != tc.status || got.replay != tc.replay {
				t.Fatalf("classify(%s) = class %d %s replay %t, want class %d %s replay %t", tc.name, got.class, got.status, got.replay, tc.class, tc.status, tc.replay)
			}
		})
	}
}

// A repetition is an earlier operation of the worker, sent with the same key
// and the same body, and it is not registered again.
func TestNextOperation_sendsAnEarlierOperationAgainWithItsKeyAndBody(t *testing.T) {
	t.Parallel()
	l := newLoad(options{providerClient: "provider-a"})
	l.wallets = []wallet{{id: "w-0", player: "p-0"}, {id: "w-1", player: "p-1"}}
	mix := newMixer(1, 0, len(l.wallets))
	var history []*operation
	for sequence := 0; sequence < 10_000; sequence++ {
		op, fresh := l.nextOperation(mix, history, "id-"+string(rune('a'+sequence%26)))
		if fresh {
			history = append(history, op)
			continue
		}
		if !containsOperation(history, op) {
			t.Fatalf("repeated operation %s is not one the worker sent before", op.key)
		}
		if len(l.operations) != len(history) {
			t.Fatalf("operations registered = %d after a repetition, want the %d new ones", len(l.operations), len(history))
		}
		return
	}
	t.Fatalf("no repetition in 10000 arrivals, want one in twenty")
}

func containsOperation(history []*operation, op *operation) bool {
	for _, earlier := range history {
		if earlier.key == op.key && string(earlier.body) == string(op.body) {
			return true
		}
	}
	return false
}

// The body names the wallet and the player it was opened for, the provider of
// the token, and the amount as a two-place decimal string.
func TestOperation_writesTheBodyOfTheContract(t *testing.T) {
	t.Parallel()
	l := newLoad(options{providerClient: "provider-a"})
	l.wallets = []wallet{{id: "w-0", player: "p-0"}}
	op := l.operation(draw{wallet: 0, kind: kindBet, cents: 325}, "run-1-2")
	var body map[string]any
	if err := json.Unmarshal(op.body, &body); err != nil {
		t.Fatalf("body of the operation = %v, want JSON", err)
	}
	money, _ := body["money"].(map[string]any)
	if body["walletId"] != "w-0" || body["playerId"] != "p-0" || body["providerId"] != "provider-a" || body["kind"] != kindBet || money["amount"] != "3.25" {
		t.Fatalf("body = %s, want w-0 of p-0 by provider-a, a BET of 3.25", op.body)
	}
	if op.key != "key-run-1-2" || body["externalTransactionId"] != "ext-run-1-2" {
		t.Fatalf("key %s and external identity %v, want both from the arrival run-1-2", op.key, body["externalTransactionId"])
	}
}

// The first decided answer of a key is the one counted; a later one that
// disagrees with it, or concludes the key again, is a finding.
func TestDecide_holdsEveryLaterAnswerOfAKeyToTheFirst(t *testing.T) {
	t.Parallel()
	l := newLoad(options{})
	op := &operation{key: "key-1"}
	l.decide(op, answer{class: decided, status: statusRejected, failureCode: "INSUFFICIENT_FUNDS"})
	l.decide(op, answer{class: decided, status: statusRejected, failureCode: "INSUFFICIENT_FUNDS", replay: true})
	if l.rejections["INSUFFICIENT_FUNDS"] != 1 || l.replays != 1 || len(l.findings) != 0 {
		t.Fatalf("after a rejection and its replay = %v rejections, %d replays, findings %v; want 1, 1 and none", l.rejections, l.replays, l.findings)
	}
	l.decide(op, answer{class: decided, status: statusProcessed})
	if len(l.findings) != 2 || !strings.Contains(l.findings[0], "REJECTED and then PROCESSED") || !strings.Contains(l.findings[1], "concluded again") {
		t.Fatalf("findings after a second conclusion = %v, want the status change and the conclusion named", l.findings)
	}
}

// The wallets are opened for a new player each, at the opening balance.
func TestOpen_opensEachWalletForANewPlayer(t *testing.T) {
	t.Parallel()
	fake, server := newFakeService(t)
	l := newLoad(optionsFor(t, server))
	if err := l.open(context.Background()); err != nil {
		t.Fatalf("open = %v, want nil", err)
	}
	players := map[string]bool{}
	for _, opened := range l.wallets {
		players[opened.player] = true
		if held := fake.wallets[opened.id]; held == nil || held.cents != openingCents {
			t.Fatalf("wallet %s opened = %+v, want it at %d cents", opened.id, held, openingCents)
		}
	}
	if len(players) != l.opts.wallets {
		t.Fatalf("players of %d wallets = %d, want one each", l.opts.wallets, len(players))
	}
}

// A wallet the service will not open stops the run before anything is sent.
func TestOpen_failsWhenTheServiceRefusesTheWallet(t *testing.T) {
	t.Parallel()
	_, server := newFakeService(t)
	o := optionsFor(t, server)
	o.internalSecret = ""
	if err := newLoad(o).open(context.Background()); err == nil || !strings.Contains(err.Error(), "wallet-internal") {
		t.Fatalf("open with a credential the provider refuses = %v, want a failure naming the client", err)
	}
}

// Each worker keeps one connection of its own, so the arrivals of a window
// reach the service over as many connections as there are workers.
func TestSend_keepsOneConnectionPerWorker(t *testing.T) {
	t.Parallel()
	fake, server := newFakeService(t)
	l := newLoad(optionsFor(t, server))
	if err := l.open(context.Background()); err != nil {
		t.Fatalf("open = %v, want nil", err)
	}
	l.send(context.Background(), time.Now().Add(200*time.Millisecond))
	if len(fake.remotes) != l.opts.concurrency {
		t.Fatalf("connections of %d workers = %d, want one each", l.opts.concurrency, len(fake.remotes))
	}
	if l.sent == 0 || l.sent != fake.arrivals {
		t.Fatalf("arrivals sent = %d and received = %d, want the same and above zero", l.sent, fake.arrivals)
	}
}

// An answer the load does not expect fails the run, naming the key.
func TestRun_failsOnAnAnswerItDoesNotExpect(t *testing.T) {
	t.Parallel()
	fake, server := newFakeService(t)
	fake.wager = func(w http.ResponseWriter, _ *http.Request, arrival int) bool {
		if arrival != 3 {
			return false
		}
		writeJSON(w, http.StatusConflict, map[string]string{"title": "conflict"})
		return true
	}
	err := run(context.Background(), optionsFor(t, server), &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "answered 409") || !strings.Contains(err.Error(), "key key-") {
		t.Fatalf("run with a 409 = %v, want the verdict to fail naming the key and the answer", err)
	}
}
