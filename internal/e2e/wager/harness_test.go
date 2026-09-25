//go:build integration

package wager

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/platform/app"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

// The secrets of the local realm. They are the documented example values of the
// challenge, versioned next to the realm import, and not a production credential.
const (
	internalClient = "wallet-internal"
	internalSecret = "wallet-internal-local"
	providerClient = "provider-a"
	providerSecret = "provider-a-local"
	otherClient    = "provider-b"
	otherSecret    = "provider-b-local"
)

const wagerRoute = "/wagering/transactions"

// TestMain falls back to the LocalStack credential when it does not come from the
// environment, so the local gate does not depend on a prepared shell.
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

// suite is the process under test together with the tokens the cases speak with.
type suite struct {
	base     string
	internal string
	provider string
	other    string
}

func start(t *testing.T) (context.Context, suite) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	base := boot(ctx, t)
	return ctx, suite{
		base:     base,
		internal: tokenFor(ctx, t, internalClient, internalSecret),
		provider: tokenFor(ctx, t, providerClient, providerSecret),
		other:    tokenFor(ctx, t, otherClient, otherSecret),
	}
}

func boot(ctx context.Context, t *testing.T) string {
	t.Helper()
	cfg, err := config.Load(func(key string) string { return suiteEnv()[key] })
	if err != nil {
		t.Fatalf("config = %v, want nil", err)
	}
	got := make(chan *httpapi.Server, 1)
	application := app.New(cfg, fx.Invoke(func(srv *httpapi.Server) { got <- srv }))
	startCtx, startCancel := context.WithTimeout(ctx, 20*time.Second)
	defer startCancel()
	if err := application.Start(startCtx); err != nil {
		t.Fatalf("start = %v, want nil", err)
	}
	// The cleanup runs after the test context is cancelled, so the shutdown carries
	// the same context without its deadline.
	stopping := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(stopping, 15*time.Second)
		defer stopCancel()
		if err := application.Stop(stopCtx); err != nil {
			t.Fatalf("stop = %v, want nil", err)
		}
	})
	return "http://" + (<-got).Addr()
}

func suiteEnv() map[string]string {
	return map[string]string{
		"HTTP_ADDR":                   "127.0.0.1:0",
		"DATABASE_URL":                databaseURL(),
		"SQS_ENDPOINT":                envOr("SQS_ENDPOINT", "http://localhost:4566"),
		"SQS_QUEUE_URL":               envOr("SQS_QUEUE_URL", "http://localhost:4566/000000000000/wager-transactions.fifo"),
		"OTEL_EXPORTER_OTLP_ENDPOINT": envOr("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		"IDP_ISSUER":                  issuer(),
		"CLIENTS_PATH":                envOr("CLIENTS_PATH", "../../../deploy/local/clients.yaml"),
		"PPROF_ADDR":                  envOr("PPROF_ADDR", "127.0.0.1:0"),
	}
}

// answer is one HTTP response, kept raw so that a case reads it as the outcome or
// as problem details without the helper guessing which.
type answer struct {
	status int
	header http.Header
	body   []byte
}

func (a answer) transaction(t *testing.T) externalTransaction {
	t.Helper()
	var answered externalTransaction
	if err := json.Unmarshal(a.body, &answered); err != nil {
		t.Fatalf("unmarshal of the transaction = %v, want nil: %s", err, a.body)
	}
	return answered
}

func (a answer) refusal(t *testing.T) problem.Details {
	t.Helper()
	var refusal problem.Details
	if err := json.Unmarshal(a.body, &refusal); err != nil {
		t.Fatalf("unmarshal of the refusal = %v, want nil: %s", err, a.body)
	}
	return refusal
}

// externalMoney is money as the client reads it: two strings, never a JSON number.
type externalMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type externalTransaction struct {
	ID                    string        `json:"id"`
	Kind                  string        `json:"kind"`
	Status                string        `json:"status"`
	ProviderID            string        `json:"providerId"`
	ExternalTransactionID string        `json:"externalTransactionId"`
	Money                 externalMoney `json:"money"`
	ObservedBalance       externalMoney `json:"observedBalance"`
	FailureCode           string        `json:"failureCode"`
	IdempotentReplay      bool          `json:"idempotentReplay"`
}

type externalWallet struct {
	ID       string        `json:"id"`
	PlayerID string        `json:"playerId"`
	Balance  externalMoney `json:"balance"`
	Version  int64         `json:"version"`
}

// owner is the wallet a case operates on, with the player that owns it.
type owner struct {
	id     string
	player string
}

// openingBalance is what every case starts from: a thousand covers the bets below
// and leaves the arithmetic of each outcome readable.
const openingBalance = "1000.00"

func openWallet(ctx context.Context, t *testing.T, at suite) owner {
	t.Helper()
	player := newID()
	payload, err := json.Marshal(map[string]any{
		"playerId":       player,
		"initialBalance": map[string]string{"amount": openingBalance, "currency": "BRL"},
	})
	if err != nil {
		t.Fatalf("marshal opening = %v, want nil", err)
	}
	answered := call(ctx, t, request{method: http.MethodPost, url: at.base + "/wallets", bearer: at.internal, payload: string(payload)})
	if answered.status != http.StatusCreated {
		t.Fatalf("opening = %d, want 201: %s", answered.status, answered.body)
	}
	var opened externalWallet
	if err := json.Unmarshal(answered.body, &opened); err != nil {
		t.Fatalf("unmarshal opening = %v, want nil", err)
	}
	return owner{id: opened.ID, player: player}
}

// operation is the body of one submission. Every field carries what a valid bet
// uses, and a case overrides only what it is about.
func operation(changes map[string]any) string {
	body := map[string]any{
		"providerId":            providerClient,
		"externalTransactionId": "external-" + newID(),
		"roundId":               "round-" + newID(),
		"gameId":                "game-1",
		"kind":                  "BET",
		"money":                 map[string]string{"amount": "25.00", "currency": "BRL"},
	}
	for field, value := range changes {
		body[field] = value
	}
	payload, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func (o owner) bet(amount string, changes map[string]any) string {
	return o.of("BET", amount, changes)
}

func (o owner) loss(changes map[string]any) string {
	return o.of("LOSS", "0.00", changes)
}

func (o owner) win(amount string, changes map[string]any) string {
	return o.of("WIN", amount, changes)
}

func (o owner) of(kind, amount string, changes map[string]any) string {
	body := map[string]any{
		"playerId": o.player,
		"walletId": o.id,
		"kind":     kind,
		"money":    map[string]string{"amount": amount, "currency": "BRL"},
	}
	for field, value := range changes {
		body[field] = value
	}
	return operation(body)
}

// request is one call to the border. The key travels in the header, which is where
// the contract puts it over HTTP.
type request struct {
	method  string
	url     string
	bearer  string
	payload string
	key     string
}

func submit(ctx context.Context, t *testing.T, at suite, bearer, key, payload string) answer {
	t.Helper()
	return call(ctx, t, request{
		method:  http.MethodPost,
		url:     at.base + wagerRoute,
		bearer:  bearer,
		payload: payload,
		key:     key,
	})
}

func read(ctx context.Context, t *testing.T, at suite, bearer, id string) answer {
	t.Helper()
	return call(ctx, t, request{method: http.MethodGet, url: at.base + wagerRoute + "/" + id, bearer: bearer})
}

func call(ctx context.Context, t *testing.T, ask request) answer {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, ask.method, ask.url, bytes.NewReader([]byte(ask.payload)))
	if err != nil {
		t.Fatalf("request %s = %v, want nil", ask.url, err)
	}
	if ask.payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if ask.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+ask.bearer)
	}
	if ask.key != "" {
		req.Header.Set("Idempotency-Key", ask.key)
	}
	return send(t, req)
}

func send(t *testing.T, req *http.Request) answer {
	t.Helper()
	client := &http.Client{Timeout: 20 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("call %s = %v, want nil", req.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s = %v, want nil", req.URL, err)
	}
	return answer{status: res.StatusCode, header: res.Header, body: payload}
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
	answered := send(t, req)
	if answered.status != http.StatusOK {
		t.Fatalf("token status = %d, want 200: the suite needs the realm imported", answered.status)
	}
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(answered.body, &issued); err != nil {
		t.Fatalf("unmarshal token = %v, want nil", err)
	}
	if issued.AccessToken == "" {
		t.Fatalf("access token is empty, want one issued for %s", clientID)
	}
	return issued.AccessToken
}

func issuer() string {
	return envOr("IDP_ISSUER", envOr("KEYCLOAK_BASE_URL", "http://localhost:8080")+"/realms/junglegaming")
}

func connect(ctx context.Context, t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, databaseURL())
	if err != nil {
		t.Fatalf("connect = %v, want nil: the suite needs the migration applied", err)
	}
	// The cleanup runs after the test context is cancelled, so the close gets a
	// context that carries its values without its deadline.
	closing := context.WithoutCancel(ctx)
	t.Cleanup(func() { _ = conn.Close(closing) })
	return conn
}

func databaseURL() string {
	return envOr("DATABASE_URL", "postgres://junglegaming:junglegaming@localhost:5432/junglegaming?sslmode=disable")
}

func newID() string {
	return uuid.NewV7().String()
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
