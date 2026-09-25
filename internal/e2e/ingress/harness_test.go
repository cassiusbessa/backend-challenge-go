//go:build integration

// The scaffolding of the suite: the real stack, a pair of queues that exists only
// for the length of a case, and the helpers that send a message and read the rows
// back.
//
// Every case waits for a condition with a deadline and never for a number of
// turns. That is a corollary of what was measured on the local broker: a fetch can
// answer empty while the queue is not, so a case that counted turns would be
// intermittent.
package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"
	"go.uber.org/fx"

	"github.com/junglegaming/backend-challenge-go/internal/platform/app"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
	"github.com/junglegaming/backend-challenge-go/internal/platform/httpapi"
)

// The secrets of the local realm. They are the documented example values of the
// challenge, versioned next to the realm import, and not a production credential.
const (
	internalClient = "wallet-internal"
	internalSecret = "wallet-internal-local"
)

// The two providers of the local map. The sender of the suite is mapped to the
// first alone, so a body declaring the second is the refusal that needs no second
// principal.
const (
	mappedProvider   = "provider-a"
	unmappedProvider = "provider-b"
)

// The two credentials a case sends with. The local broker derives the sender
// identity it registers on a message from the access key, so choosing the
// credential is what produces two identities — which is the only way to exercise
// the refusal without provisioning a second principal, since every principal the
// apply could create falls in the same account.
const (
	mappedSender   = "000000000000"
	unmappedSender = "111111111111"
)

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

// suite is the process under test together with the queues it consumes and the
// token the cases open a wallet with.
type suite struct {
	base     string
	internal string
	queues   *queues
}

func start(t *testing.T) (context.Context, suite) {
	t.Helper()
	return startWith(t, nil)
}

// startWith boots the process over a pair of queues of its own, overridden where a
// case needs it.
//
// The queues are of the case and not the provisioned ones: the deployment consumes
// those, and a suite sharing them would be counting messages somebody else is
// taking.
func startWith(t *testing.T, overrides map[string]string) (context.Context, suite) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	pair := createQueues(ctx, t)
	env := map[string]string{
		"SQS_QUEUE_URL":      pair.ingress,
		"SQS_DLQ_URL":        pair.dead,
		"QUEUE_SENDERS_PATH": senderMap(t),
	}
	for key, value := range overrides {
		env[key] = value
	}
	base := boot(ctx, t, env)
	return ctx, suite{base: base, internal: tokenFor(ctx, t, internalClient, internalSecret), queues: pair}
}

// senderMap writes the map of the suite: the sender the local broker registers for
// the generic credential, mapped to one provider alone.
//
// One provider rather than two is what makes the second gate to the dead-letter
// queue reachable — a body declaring the other one — with no second principal and
// no second account.
func senderMap(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue-senders.yaml")
	content := "senders:\n  \"" + mappedSender + "\":\n    providers:\n      - " + mappedProvider + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the sender map = %v, want nil", err)
	}
	return path
}

func boot(ctx context.Context, t *testing.T, overrides map[string]string) string {
	t.Helper()
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
	startCtx, startCancel := context.WithTimeout(ctx, 20*time.Second)
	defer startCancel()
	if err := application.Start(startCtx); err != nil {
		t.Fatalf("start = %v, want nil", err)
	}
	// The cleanup runs after the test context is cancelled, so the shutdown carries
	// the same context without its deadline.
	stopping := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(stopping, 20*time.Second)
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
		"SNS_ENDPOINT":                envOr("SNS_ENDPOINT", "http://localhost:4566"),
		"SNS_TOPIC_ARN":               envOr("SNS_TOPIC_ARN", "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo"),
		"OTEL_EXPORTER_OTLP_ENDPOINT": envOr("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		"IDP_ISSUER":                  issuer(),
		"CLIENTS_PATH":                envOr("CLIENTS_PATH", "../../../deploy/local/clients.yaml"),
		"PPROF_ADDR":                  envOr("PPROF_ADDR", "127.0.0.1:0"),
		// The three windows of the consumer, shortened so a case does not wait out
		// the defaults of production, and kept in the order the invariant asks: the
		// invisibility covers the wait of the poll plus the decision.
		"QUEUE_POLL":       "1s",
		"QUEUE_VISIBILITY": "6s",
		"QUEUE_TIMEOUT":    "4s",
		// The relay and the reference worker scan far more often than production so
		// a case reads the outcome instead of waiting out the default.
		"OUTBOX_INTERVAL":    "50ms",
		"REFERENCE_INTERVAL": "50ms",
	}
}

// queues is the pair of FIFO queues one case works over.
type queues struct {
	client  *sqs.Client
	ingress string
	dead    string
}

// createQueues builds the ingress queue and the dead-letter queue behind it, with
// the redrive the deployment uses, and takes both down after the case.
func createQueues(ctx context.Context, t *testing.T) *queues {
	t.Helper()
	client := clientWith(ctx, t, localAWS["AWS_ACCESS_KEY_ID"], localAWS["AWS_SECRET_ACCESS_KEY"])
	suffix := strings.ReplaceAll(uuid.NewV7().String(), "-", "")
	dead := createQueue(ctx, t, client, "ingress-suite-dlq-"+suffix+".fifo", nil)
	redrive, err := json.Marshal(map[string]any{
		"deadLetterTargetArn": queueARN(ctx, t, client, dead),
		// The same count the deployment provisions, so the limit of the application
		// sits below it here too.
		"maxReceiveCount": 15,
	})
	if err != nil {
		t.Fatalf("marshal the redrive policy = %v, want nil", err)
	}
	ingress := createQueue(ctx, t, client, "ingress-suite-"+suffix+".fifo", map[string]string{
		"RedrivePolicy":                 string(redrive),
		"VisibilityTimeout":             "6",
		"ReceiveMessageWaitTimeSeconds": "1",
	})
	return &queues{client: client, ingress: ingress, dead: dead}
}

func createQueue(ctx context.Context, t *testing.T, client *sqs.Client, name string, attributes map[string]string) string {
	t.Helper()
	all := map[string]string{"FifoQueue": "true"}
	for key, value := range attributes {
		all[key] = value
	}
	created, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: all})
	if err != nil {
		t.Fatalf("create %s = %v, want nil", name, err)
	}
	queueURL := aws.ToString(created.QueueUrl)
	t.Cleanup(func() {
		closing := context.WithoutCancel(ctx)
		_, _ = client.DeleteQueue(closing, &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})
	return queueURL
}

func queueARN(ctx context.Context, t *testing.T, client *sqs.Client, queueURL string) string {
	t.Helper()
	out, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatalf("read the queue arn = %v, want nil", err)
	}
	return out.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
}

// clientWith builds a queue client on one explicit credential. The credential is
// what decides the sender identity the broker registers, so it is a parameter and
// not the ambient environment.
func clientWith(ctx context.Context, t *testing.T, key, secret string) *sqs.Client {
	t.Helper()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithDefaultRegion("us-east-1"),
		awsconfig.WithEC2IMDSClientEnableState(imds.ClientDisabled),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(key, secret, "")),
	)
	if err != nil {
		t.Fatalf("load the aws configuration = %v, want nil", err)
	}
	return sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		o.BaseEndpoint = aws.String(envOr("SQS_ENDPOINT", "http://localhost:4566"))
	})
}

// send puts one message on the ingress queue as the given sender.
//
// The group is the wallet in lowercase, which is what keeps two operations of one
// wallet in order. The deduplication is fresh every time, so a case can send the
// same envelope twice without the five-minute window of the broker swallowing the
// second: that window is not the inbox, and the point of the second send is to
// reach the inbox.
func (q *queues) send(ctx context.Context, t *testing.T, sender, group, body string) {
	t.Helper()
	client := q.client
	if sender != mappedSender {
		client = clientWith(ctx, t, sender, "test")
	}
	_, err := client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(q.ingress),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(strings.ToLower(group)),
		MessageDeduplicationId: aws.String(newID()),
	})
	if err != nil {
		t.Fatalf("send as %s = %v, want nil", sender, err)
	}
}

// awaitEmpty waits until the ingress queue holds nothing, visible or not, which is
// what says the message was answered for.
func (q *queues) awaitEmpty(ctx context.Context, t *testing.T) {
	t.Helper()
	until(t, "the ingress queue to hold nothing", func() bool {
		return q.depth(ctx, t, q.ingress) == 0
	})
}

// awaitDeadLetter waits until that many messages are on the dead-letter queue.
func (q *queues) awaitDeadLetter(ctx context.Context, t *testing.T, want int64) {
	t.Helper()
	until(t, "the dead-letter queue to hold "+strconv.FormatInt(want, 10), func() bool {
		return q.depth(ctx, t, q.dead) == want
	})
}

// depth is every message of that queue: waiting, in flight and delayed. A count of
// the visible ones alone would read a message being decided as a queue that
// drained.
func (q *queues) depth(ctx context.Context, t *testing.T, queueURL string) int64 {
	t.Helper()
	out, err := q.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameApproximateNumberOfMessages,
			sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
			sqstypes.QueueAttributeNameApproximateNumberOfMessagesDelayed,
		},
	})
	if err != nil {
		t.Fatalf("read the depth of %s = %v, want nil", queueURL, err)
	}
	var total int64
	for _, name := range []sqstypes.QueueAttributeName{
		sqstypes.QueueAttributeNameApproximateNumberOfMessages,
		sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		sqstypes.QueueAttributeNameApproximateNumberOfMessagesDelayed,
	} {
		value, err := strconv.ParseInt(out.Attributes[string(name)], 10, 64)
		if err != nil {
			continue
		}
		total += value
	}
	return total
}

// until polls a condition until it holds or the deadline comes. It is how this
// suite waits: a fetch can answer empty while the queue is not, so counting turns
// would be intermittent.
func until(t *testing.T, what string, holds func() bool) {
	t.Helper()
	deadline := time.Now().Add(conditionWait)
	for time.Now().Before(deadline) {
		if holds() {
			return
		}
		time.Sleep(pollEvery)
	}
	t.Fatalf("waited %s for %s and it did not happen", conditionWait, what)
}

const (
	conditionWait = 45 * time.Second
	pollEvery     = 100 * time.Millisecond
)

// message is one envelope on the ingress queue, with the given fields replaced. A
// nil override removes the field, which is how an absent one is told from one that
// is there and empty.
func message(identity string, data map[string]any, top map[string]any) string {
	body := map[string]any{
		"providerId":            mappedProvider,
		"externalTransactionId": "external-" + newID(),
		"idempotencyKey":        "key-" + newID(),
		"roundId":               "round-" + newID(),
		"gameId":                "game-1",
		"kind":                  "BET",
		"money":                 map[string]string{"amount": "25.00", "currency": "BRL"},
	}
	apply(body, data)
	envelope := map[string]any{"messageId": identity, "data": body}
	apply(envelope, top)
	raw, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func apply(into, overrides map[string]any) {
	for field, value := range overrides {
		if value == nil {
			delete(into, field)
			continue
		}
		into[field] = value
	}
}

// owner is the wallet a case operates on, with the player that owns it.
type owner struct {
	id     string
	player string
}

// openingBalance is what every case starts from: a thousand covers the bets below
// and leaves the arithmetic readable.
const openingBalance = "1000.00"

func openWallet(ctx context.Context, t *testing.T, at suite) owner {
	t.Helper()
	player := newID()
	payload, err := json.Marshal(map[string]any{
		"playerId":       player,
		"initialBalance": map[string]string{"amount": openingBalance, "currency": "BRL"},
	})
	if err != nil {
		t.Fatalf("marshal the opening = %v, want nil", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, at.base+"/wallets", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("opening request = %v, want nil", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+at.internal)
	status, body := call(t, req)
	if status != http.StatusCreated {
		t.Fatalf("opening = %d, want 201: %s", status, body)
	}
	var opened struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &opened); err != nil {
		t.Fatalf("unmarshal the opening = %v, want nil", err)
	}
	return owner{id: opened.ID, player: player}
}

// bet is the body of one bet of this wallet, with the given fields replaced.
func (o owner) bet(identity, amount string, changes map[string]any) string {
	data := map[string]any{
		"playerId": o.player,
		"walletId": o.id,
		"money":    map[string]string{"amount": amount, "currency": "BRL"},
	}
	apply(data, changes)
	return message(identity, data, nil)
}

func call(t *testing.T, req *http.Request) (int, []byte) {
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
	return res.StatusCode, payload
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
	status, body := call(t, req)
	if status != http.StatusOK {
		t.Fatalf("token status = %d, want 200: the suite needs the realm imported", status)
	}
	var issued struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &issued); err != nil {
		t.Fatalf("unmarshal the token = %v, want nil", err)
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
	closing := context.WithoutCancel(ctx)
	t.Cleanup(func() { _ = conn.Close(closing) })
	return conn
}

func databaseURL() string {
	return envOr("DATABASE_URL", "postgres://junglegaming:junglegaming@localhost:5432/junglegaming?sslmode=disable")
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func newID() string {
	return uuid.NewV7().String()
}
