package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
	"uuid"
)

// requestTimeout bounds one request. It is far above any latency the load
// measures, and only keeps a replica that stopped answering from holding a
// worker forever.
const requestTimeout = 10 * time.Second

// The opening balance of every wallet the load opens, 10000.00: far above what
// the hot wallets lose in a run, so a rejection for funds is a rare decided
// answer and not the shape of the load.
const openingCents = 1_000_000

// The statuses a decided answer carries.
const (
	statusProcessed = "PROCESSED"
	statusRejected  = "REJECTED"
)

// wallet is one wallet the load opened, and the player it was opened for.
type wallet struct {
	id     string
	player string
}

// operation is one wager the load sends, identified by its key. The body is
// kept byte for byte, so a repetition sends the same one.
type operation struct {
	key    string
	wallet int
	kind   string
	cents  int64
	body   []byte
}

// class is what an arrival told the load about the operation.
type class int

const (
	// decided is an outcome the service recorded: the first conclusion, its
	// replay, or a rejection that wrote its row.
	decided class = iota + 1
	// failed is an arrival whose outcome is not known — a transport failure or
	// a 5xx — and that the resolution settles by its key.
	failed
	// unexpected is any other answer, which is a defect of the load or of the
	// service, and fails the run.
	unexpected
)

// answer is one arrival, classified.
type answer struct {
	class       class
	status      string
	replay      bool
	failureCode string
	detail      string
}

// load is one run: the wallets, the operations and every arrival counted.
type load struct {
	opts     options
	run      string
	internal *tokenSource
	provider *tokenSource
	api      *http.Client
	wallets  []wallet

	mu         sync.Mutex
	operations []*operation
	decisions  map[string]answer
	surprised  map[string]bool
	latencies  []time.Duration
	sent       int
	errors     int
	replays    int
	rejections map[string]int
	unexpected int
	findings   []string
}

func newLoad(o options) *load {
	return &load{
		opts:       o,
		run:        uuid.NewV4().String()[:8],
		internal:   newTokenSource(o, o.internalClient, o.internalSecret),
		provider:   newTokenSource(o, o.providerClient, o.providerSecret),
		api:        &http.Client{Timeout: requestTimeout},
		decisions:  map[string]answer{},
		surprised:  map[string]bool{},
		rejections: map[string]int{},
	}
}

// open opens the wallets of the run, each for a new player. The identities are
// random and not drawn from the seed: a second run with the same seed would
// otherwise open the same player again and be refused.
func (l *load) open(ctx context.Context) error {
	for range l.opts.wallets {
		player := uuid.NewV4().String()
		var opened struct {
			ID string `json:"id"`
		}
		body := fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":%q,"currency":"BRL"}}`, player, amountOf(openingCents))
		code, err := l.call(ctx, l.internal, http.MethodPost, "/wallets", []byte(body), &opened)
		if err != nil {
			return fmt.Errorf("open a wallet: %w", err)
		}
		if code != http.StatusCreated || opened.ID == "" {
			return fmt.Errorf("open a wallet: answered %d, want 201 with the wallet", code)
		}
		l.wallets = append(l.wallets, wallet{id: opened.ID, player: player})
	}
	return nil
}

// call sends one request of the internal client and decodes a 2xx body into
// out, answering the status.
func (l *load) call(ctx context.Context, tokens *tokenSource, method, path string, body []byte, out any) (int, error) {
	token, err := tokens.Token(ctx)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, method, l.opts.address+path, bytes.NewReader(body)) //nolint:gosec // the operator names the address
	if err != nil {
		return 0, fmt.Errorf("build %s %s: %w", method, path, err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := l.api.Do(request) //nolint:gosec // the operator names the address
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return response.StatusCode, nil
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return response.StatusCode, fmt.Errorf("read %s %s: %w", method, path, err)
	}
	return response.StatusCode, nil
}

// send runs the workers until the window closes. A worker does not start an
// arrival after it, and finishes the one in flight.
func (l *load) send(ctx context.Context, until time.Time) {
	var workers sync.WaitGroup
	for worker := range l.opts.concurrency {
		workers.Go(func() { l.work(ctx, worker, until) })
	}
	workers.Wait()
}

// work is one worker: its own mix, its own history, and its own connection. One
// idle connection per worker is what spreads the connections, and with them the
// arrivals, over the replicas behind a balancer that balances by connection.
func (l *load) work(ctx context.Context, worker int, until time.Time) {
	client := &http.Client{Timeout: requestTimeout, Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        1,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     90 * time.Second,
	}}
	defer client.CloseIdleConnections()
	mix := newMixer(l.opts.seed, worker, len(l.wallets))
	var history []*operation
	for sequence := 0; ctx.Err() == nil && time.Now().Before(until); sequence++ {
		op, fresh := l.nextOperation(mix, history, fmt.Sprintf("%s-%d-%d", l.run, worker, sequence))
		if fresh {
			history = append(history, op)
		}
		started := time.Now()
		got := l.arrive(ctx, client, op)
		l.record(op, got, time.Since(started))
	}
}

// nextOperation answers an earlier operation of this worker to send again, or a
// new one under the identity given, and reports whether it is new.
func (l *load) nextOperation(mix *mixer, history []*operation, id string) (*operation, bool) {
	if len(history) > 0 && mix.repeats() {
		return history[mix.pick(len(history))], false
	}
	op := l.operation(mix.next(), id)
	l.mu.Lock()
	l.operations = append(l.operations, op)
	l.mu.Unlock()
	return op, true
}

// operation builds the body of one wager on the wallet drawn. The external
// identity, the round and the key all derive from the identity of the arrival,
// which is unique in the run and carries the run.
func (l *load) operation(chosen draw, id string) *operation {
	target := l.wallets[chosen.wallet]
	body, _ := json.Marshal(struct {
		ProviderID            string `json:"providerId"`
		ExternalTransactionID string `json:"externalTransactionId"`
		PlayerID              string `json:"playerId"`
		WalletID              string `json:"walletId"`
		RoundID               string `json:"roundId"`
		GameID                string `json:"gameId"`
		Kind                  string `json:"kind"`
		Money                 struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		} `json:"money"`
	}{
		ProviderID:            l.opts.providerClient,
		ExternalTransactionID: "ext-" + id,
		PlayerID:              target.player,
		WalletID:              target.id,
		RoundID:               "round-" + id,
		GameID:                "load",
		Kind:                  chosen.kind,
		Money: struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		}{Amount: amountOf(chosen.cents), Currency: "BRL"},
	})
	return &operation{key: "key-" + id, wallet: chosen.wallet, kind: chosen.kind, cents: chosen.cents, body: body}
}

// arrive sends the operation once and classifies what came back. The body is a
// bytes reader, so the transport can send it again on a fresh connection when
// the replica closed the idle one: a request carrying Idempotency-Key is one it
// treats as replayable.
func (l *load) arrive(ctx context.Context, client *http.Client, op *operation) answer {
	token, err := l.provider.Token(ctx)
	if err != nil {
		return answer{class: failed, detail: err.Error()}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, l.opts.address+"/wagering/transactions", bytes.NewReader(op.body)) //nolint:gosec // the operator names the address
	if err != nil {
		return answer{class: unexpected, detail: err.Error()}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", op.key)
	response, err := client.Do(request) //nolint:gosec // the operator names the address
	if err != nil {
		return classify(0, nil, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return classify(response.StatusCode, body, err)
}

// classify reads one answer of the wager route.
//
// A 201 is the first conclusion and a 200 its replay; a 422 is decided only
// when it names the row it recorded, which is how the contract tells a durable
// rejection from a refusal that wrote nothing — and among those are the two
// conflicts of idempotency, which the load never provokes on purpose.
func classify(code int, body []byte, err error) answer {
	if err != nil {
		return answer{class: failed, detail: err.Error()}
	}
	if code >= http.StatusInternalServerError {
		return answer{class: failed, detail: fmt.Sprintf("answered %d", code)}
	}
	var got struct {
		Status           string `json:"status"`
		IdempotentReplay bool   `json:"idempotentReplay"`
		FailureCode      string `json:"failureCode"`
		TransactionID    string `json:"transactionId"`
	}
	surprise := answer{class: unexpected, detail: fmt.Sprintf("answered %d: %.200s", code, body)}
	if json.Unmarshal(body, &got) != nil {
		return surprise
	}
	switch {
	case (code == http.StatusCreated || code == http.StatusOK) && got.Status == statusProcessed:
		return answer{class: decided, status: statusProcessed, replay: got.IdempotentReplay}
	case code == http.StatusUnprocessableEntity && got.TransactionID != "":
		return answer{class: decided, status: statusRejected, replay: got.IdempotentReplay, failureCode: got.FailureCode}
	}
	return surprise
}

// record counts one arrival of the window.
func (l *load) record(op *operation, got answer, took time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent++
	switch got.class {
	case failed:
		l.errors++
	case unexpected:
		l.surprise(op, got)
	case decided:
		l.latencies = append(l.latencies, took)
		l.decide(op, got)
	}
}

// decide keeps the first decided answer of a key, and holds every later one to
// it: the same key answering another status, or concluding twice, is the
// duplication the verdict exists to catch, named where it happened.
func (l *load) decide(op *operation, got answer) {
	if got.replay {
		l.replays++
	}
	first, known := l.decisions[op.key]
	if !known {
		l.decisions[op.key] = got
		if got.status == statusRejected {
			l.rejections[got.failureCode]++
		}
		return
	}
	if first.status != got.status {
		l.findings = append(l.findings, fmt.Sprintf("key %s answered %s and then %s", op.key, first.status, got.status))
	}
	if !got.replay {
		l.findings = append(l.findings, fmt.Sprintf("key %s was concluded again instead of replayed", op.key))
	}
}

// surprise counts an unexpected answer and names the first few. One is enough
// to fail the run, and a broken credential would otherwise name every arrival.
func (l *load) surprise(op *operation, got answer) {
	l.surprised[op.key] = true
	l.unexpected++
	if l.unexpected <= maxSurprises {
		l.findings = append(l.findings, fmt.Sprintf("key %s %s", op.key, got.detail))
	}
}

// maxSurprises is how many unexpected answers are named before the rest are only
// counted as failing the run.
const maxSurprises = 10

// amountOf writes cents as the decimal string of two places the contract takes.
func amountOf(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}
