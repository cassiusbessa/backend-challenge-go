package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"
)

// fakeService is the settlement as the load sees it, in memory and behind one
// address: the identity provider, the wallet routes, the wager route with its
// idempotency, and the reconciliation. Each case bends one part of it.
type fakeService struct {
	mu       sync.Mutex
	wallets  map[string]*fakeWallet
	keys     map[string]fakeOutcome
	remotes  map[string]bool
	arrivals int
	tokens   int
	lifespan int

	// wager answers the wager route in place of the settlement when it reports
	// true: the case about an answer the load does not expect.
	wager func(w http.ResponseWriter, r *http.Request, arrival int) bool
	// drift is added to the balance a read answers, to fake a lost movement.
	drift int64
	// metrics answers the query of the metric backend, by expression.
	metrics func(query string, at time.Time) (int, string)
}

type fakeWallet struct {
	player  string
	cents   int64
	version int64
	entries int64
}

type fakeOutcome struct {
	body   string
	status string
	id     string
}

func newFakeService(t *testing.T) (*fakeService, *httptest.Server) {
	t.Helper()
	fake := &fakeService{wallets: map[string]*fakeWallet{}, keys: map[string]fakeOutcome{}, remotes: map[string]bool{}, lifespan: 300}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /realms/{realm}/protocol/openid-connect/token", fake.token)
	mux.HandleFunc("POST /wallets", fake.open)
	mux.HandleFunc("GET /wallets/{id}", fake.read)
	mux.HandleFunc("POST /wallets/{id}/reconciliation", fake.reconcile)
	mux.HandleFunc("POST /wagering/transactions", fake.settle)
	mux.HandleFunc("GET /api/v1/query", fake.query)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return fake, server
}

// optionsFor answers options aimed at the fake, short enough for a case.
func optionsFor(t *testing.T, server *httptest.Server) options {
	t.Helper()
	return options{
		target: "compose", address: server.URL, replicaLabel: "instance", root: t.TempDir(),
		idp: server.URL, realm: "junglegaming", prometheus: server.URL,
		internalClient: "wallet-internal", internalSecret: "wallet-internal-local",
		providerClient: "provider-a", providerSecret: "provider-a-local",
		replicas: 1, duration: 150 * time.Millisecond, concurrency: 4, wallets: 6, seed: 7,
		report: t.TempDir() + "/report.json",
		settle: time.Millisecond, drainWait: time.Second, resolveWait: time.Second,
		commands: func(context.Context, []string) (string, error) { return "", fmt.Errorf("no command in this case") },
	}
}

func (f *fakeService) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.tokens++
	issued, lifespan := f.tokens, f.lifespan
	f.mu.Unlock()
	if r.FormValue("grant_type") != "client_credentials" || r.FormValue("client_secret") == "" {
		http.Error(w, "unsupported grant", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": fmt.Sprintf("token-%d", issued), "expires_in": lifespan})
}

func (f *fakeService) open(w http.ResponseWriter, r *http.Request) {
	var asked struct {
		PlayerID       string `json:"playerId"`
		InitialBalance struct {
			Amount string `json:"amount"`
		} `json:"initialBalance"`
	}
	_ = json.NewDecoder(r.Body).Decode(&asked)
	cents, _ := centsOf(asked.InitialBalance.Amount)
	id := uuid.NewV4().String()
	f.mu.Lock()
	f.wallets[id] = &fakeWallet{player: asked.PlayerID, cents: cents, version: 1, entries: 1}
	f.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "playerId": asked.PlayerID, "version": 1})
}

func (f *fakeService) read(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	held, ok := f.wallets[r.PathValue("id")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": r.PathValue("id"), "version": held.version,
		"balance": map[string]string{"amount": amountOf(held.cents + f.drift), "currency": "BRL"},
	})
}

func (f *fakeService) reconcile(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	held, ok := f.wallets[r.PathValue("id")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"consistent": true, "checkedEntries": held.entries})
}

// settle is the wager route: a known key with the same body is a replay, and a
// new one moves the wallet the way the settlement does.
func (f *fakeService) settle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.arrivals++
	arrival := f.arrivals
	f.remotes[r.RemoteAddr] = true
	f.mu.Unlock()
	if f.wager != nil && f.wager(w, r, arrival) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Header.Get("Idempotency-Key")
	if recorded, ok := f.keys[key]; ok {
		answerOutcome(w, recorded, true)
		return
	}
	outcome := f.apply(body)
	f.keys[key] = outcome
	answerOutcome(w, outcome, false)
}

func (f *fakeService) apply(body []byte) fakeOutcome {
	var asked struct {
		WalletID string `json:"walletId"`
		Kind     string `json:"kind"`
		Money    struct {
			Amount string `json:"amount"`
		} `json:"money"`
	}
	_ = json.Unmarshal(body, &asked)
	cents, _ := centsOf(asked.Money.Amount)
	held := f.wallets[asked.WalletID]
	outcome := fakeOutcome{body: string(body), status: statusProcessed, id: uuid.NewV4().String()}
	switch {
	case asked.Kind == kindBet && cents > held.cents:
		outcome.status = statusRejected
	case asked.Kind == kindBet:
		held.cents, held.version, held.entries = held.cents-cents, held.version+1, held.entries+1
	case asked.Kind == kindWin:
		held.cents, held.version, held.entries = held.cents+cents, held.version+1, held.entries+1
	}
	return outcome
}

func answerOutcome(w http.ResponseWriter, outcome fakeOutcome, replay bool) {
	if outcome.status == statusRejected {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"status": 422, "failureCode": "INSUFFICIENT_FUNDS", "transactionId": outcome.id, "idempotentReplay": replay})
		return
	}
	code := http.StatusCreated
	if replay {
		code = http.StatusOK
	}
	writeJSON(w, code, map[string]any{"transactionId": outcome.id, "status": outcome.status, "idempotentReplay": replay})
}

// query is the metric backend. With no answer set, it answers an empty vector,
// which is what the backend answers for a series it does not hold.
func (f *fakeService) query(w http.ResponseWriter, r *http.Request) {
	at, _ := strconv.ParseFloat(r.URL.Query().Get("time"), 64)
	when := time.UnixMilli(int64(at * 1000))
	if f.metrics == nil {
		writePlain(w, http.StatusOK, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
		return
	}
	code, body := f.metrics(r.URL.Query().Get("query"), when)
	writePlain(w, code, body)
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writePlain(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

// centsOf reads a two-place decimal the way the fake needs it: the load only
// ever writes that form.
func centsOf(amount string) (int64, error) {
	whole, fraction, _ := strings.Cut(amount, ".")
	units, err := strconv.ParseInt(whole+fraction, 10, 64)
	return units, err
}
