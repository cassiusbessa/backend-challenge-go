//go:build integration

// The scaffolding of the scenarios: the parameters of a run, a fleet of
// independent instances of the process over the suite database, the pair of
// queues every instance of a case consumes, and the helpers that call an
// instance, release arrivals together, wait for a condition and read what each
// instance counted.
//
// Every wait is bounded by SCENARIO_DEADLINE, through the context of the case,
// and never by a count of turns: a background component is read through the
// outcome it leaves, and that outcome is the only thing there is to wait on.
package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/platform/app"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// The secrets of the local realm. They are the documented example values of the
// challenge, versioned next to the realm import, and not a production credential.
const (
	internalClient = "wallet-internal"
	internalSecret = "wallet-internal-local"
	providerClient = "provider-a"
	providerSecret = "provider-a-local"
)

const wagerRoute = "/wagering/transactions"

// TestMain falls back to the LocalStack credential when it does not come from the
// environment, so the local gate does not depend on a prepared shell. The
// instances read it from the environment of this process, which is the one they
// run in.
func TestMain(m *testing.M) {
	for key, value := range localAWS {
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				panic(err)
			}
		}
	}
	os.Exit(m.Run())
}

var localAWS = map[string]string{
	"AWS_ACCESS_KEY_ID":     "test",
	"AWS_SECRET_ACCESS_KEY": "test",
	"AWS_REGION":            "us-east-1",
}

// scene is what one case runs over: its parameters, the pair of queues every
// instance of it consumes, the configuration every instance starts from, and the
// two tokens it speaks with.
type scene struct {
	params   Params
	queues   *queues
	env      map[string]string
	internal string
	provider string
}

// setUp reads the parameters before anything else, so an invalid one fails the
// case before any instance is up and before any row is written. The context it
// answers carries SCENARIO_DEADLINE, which bounds every wait of the case.
//
// Every case consumes a pair of queues of its own, even the ones that speak only
// HTTP: the application of the Compose consumes the provisioned pair, and an
// instance here consuming it too would be deciding messages nobody sent it.
func setUp(t *testing.T) (context.Context, *scene) {
	t.Helper()
	params, err := Read(os.Getenv)
	if err != nil {
		t.Fatalf("read the parameters = %v, want nil", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), params.Deadline)
	t.Cleanup(cancel)
	pair := createQueues(ctx, t)
	return ctx, &scene{
		params:   params,
		queues:   pair,
		env:      caseEnv(t, pair),
		internal: tokenFor(ctx, t, internalClient, internalSecret),
		provider: tokenFor(ctx, t, providerClient, providerSecret),
	}
}

// caseEnv is the configuration every instance of a case starts from.
func caseEnv(t *testing.T, pair *queues) map[string]string {
	t.Helper()
	return map[string]string{
		// Every instance listens on ports of its own, which is what lets a fleet
		// share one host.
		"HTTP_ADDR":                   "127.0.0.1:0",
		"PPROF_ADDR":                  "127.0.0.1:0",
		"DATABASE_URL":                suiteenv.DatabaseURL(),
		"SQS_ENDPOINT":                sqsEndpoint(),
		"SQS_QUEUE_URL":               pair.ingress,
		"SQS_DLQ_URL":                 pair.dead,
		"QUEUE_SENDERS_PATH":          senderMap(t),
		"SNS_ENDPOINT":                snsEndpoint(),
		"SNS_TOPIC_ARN":               suiteenv.Or("SNS_TOPIC_ARN", "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo"),
		"OTEL_EXPORTER_OTLP_ENDPOINT": suiteenv.Or("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		"IDP_ISSUER":                  issuer(),
		"CLIENTS_PATH":                suiteenv.Or("CLIENTS_PATH", "../../../deploy/local/clients.yaml"),
		// The three windows of the consumer, shortened so a redelivery comes back
		// inside the case, and kept in the order the invariant asks: the
		// invisibility covers the wait of the poll plus the decision.
		"QUEUE_POLL":       "1s",
		"QUEUE_VISIBILITY": "6s",
		"QUEUE_TIMEOUT":    "4s",
		// The relay and the reference worker scan far more often than production,
		// so a case reads an outcome instead of waiting out the default.
		"OUTBOX_INTERVAL":    "50ms",
		"REFERENCE_INTERVAL": "50ms",
	}
}

func sqsEndpoint() string {
	return suiteenv.Or("SQS_ENDPOINT", "http://localhost:4566")
}

func snsEndpoint() string {
	return suiteenv.Or("SNS_ENDPOINT", "http://localhost:4566")
}

// senderMap writes the map of the case: the sender the local broker registers for
// the generic credential, mapped to the provider of the token, so one operation
// can arrive over either channel as the same provider.
func senderMap(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue-senders.yaml")
	content := "senders:\n  \"000000000000\":\n    providers:\n      - " + providerClient + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the sender map = %v, want nil", err)
	}
	return path
}

// instance is one process under test, with its own graph, pool, port and
// background components. It shares nothing in memory with another instance: the
// database and the broker are the only things two of them have in common.
type instance struct {
	base        string
	application *fx.App
	stopped     bool
}

// fleet is the instances of a case. The arrivals of a case are spread over it by
// index, so every instance takes its share of them.
type fleet []*instance

// at is the instance that takes the arrival of that index.
func (f fleet) at(index int) *instance {
	return f[index%len(f)]
}

// launch boots count instances, each over the configuration of the case,
// overridden where the case needs it.
func (s *scene) launch(ctx context.Context, t *testing.T, count int, overrides map[string]string) fleet {
	t.Helper()
	out := make(fleet, 0, count)
	for range count {
		out = append(out, s.boot(ctx, t, overrides))
	}
	return out
}

// boot starts one instance the way the binary does, through app.New, and stops
// it after the case unless the case stopped it first.
func (s *scene) boot(ctx context.Context, t *testing.T, overrides map[string]string) *instance {
	t.Helper()
	env := maps.Clone(s.env)
	maps.Copy(env, overrides)
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("config = %v, want nil", err)
	}
	got := make(chan *httpapi.Server, 1)
	application := app.New(cfg, fx.Invoke(func(srv *httpapi.Server) { got <- srv }))
	starting, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := application.Start(starting); err != nil {
		t.Fatalf("start = %v, want nil", err)
	}
	started := &instance{base: "http://" + (<-got).Addr(), application: application}
	// The cleanup runs after the context of the case is cancelled, so the stop
	// carries the same context without its deadline.
	stopping := context.WithoutCancel(ctx)
	t.Cleanup(func() { started.stop(stopping, t) })
	return started
}

// stop takes the instance down, once: a case stops one in the middle, and the
// cleanup stops whatever is still up.
func (in *instance) stop(ctx context.Context, t *testing.T) {
	t.Helper()
	if in.stopped {
		return
	}
	in.stopped = true
	// Above the shutdown budget of the process, so the lifecycle is what decides
	// how long a stop takes and this bound only catches one that hangs.
	stopping, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := in.application.Stop(stopping); err != nil {
		t.Fatalf("stop = %v, want nil", err)
	}
}

// stop takes every instance of the fleet down, which is the whole process going
// away: nothing any of them held in memory survives it.
func (f fleet) stop(ctx context.Context, t *testing.T) {
	t.Helper()
	for _, each := range f {
		each.stop(ctx, t)
	}
}

// tally sums the samples of the family whose labels include the ones given. A
// sample carries more labels than a case asks about, and each instance counts
// only what it decided itself.
func (in *instance) tally(ctx context.Context, t *testing.T, name string, match map[string]string) float64 {
	t.Helper()
	scraped, err := suiteenv.ScrapeMetrics(ctx, in.base)
	if err != nil {
		t.Fatalf("scrape %s = %v, want nil", in.base, err)
	}
	var total float64
	for _, labels := range scraped.Labels(name) {
		if includes(labels, match) {
			value, _ := scraped.Value(name, labels)
			total += value
		}
	}
	return total
}

func includes(labels, match map[string]string) bool {
	for key, value := range match {
		if labels[key] != value {
			return false
		}
	}
	return true
}

// front puts a proxy with those faculties between the broker at target and the
// instances a case points at it, for the length of the case, and answers the
// proxy and its address.
func front(t *testing.T, target string, faculties Faculties) (*Proxy, string) {
	t.Helper()
	broker, err := url.Parse(target)
	if err != nil {
		t.Fatalf("url.Parse of the broker = %v, want nil", err)
	}
	proxy := NewProxy(broker, faculties)
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	return proxy, server.URL
}

// request is one call to an instance. The key travels in the header, which is
// where the contract puts it over HTTP.
type request struct {
	method  string
	url     string
	bearer  string
	payload string
	key     string
}

// answer is one HTTP response, kept raw so a case reads it as it needs.
type answer struct {
	status int
	body   []byte
}

// outcome is the transaction a success of the wager route answers, and rejection
// is the problem details of a rule that refused. The contract names each field
// of both here and nowhere else in the package.
type outcome struct {
	ID               string        `json:"id"`
	Status           string        `json:"status"`
	ObservedBalance  externalMoney `json:"observedBalance"`
	FailureCode      string        `json:"failureCode"`
	IdempotentReplay bool          `json:"idempotentReplay"`
}

type rejection struct {
	FailureCode      string `json:"failureCode"`
	IdempotentReplay bool   `json:"idempotentReplay"`
}

// reconciliation is what the reconciliation of a wallet answers, in the fields a
// case asks about.
type reconciliation struct {
	StoredBalance externalMoney `json:"storedBalance"`
	LedgerBalance externalMoney `json:"ledgerBalance"`
	Consistent    bool          `json:"consistent"`
}

// externalMoney is money as the client reads it: two strings, never a number.
type externalMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func (a answer) outcome(t *testing.T) outcome {
	t.Helper()
	var read outcome
	if err := json.Unmarshal(a.body, &read); err != nil {
		t.Fatalf("unmarshal the outcome = %v, want nil: %s", err, a.body)
	}
	return read
}

func (a answer) rejection(t *testing.T) rejection {
	t.Helper()
	var read rejection
	if err := json.Unmarshal(a.body, &read); err != nil {
		t.Fatalf("unmarshal the rejection = %v, want nil: %s", err, a.body)
	}
	return read
}

// verdict names an answer of the wager route in one token a case counts and
// compares: the status number, the status of the transaction or the token of
// the refusal, and whether it was a replay.
func (a answer) verdict(t *testing.T) string {
	t.Helper()
	if a.status == http.StatusUnprocessableEntity {
		refused := a.rejection(t)
		return fmt.Sprintf("%d %s replay=%t", a.status, refused.FailureCode, refused.IdempotentReplay)
	}
	settled := a.outcome(t)
	return fmt.Sprintf("%d %s replay=%t", a.status, settled.Status, settled.IdempotentReplay)
}

// exchange sends one request and reads the whole answer. It fails nothing
// itself, so it can run off the goroutine of the test; the deadline of the case,
// carried by ctx, is what bounds it.
func exchange(ctx context.Context, ask request) (answer, error) {
	req, err := http.NewRequestWithContext(ctx, ask.method, ask.url, strings.NewReader(ask.payload))
	if err != nil {
		return answer{}, fmt.Errorf("build %s: %w", ask.url, err)
	}
	ask.headers(req.Header)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return answer{}, fmt.Errorf("call %s: %w", ask.url, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return answer{}, fmt.Errorf("read %s: %w", ask.url, err)
	}
	return answer{status: res.StatusCode, body: body}, nil
}

// headers sets what the request carries besides its body. A field left empty is
// a header the request goes without, which is how a case leaves one out.
func (ask request) headers(into http.Header) {
	if ask.payload != "" {
		into.Set("Content-Type", "application/json")
	}
	if ask.bearer != "" {
		into.Set("Authorization", "Bearer "+ask.bearer)
	}
	if ask.key != "" {
		into.Set("Idempotency-Key", ask.key)
	}
}

func call(ctx context.Context, t *testing.T, ask request) answer {
	t.Helper()
	answered, err := exchange(ctx, ask)
	if err != nil {
		t.Fatalf("exchange = %v, want nil", err)
	}
	return answered
}

// volley is a set of requests released together by one barrier, each answered on
// its own. The goroutines never fail the case — only the goroutine of the test
// can — so each keeps its failure for the case to read.
type volley struct {
	answers  []answer
	failures []error
	done     []chan struct{}
}

// release starts every request, holds them all at the barrier and then lets
// them go at once, and answers without waiting for any of them.
func release(ctx context.Context, asks []request) *volley {
	v := &volley{
		answers:  make([]answer, len(asks)),
		failures: make([]error, len(asks)),
		done:     make([]chan struct{}, len(asks)),
	}
	barrier := make(chan struct{})
	for index, ask := range asks {
		v.done[index] = make(chan struct{})
		go func() {
			defer close(v.done[index])
			<-barrier
			v.answers[index], v.failures[index] = exchange(ctx, ask)
		}()
	}
	close(barrier)
	return v
}

// answered waits for the request of that index and answers it.
func (v *volley) answered(ctx context.Context, t *testing.T, index int) answer {
	t.Helper()
	select {
	case <-v.done[index]:
	case <-ctx.Done():
		t.Fatalf("request %d of the volley did not answer before the deadline of the case", index)
	}
	if v.failures[index] != nil {
		t.Fatalf("request %d of the volley = %v, want an answer", index, v.failures[index])
	}
	return v.answers[index]
}

// waiting reports whether the request of that index has not answered yet.
func (v *volley) waiting(index int) bool {
	select {
	case <-v.done[index]:
		return false
	default:
		return true
	}
}

// all waits for every request and answers them in the order they were asked.
func (v *volley) all(ctx context.Context, t *testing.T) []answer {
	t.Helper()
	for index := range v.done {
		v.answered(ctx, t, index)
	}
	return v.answers
}

// together releases the requests at once and waits for every answer.
func together(ctx context.Context, t *testing.T, asks []request) []answer {
	t.Helper()
	return release(ctx, asks).all(ctx, t)
}

// until polls the condition until it holds, and fails naming what it waited for
// when the deadline of the case comes first.
func until(ctx context.Context, t *testing.T, what string, holds func() bool) {
	t.Helper()
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			t.Fatalf("waited for %s until the deadline of the case, and it did not happen", what)
		}
		if holds() {
			return
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// pollEvery is how often a wait looks. It is well above the interval the
// workers run at, and far below any deadline.
const pollEvery = 50 * time.Millisecond

// owner is a wallet a case operates on, with the player that owns it.
type owner struct {
	id     string
	player string
}

// open opens a wallet through that instance, as the internal client.
func (s *scene) open(ctx context.Context, t *testing.T, at *instance, balance string) owner {
	t.Helper()
	player := suiteenv.NewID()
	payload, err := json.Marshal(map[string]any{
		"playerId":       player,
		"initialBalance": map[string]string{"amount": balance, "currency": Currency},
	})
	if err != nil {
		t.Fatalf("marshal the opening = %v, want nil", err)
	}
	opened := call(ctx, t, request{method: http.MethodPost, url: at.base + "/wallets", bearer: s.internal, payload: string(payload)})
	if opened.status != http.StatusCreated {
		t.Fatalf("opening = %d, want 201: %s", opened.status, opened.body)
	}
	return owner{id: opened.outcome(t).ID, player: player}
}

// operation is the business of one submission. Both channels carry it: it is the
// body over HTTP and the data of the envelope on the queue.
type operation map[string]any

func (o owner) bet(amount string) operation {
	return o.of("BET", amount, nil)
}

// of is an operation of this wallet, every field carrying what a valid one uses
// and the changes replacing what the case is about.
func (o owner) of(kind, amount string, changes map[string]any) operation {
	op := operation{
		"providerId":            providerClient,
		"externalTransactionId": "external-" + suiteenv.NewID(),
		"playerId":              o.player,
		"walletId":              o.id,
		"roundId":               "round-" + suiteenv.NewID(),
		"gameId":                "game-1",
		"kind":                  kind,
		"money":                 map[string]string{"amount": amount, "currency": Currency},
	}
	maps.Copy(op, changes)
	return op
}

// with is a copy of the operation with the given fields replaced, which is how a
// case asks for the same operation with another body.
func (op operation) with(changes map[string]any) operation {
	out := maps.Clone(op)
	maps.Copy(out, changes)
	return out
}

func (op operation) body() string {
	raw, err := json.Marshal(op)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// wager is the submission of that operation under that key to that instance.
func (s *scene) wager(at *instance, key string, op operation) request {
	return request{method: http.MethodPost, url: at.base + wagerRoute, bearer: s.provider, payload: op.body(), key: key}
}

func (s *scene) submit(ctx context.Context, t *testing.T, at *instance, key string, op operation) answer {
	t.Helper()
	return call(ctx, t, s.wager(at, key, op))
}

// newKey is an idempotency key no other arrival has used.
func newKey() string {
	return "key-" + suiteenv.NewID()
}

// tokenFor asks the IdP for a client_credentials token. The service never mints
// one: whoever issues is the IdP.
func tokenFor(ctx context.Context, t *testing.T, clientID, secret string) string {
	t.Helper()
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer()+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("token request = %v, want nil", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("token call = %v, want nil", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d, want 200: the case needs the realm imported", res.StatusCode)
	}
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&issued); err != nil {
		t.Fatalf("decode the token = %v, want nil", err)
	}
	return issued.AccessToken
}

func issuer() string {
	return suiteenv.Or("IDP_ISSUER", suiteenv.Or("KEYCLOAK_BASE_URL", "http://localhost:8080")+"/realms/junglegaming")
}
