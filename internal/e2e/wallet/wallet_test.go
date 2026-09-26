//go:build integration

// The journey of the wallet routes against a real PostgreSQL and a real IdP.
// Every case mints its own player, so two runs of the suite never collide on the
// unique index of one wallet per player and currency.
package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/platform/app"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/httpapi"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// TestMain falls back to the LocalStack credential when it does not come from
// the environment, so the local gate does not depend on a prepared shell.
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

func TestOpenWallet_recordsWalletOpeningAndEntryInOneCommit(t *testing.T) {
	ctx, base := start(t)
	bearer := tokenFor(ctx, t, internalClient, internalSecret)
	player := suiteenv.NewID()
	answered, status := open(ctx, t, base, bearer, body(player, "1000.00", "BRL"))
	if status != http.StatusCreated {
		t.Fatalf("status of the opening with a balance = %d, want 201", status)
	}
	if answered.Balance.Amount != "1000.00" || answered.Balance.Currency != "BRL" {
		t.Fatalf("balance = %+v, want {1000.00 BRL}", answered.Balance)
	}
	if answered.Version != 1 {
		t.Fatalf("version = %d, want 1", answered.Version)
	}
	if answered.PlayerID != player {
		t.Fatalf("playerId = %s, want %s", answered.PlayerID, player)
	}
	assertStored(ctx, t, answered.ID, 100000)
	assertOpening(ctx, t, answered.ID, 100000)
}

func TestOpenWallet_atZeroRecordsNeitherTransactionNorEntry(t *testing.T) {
	ctx, base := start(t)
	bearer := tokenFor(ctx, t, internalClient, internalSecret)
	answered, status := open(ctx, t, base, bearer, body(suiteenv.NewID(), "0.00", "BRL"))
	if status != http.StatusCreated {
		t.Fatalf("status of the opening at zero = %d, want 201", status)
	}
	if answered.Balance.Amount != "0.00" {
		t.Fatalf("balance = %s, want 0.00", answered.Balance.Amount)
	}
	assertStored(ctx, t, answered.ID, 0)
	if got := countTransactions(ctx, t, answered.ID); got != 0 {
		t.Fatalf("transactions = %d, want 0 for an opening at zero", got)
	}
	if got := countEntries(ctx, t, answered.ID); got != 0 {
		t.Fatalf("entries = %d, want 0 for an opening at zero", got)
	}
}

func TestOpenWallet_refusesTheSecondWalletOfThePlayerInTheSameCurrency(t *testing.T) {
	ctx, base := start(t)
	bearer := tokenFor(ctx, t, internalClient, internalSecret)
	player := suiteenv.NewID()
	first, status := open(ctx, t, base, bearer, body(player, "1000.00", "BRL"))
	if status != http.StatusCreated {
		t.Fatalf("first opening = %d, want 201", status)
	}
	refusal, status := openRefusal(ctx, t, base, bearer, body(player, "500.00", "BRL"))
	if status != http.StatusConflict {
		t.Fatalf("second opening = %d, want 409", status)
	}
	if refusal.FailureCode != "" {
		t.Fatalf("failureCode = %s, want empty: an opening is not a wager", refusal.FailureCode)
	}
	assertStored(ctx, t, first.ID, 100000)
}

func TestOpenWallet_acceptsTheSamePlayerInAnotherCurrency(t *testing.T) {
	ctx, base := start(t)
	bearer := tokenFor(ctx, t, internalClient, internalSecret)
	player := suiteenv.NewID()
	if _, status := open(ctx, t, base, bearer, body(player, "1000.00", "BRL")); status != http.StatusCreated {
		t.Fatalf("BRL opening = %d, want 201", status)
	}
	answered, status := open(ctx, t, base, bearer, body(player, "10.00", "USD"))
	if status != http.StatusCreated {
		t.Fatalf("USD opening = %d, want 201", status)
	}
	if answered.Balance.Currency != "USD" {
		t.Fatalf("currency = %s, want USD", answered.Balance.Currency)
	}
}

func TestOpenWallet_refusesEveryAmountOutsideTheContractWithoutWriting(t *testing.T) {
	ctx, base := start(t)
	bearer := tokenFor(ctx, t, internalClient, internalSecret)
	for _, amount := range []string{"", "NaN", "Infinity", "2.5e1", "-25.00", "25.005"} {
		t.Run("amount "+amount+" is refused", func(t *testing.T) {
			player := suiteenv.NewID()
			refusal, status := openRefusal(ctx, t, base, bearer, body(player, amount, "BRL"))
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
			if refusal.FailureCode != "" {
				t.Fatalf("failureCode = %s, want empty for invalid input", refusal.FailureCode)
			}
			assertNoWalletOf(ctx, t, player)
		})
	}
}

func TestOpenWallet_refusesAProviderByPermissionWithoutCreatingAWallet(t *testing.T) {
	ctx, base := start(t)
	bearer := tokenFor(ctx, t, providerClient, providerSecret)
	player := suiteenv.NewID()
	_, status := openRefusal(ctx, t, base, bearer, body(player, "1000.00", "BRL"))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	assertNoWalletOf(ctx, t, player)
}

func TestOpenWallet_refusesAnAbsentCredential(t *testing.T) {
	ctx, base := start(t)
	player := suiteenv.NewID()
	_, status := openRefusal(ctx, t, base, "", body(player, "1000.00", "BRL"))
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	assertNoWalletOf(ctx, t, player)
}

func TestOpenWallet_refusesAnExpiredCredential(t *testing.T) {
	ctx, base := start(t)
	restore := shortenTokenLifespan(ctx, t, 1)
	t.Cleanup(restore)
	bearer := tokenFor(ctx, t, internalClient, internalSecret)
	waitUntilRefused(ctx, t, base, bearer)
}

// waitUntilRefused polls the route until the token the realm issued has expired,
// within a deadline. It polls instead of sleeping a fixed span, because the
// clock of the IdP is not the clock of this process.
func waitUntilRefused(ctx context.Context, t *testing.T, base, bearer string) {
	t.Helper()
	const window = 20 * time.Second
	deadline := time.Now().Add(window)
	attempts := 0
	for time.Now().Before(deadline) {
		attempts++
		_, status := openRefusal(ctx, t, base, bearer, body(suiteenv.NewID(), "1.00", "BRL"))
		if status == http.StatusUnauthorized {
			return
		}
		if status != http.StatusCreated {
			t.Fatalf("status while waiting for the expiry = %d, want 201 or 401", status)
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("the border still accepted the token on %d attempts across %s, want it refused past the expiry", attempts, window)
}

func TestReadWallet_answersTheStoredBalanceAndVersion(t *testing.T) {
	ctx, base := start(t)
	bearer := tokenFor(ctx, t, internalClient, internalSecret)
	opened, status := open(ctx, t, base, bearer, body(suiteenv.NewID(), "1000.00", "BRL"))
	if status != http.StatusCreated {
		t.Fatalf("opening before the read = %d, want 201", status)
	}
	answered, status := read(ctx, t, base, bearer, opened.ID)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if answered.Balance.Amount != "1000.00" || answered.Version != 1 {
		t.Fatalf("read = %s and version %d, want 1000.00 and 1", answered.Balance.Amount, answered.Version)
	}
	assertStored(ctx, t, opened.ID, 100000)
	if got := countEntries(ctx, t, opened.ID); got != 1 {
		t.Fatalf("entries after the read = %d, want the 1 from the opening", got)
	}
}

func TestReadWallet_answers404ForAWalletThatDoesNotExist(t *testing.T) {
	ctx, base := start(t)
	bearer := tokenFor(ctx, t, internalClient, internalSecret)
	asked := suiteenv.NewID()
	status, mediaType := readRefusal(ctx, t, base, bearer, asked)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if mediaType != problem.MediaType {
		t.Fatalf("content type = %s, want %s", mediaType, problem.MediaType)
	}
	if got := count(ctx, t, "SELECT count(*) FROM wallets WHERE id = $1", asked); got != 0 {
		t.Fatalf("wallets for the identity read = %d, want 0: a read writes nothing", got)
	}
}

func TestReadWallet_refusesAProviderByPermission(t *testing.T) {
	ctx, base := start(t)
	internal := tokenFor(ctx, t, internalClient, internalSecret)
	opened, status := open(ctx, t, base, internal, body(suiteenv.NewID(), "1000.00", "BRL"))
	if status != http.StatusCreated {
		t.Fatalf("opening before the provider is refused = %d, want 201", status)
	}
	provider := tokenFor(ctx, t, providerClient, providerSecret)
	status, _ = readRefusal(ctx, t, base, provider, opened.ID)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: a provider does not read a balance", status)
	}
}

func TestHealth_staysPublic(t *testing.T) {
	ctx, base := start(t)
	for _, route := range []string{"/health/live", "/health/ready"} {
		t.Run(route+" answers without a token", func(t *testing.T) {
			status := statusOf(ctx, t, http.MethodGet, base+route, "", nil)
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				t.Fatalf("status = %d, want the route to answer without a credential", status)
			}
		})
	}
}

// externalMoney is money as the client reads it: two strings, never a JSON
// number.
type externalMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type externalWallet struct {
	ID       string        `json:"id"`
	PlayerID string        `json:"playerId"`
	Balance  externalMoney `json:"balance"`
	Version  int64         `json:"version"`
}

func body(player, amount, currency string) string {
	payload, err := json.Marshal(map[string]any{
		"playerId":       player,
		"initialBalance": map[string]string{"amount": amount, "currency": currency},
	})
	if err != nil {
		panic(err)
	}
	return string(payload)
}

func open(ctx context.Context, t *testing.T, base, bearer, payload string) (externalWallet, int) {
	t.Helper()
	var answered externalWallet
	status := request(ctx, t, http.MethodPost, base+"/wallets", bearer, payload, &answered)
	return answered, status
}

func openRefusal(ctx context.Context, t *testing.T, base, bearer, payload string) (problem.Details, int) {
	t.Helper()
	var refusal problem.Details
	status := request(ctx, t, http.MethodPost, base+"/wallets", bearer, payload, &refusal)
	return refusal, status
}

func read(ctx context.Context, t *testing.T, base, bearer, walletID string) (externalWallet, int) {
	t.Helper()
	var answered externalWallet
	status := request(ctx, t, http.MethodGet, base+"/wallets/"+walletID, bearer, "", &answered)
	return answered, status
}

func readRefusal(ctx context.Context, t *testing.T, base, bearer, walletID string) (int, string) {
	t.Helper()
	res := send(ctx, t, http.MethodGet, base+"/wallets/"+walletID, bearer, "")
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode, res.Header.Get("Content-Type")
}

func statusOf(ctx context.Context, t *testing.T, method, rawURL, bearer string, into any) int {
	t.Helper()
	return request(ctx, t, method, rawURL, bearer, "", into)
}

func request(ctx context.Context, t *testing.T, method, rawURL, bearer, payload string, into any) int {
	t.Helper()
	res := send(ctx, t, method, rawURL, bearer, payload)
	defer func() { _ = res.Body.Close() }()
	answered, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s = %v, want nil", rawURL, err)
	}
	if into != nil && len(answered) > 0 {
		if err := json.Unmarshal(answered, into); err != nil {
			t.Fatalf("unmarshal %s = %v, want nil: %s", rawURL, err, answered)
		}
	}
	return res.StatusCode
}

func send(ctx context.Context, t *testing.T, method, rawURL, bearer, payload string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader([]byte(payload)))
	if err != nil {
		t.Fatalf("request %s = %v, want nil", rawURL, err)
	}
	if payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("call %s = %v, want nil", rawURL, err)
	}
	return res
}

func start(t *testing.T) (context.Context, string) {
	t.Helper()
	return startWith(t, nil)
}

// startWith boots the process with the configuration of the suite, overridden
// where a case needs it: the interval of the divergence watcher is shortened
// so a case reads a verdict instead of waiting out the default.
func startWith(t *testing.T, overrides map[string]string) (context.Context, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	env := suiteEnv()
	for key, value := range overrides {
		env[key] = value
	}
	cfg, err := config.Load(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("config = %v, want nil", err)
	}
	got := make(chan *httpapi.Server, 1)
	application := app.New(cfg, fx.Invoke(func(srv *httpapi.Server) { got <- srv }))
	startCtx, startCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer startCancel()
	if err := application.Start(startCtx); err != nil {
		t.Fatalf("start = %v, want nil", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer stopCancel()
		if err := application.Stop(stopCtx); err != nil {
			t.Fatalf("stop = %v, want nil", err)
		}
	})
	return ctx, "http://" + (<-got).Addr()
}

func suiteEnv() map[string]string {
	return map[string]string{
		"HTTP_ADDR":                   "127.0.0.1:0",
		"DATABASE_URL":                suiteenv.DatabaseURL(),
		"SQS_ENDPOINT":                suiteenv.Or("SQS_ENDPOINT", "http://localhost:4566"),
		"SNS_ENDPOINT":                suiteenv.Or("SNS_ENDPOINT", "http://localhost:4566"),
		"SNS_TOPIC_ARN":               suiteenv.Or("SNS_TOPIC_ARN", "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo"),
		"SQS_QUEUE_URL":               suiteenv.Or("SQS_QUEUE_URL", "http://localhost:4566/000000000000/wager-transactions.fifo"),
		"SQS_DLQ_URL":                 suiteenv.Or("SQS_DLQ_URL", "http://localhost:4566/000000000000/wager-transactions-dlq.fifo"),
		"OTEL_EXPORTER_OTLP_ENDPOINT": suiteenv.Or("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		"IDP_ISSUER":                  issuer(),
		"CLIENTS_PATH":                suiteenv.Or("CLIENTS_PATH", "../../../deploy/local/clients.yaml"),
		"QUEUE_SENDERS_PATH":          suiteenv.Or("QUEUE_SENDERS_PATH", "../../../deploy/local/queue-senders.yaml"),
		"PPROF_ADDR":                  suiteenv.Or("PPROF_ADDR", "127.0.0.1:0"),
	}
}

// assertStored reads the committed wallet right after its opening, where the
// version is 1 because the wallet is born at 1. The cases that move the balance
// afterwards read the version from the report they assert instead.
func assertStored(ctx context.Context, t *testing.T, walletID string, cents int64) {
	t.Helper()
	conn := connect(ctx, t)
	var storedCents, storedVersion int64
	err := conn.QueryRow(ctx, "SELECT balance_cents, version FROM wallets WHERE id = $1", walletID).Scan(&storedCents, &storedVersion)
	if err != nil {
		t.Fatalf("read wallet = %v, want nil", err)
	}
	if storedCents != cents || storedVersion != 1 {
		t.Fatalf("stored wallet = %d cents at version %d, want %d at version 1", storedCents, storedVersion, cents)
	}
}

func assertOpening(ctx context.Context, t *testing.T, walletID string, cents int64) {
	t.Helper()
	assertOpeningTransaction(ctx, t, walletID, cents)
	assertOpeningEntry(ctx, t, walletID, cents)
}

func assertOpeningTransaction(ctx context.Context, t *testing.T, walletID string, cents int64) {
	t.Helper()
	var kind, status string
	var observed int64
	err := connect(ctx, t).QueryRow(ctx, "SELECT kind, status, observed_balance_cents FROM wager_transactions WHERE wallet_id = $1", walletID).
		Scan(&kind, &status, &observed)
	if err != nil {
		t.Fatalf("read transaction = %v, want nil", err)
	}
	if kind != "OPENING" || status != "PROCESSED" || observed != cents {
		t.Fatalf("opening = %s %s with %d observed, want OPENING PROCESSED with %d", kind, status, observed, cents)
	}
}

func assertOpeningEntry(ctx context.Context, t *testing.T, walletID string, cents int64) {
	t.Helper()
	var direction string
	var amount, after int64
	err := connect(ctx, t).QueryRow(ctx, "SELECT direction, amount_cents, balance_after_cents FROM ledger_entries WHERE wallet_id = $1", walletID).
		Scan(&direction, &amount, &after)
	if err != nil {
		t.Fatalf("read entry = %v, want nil", err)
	}
	if direction != "CREDIT" || amount != cents || after != cents {
		t.Fatalf("entry = %s of %d leaving %d, want CREDIT of %d leaving %d", direction, amount, after, cents, cents)
	}
}

// assertNoWalletOf scopes the count to the player of the case. Counting the whole
// table would read the rows of every suite sharing this database.
func assertNoWalletOf(ctx context.Context, t *testing.T, player string) {
	t.Helper()
	if got := count(ctx, t, "SELECT count(*) FROM wallets WHERE player_id = $1", player); got != 0 {
		t.Fatalf("wallets of the player = %d, want 0: the refusal writes nothing", got)
	}
}

func countTransactions(ctx context.Context, t *testing.T, walletID string) int64 {
	return count(ctx, t, "SELECT count(*) FROM wager_transactions WHERE wallet_id = $1", walletID)
}

func countEntries(ctx context.Context, t *testing.T, walletID string) int64 {
	return count(ctx, t, "SELECT count(*) FROM ledger_entries WHERE wallet_id = $1", walletID)
}

func count(ctx context.Context, t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var total int64
	if err := connect(ctx, t).QueryRow(ctx, query, args...).Scan(&total); err != nil {
		t.Fatalf("count = %v, want nil", err)
	}
	return total
}

func connect(ctx context.Context, t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, suiteenv.DatabaseURL())
	if err != nil {
		t.Fatalf("connect = %v, want nil: the suite needs the migration applied", err)
	}
	// The cleanup runs after the test context is cancelled, so the close gets a
	// context that carries its values without its deadline.
	closing := context.WithoutCancel(ctx)
	t.Cleanup(func() { _ = conn.Close(closing) })
	return conn
}
