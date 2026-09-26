package config

import (
	"math"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr         string
	DatabaseURL      string
	SQSEndpoint      string
	SQSQueueURL      string
	SQSDeadLetterURL string
	SNSEndpoint      string
	SNSTopicARN      string
	OTELEndpoint     string
	IDPIssuer        string
	IDPJWKSURL       string
	ClientsPath      string
	SendersPath      string
	SampleRatio      float64
	ShutdownTimeout  time.Duration
	PPROFAddr        string

	// ReferenceTTL is how long an operation waits for the one it cites before the
	// clock closes the wait. go-reference-wait fixes the default at fifteen
	// minutes and says it is configurable.
	ReferenceTTL time.Duration

	// ReferenceInterval is how often the worker scans the queue of waits. The
	// default suits production; the integration suite shortens it so a case does
	// not wait out the default.
	ReferenceInterval time.Duration

	// OutboxInterval is how often the relay scans the publication queue, and
	// OutboxLease is how long one claim holds a row. Both default to values that
	// suit production; the integration suite shortens them so a case does not
	// wait out the default.
	OutboxInterval time.Duration
	OutboxLease    time.Duration

	// The three windows of the ingress consumer, and the relation between them is
	// an invariant rather than three numbers: the invisibility has to cover the
	// wait of the long poll plus the time it takes to decide a message, and the
	// timeout of that decision sits below what is left.
	//
	// The defaults match the queue the apply provisions — a wait of 20s under an
	// invisibility of 30s — and the integration suite shortens them so a case does
	// not wait out the default.
	QueuePoll       time.Duration
	QueueVisibility time.Duration
	QueueTimeout    time.Duration

	// ReconciliationInterval is how often the divergence watcher takes a turn,
	// and ReconciliationBatch is how many wallets one turn reads. Both default
	// to values that suit production; the integration suite shortens the
	// interval so a case reads a verdict instead of waiting out the default.
	ReconciliationInterval time.Duration
	ReconciliationBatch    int
}

type MissingError struct {
	Key string
}

func (e MissingError) Error() string {
	return "config: " + e.Key + " is missing"
}

type InvalidError struct {
	Key string
}

func (e InvalidError) Error() string {
	return "config: " + e.Key + " is not valid"
}

func Load(getenv func(string) string) (Config, error) {
	raw := read(getenv)
	if err := require(raw); err != nil {
		return Config{}, err
	}
	return build(raw)
}

func (c Config) Validate() error {
	if err := ingressWindows(c.QueuePoll, c.QueueVisibility, c.QueueTimeout); err != nil {
		return err
	}
	if err := c.positiveDurations(); err != nil {
		return err
	}
	if c.ReconciliationBatch < 1 {
		return InvalidError{Key: "RECONCILIATION_BATCH"}
	}
	return require(map[string]string{
		"HTTP_ADDR":                   c.HTTPAddr,
		"DATABASE_URL":                c.DatabaseURL,
		"SQS_ENDPOINT":                c.SQSEndpoint,
		"SQS_QUEUE_URL":               c.SQSQueueURL,
		"SNS_ENDPOINT":                c.SNSEndpoint,
		"SNS_TOPIC_ARN":               c.SNSTopicARN,
		"OTEL_EXPORTER_OTLP_ENDPOINT": c.OTELEndpoint,
		"IDP_ISSUER":                  c.IDPIssuer,
		"CLIENTS_PATH":                c.ClientsPath,
		"QUEUE_SENDERS_PATH":          c.SendersPath,
		"SQS_DLQ_URL":                 c.SQSDeadLetterURL,
	})
}

// positiveDurations refuses a Config assembled past Load with a duration Load
// would have refused. An interval of zero is not a refusal of the boot but a
// panic inside the goroutine of its ticker, and a lease, a TTL or a deadline of
// zero expires the moment it starts. The three windows of the ingress are left
// to ingressWindows, which bounds them tighter.
func (c Config) positiveDurations() error {
	for _, each := range []struct {
		key   string
		value time.Duration
	}{
		{"SHUTDOWN_TIMEOUT", c.ShutdownTimeout},
		{"REFERENCE_TTL", c.ReferenceTTL},
		{"REFERENCE_INTERVAL", c.ReferenceInterval},
		{"OUTBOX_INTERVAL", c.OutboxInterval},
		{"OUTBOX_LEASE", c.OutboxLease},
		{"RECONCILIATION_INTERVAL", c.ReconciliationInterval},
	} {
		if each.value <= 0 {
			return InvalidError{Key: each.key}
		}
	}
	return nil
}

func read(getenv func(string) string) map[string]string {
	keys := []string{
		"HTTP_ADDR",
		"DATABASE_URL",
		"SQS_ENDPOINT",
		"SQS_QUEUE_URL",
		"SQS_DLQ_URL",
		"SNS_ENDPOINT",
		"SNS_TOPIC_ARN",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"IDP_ISSUER",
		"IDP_JWKS_URL",
		"CLIENTS_PATH",
		"QUEUE_SENDERS_PATH",
		"OTEL_SAMPLE_RATIO",
		"SHUTDOWN_TIMEOUT",
		"PPROF_ADDR",
		"REFERENCE_TTL",
		"REFERENCE_INTERVAL",
		"OUTBOX_INTERVAL",
		"OUTBOX_LEASE",
		"QUEUE_POLL",
		"QUEUE_VISIBILITY",
		"QUEUE_TIMEOUT",
		"RECONCILIATION_INTERVAL",
		"RECONCILIATION_BATCH",
	}
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = strings.TrimSpace(getenv(key))
	}
	return out
}

func require(raw map[string]string) error {
	keys := []string{
		"HTTP_ADDR",
		"DATABASE_URL",
		"SQS_ENDPOINT",
		"SQS_QUEUE_URL",
		// The dead-letter queue is required beside the ingress one: a consumer
		// that cannot abandon a message would hold a poisoned one in front of its
		// wallet forever.
		"SQS_DLQ_URL",
		"SNS_ENDPOINT",
		"SNS_TOPIC_ARN",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"IDP_ISSUER",
		"CLIENTS_PATH",
		"QUEUE_SENDERS_PATH",
	}
	for _, key := range keys {
		if err := present(key, raw[key]); err != nil {
			return err
		}
	}
	return nil
}

func present(key, value string) error {
	if value == "" {
		return MissingError{Key: key}
	}
	return nil
}

func build(raw map[string]string) (Config, error) {
	ratio, err := parseRatio(raw["OTEL_SAMPLE_RATIO"])
	if err != nil {
		return Config{}, err
	}
	timing, err := durations(raw)
	if err != nil {
		return Config{}, err
	}
	batch, err := parsePositiveInt("RECONCILIATION_BATCH", raw["RECONCILIATION_BATCH"], defaultReconciliationBatch)
	if err != nil {
		return Config{}, err
	}
	return Config{
		HTTPAddr:         raw["HTTP_ADDR"],
		DatabaseURL:      raw["DATABASE_URL"],
		SQSEndpoint:      raw["SQS_ENDPOINT"],
		SQSQueueURL:      raw["SQS_QUEUE_URL"],
		SQSDeadLetterURL: raw["SQS_DLQ_URL"],
		SNSEndpoint:      raw["SNS_ENDPOINT"],
		SNSTopicARN:      raw["SNS_TOPIC_ARN"],
		OTELEndpoint:     raw["OTEL_EXPORTER_OTLP_ENDPOINT"],
		IDPIssuer:        raw["IDP_ISSUER"],
		IDPJWKSURL:       parseJWKSURL(raw["IDP_JWKS_URL"], raw["IDP_ISSUER"]),
		ClientsPath:      raw["CLIENTS_PATH"],
		SendersPath:      raw["QUEUE_SENDERS_PATH"],
		SampleRatio:      ratio,
		ShutdownTimeout:  timing.shutdown,
		PPROFAddr:        parsePPROF(raw["PPROF_ADDR"]),

		ReferenceTTL:      timing.referenceTTL,
		ReferenceInterval: timing.referenceInterval,

		OutboxInterval: timing.outboxInterval,
		OutboxLease:    timing.outboxLease,

		QueuePoll:       timing.queuePoll,
		QueueVisibility: timing.queueVisibility,
		QueueTimeout:    timing.queueTimeout,

		ReconciliationInterval: timing.reconciliationInterval,
		ReconciliationBatch:    batch,
	}, nil
}

// timing is every duration of the configuration. They are read together so that
// build stays a single assembly of the config instead of one branch per knob.
type timing struct {
	shutdown          time.Duration
	referenceTTL      time.Duration
	referenceInterval time.Duration
	outboxInterval    time.Duration
	outboxLease       time.Duration
	queuePoll         time.Duration
	queueVisibility   time.Duration
	queueTimeout      time.Duration

	reconciliationInterval time.Duration
}

// knob is one duration of the configuration: the key it is set by, the default
// it falls to, and the field it lands in.
type knob struct {
	key      string
	fallback time.Duration
	into     *time.Duration
}

// durations reads every duration through the same parse, so a knob left unset
// falls to its default and one set to something that is not a positive duration
// keeps the process from coming up, whichever knob it is.
func durations(raw map[string]string) (timing, error) {
	var out timing
	for _, each := range []knob{
		{"SHUTDOWN_TIMEOUT", defaultShutdownTimeout, &out.shutdown},
		{"REFERENCE_TTL", defaultReferenceTTL, &out.referenceTTL},
		{"REFERENCE_INTERVAL", defaultReferenceInterval, &out.referenceInterval},
		{"OUTBOX_INTERVAL", defaultOutboxInterval, &out.outboxInterval},
		{"OUTBOX_LEASE", defaultOutboxLease, &out.outboxLease},
		{"QUEUE_POLL", defaultQueuePoll, &out.queuePoll},
		{"QUEUE_VISIBILITY", defaultQueueVisibility, &out.queueVisibility},
		{"QUEUE_TIMEOUT", defaultQueueTimeout, &out.queueTimeout},
		{"RECONCILIATION_INTERVAL", defaultReconciliationInterval, &out.reconciliationInterval},
	} {
		value, err := parseDuration(each.key, raw[each.key], each.fallback)
		if err != nil {
			return timing{}, err
		}
		*each.into = value
	}
	if err := ingressWindows(out.queuePoll, out.queueVisibility, out.queueTimeout); err != nil {
		return timing{}, err
	}
	return out, nil
}

// The bounds of the long poll. Below one second the window truncates to zero on
// the way to the broker, which divides a duration into whole seconds, and a wait
// of zero is not a long poll at all: the fetch answers empty at once and nothing
// paces the loop, which asks the broker twice a turn. Twenty seconds is the widest
// wait the API takes, and a value past it is refused together with the whole call,
// so every fetch fails and no message is ever consumed.
const (
	minQueuePoll = time.Second
	maxQueuePoll = 20 * time.Second
)

// minQueueTimeout is the floor of the decision. The relation below bounds it from
// above and nothing bounds it from below: a deadline of a millisecond cuts every
// decision before it commits, and the five deliveries of a message are spent on
// the dead-letter queue with nothing wrong with it.
const minQueueTimeout = time.Second

// ingressWindows refuses a set of the three queue windows the broker would answer
// silently.
//
// The relation is not a preference. With the invisibility under the wait of the
// long poll, a fetch comes back empty and spends the delivery anyway — no error,
// no line, no signal — so five turns send every legitimate message to the
// dead-letter queue without one of them ever being processed. The deadline of a
// message sits inside the window together with the poll for the same reason: the
// delivery it is spending has to still be invisible when the answer to the broker
// goes out.
//
// Presence is not enough here, which is why this is not part of require: all three
// knobs can be set, each to a positive duration of its own, and still name a
// process that cannot settle a message.
func ingressWindows(poll, visibility, timeout time.Duration) error {
	if poll < minQueuePoll || poll > maxQueuePoll {
		return InvalidError{Key: "QUEUE_POLL"}
	}
	if timeout < minQueueTimeout {
		return InvalidError{Key: "QUEUE_TIMEOUT"}
	}
	if visibility <= poll {
		return InvalidError{Key: "QUEUE_VISIBILITY"}
	}
	if poll+timeout > visibility {
		return InvalidError{Key: "QUEUE_TIMEOUT"}
	}
	return nil
}

// The defaults of the reference wait. The TTL is the fifteen minutes of
// go-reference-wait. The interval is the base of the backoff, so a wait due now
// is picked up within one turn of the scan rather than a turn later.
//
// Every duration of the configuration reads through parseDuration: a knob left
// unset falls to its default, and one set to something that is not a positive
// duration keeps the process from coming up at all.
const (
	defaultReferenceTTL      = 15 * time.Minute
	defaultReferenceInterval = time.Second
)

// The defaults of the relay. The interval is the base of the backoff, so a row
// that has just been committed is picked up within one turn of the scan. The
// lease is wide enough for a send that is slow without being wide enough to
// park a row behind a replica that died.
const (
	defaultOutboxInterval = time.Second
	defaultOutboxLease    = 30 * time.Second
)

// The defaults of the ingress consumer, which are the ones the queue the apply
// provisions is configured with. The invisibility covers the wait of the poll plus
// the ten seconds left, and the timeout of a decision sits below those ten: a
// message that volunteered more work than that is given up on before its delivery
// is spent, and the inbox is what keeps the delivery that follows from applying it
// twice.
const (
	defaultQueuePoll       = 20 * time.Second
	defaultQueueVisibility = 30 * time.Second
	defaultQueueTimeout    = 8 * time.Second
)

// The defaults of the divergence watcher. The cost of one turn is the batch
// times the ledger of each wallet, and neither number has been measured under
// load yet: five seconds and fifty wallets are the starting point, and the
// interval moves without a deploy.
const (
	defaultReconciliationInterval = 5 * time.Second
	defaultReconciliationBatch    = 50
)

// defaultShutdownTimeout is the deadline the process has to finish the request in
// flight and stop the background work.
//
// It has to pay every share of the lifecycle, and the largest of them is the
// consumer, which owes one message its own deadline plus the window its answer to
// the broker has. The process refuses to come up on a budget under that sum rather
// than discovering it at the one shutdown that had something to report. The flush
// of the telemetry is not in here: it runs after the lifecycle, on a budget of its
// own.
const defaultShutdownTimeout = 20 * time.Second

// parseDuration answers the default for a key nobody set, and refuses one that
// is set to something that is not a positive duration. A value that cannot be
// read is not rounded to the default: a process configured wrong does not come
// up at all.
func parseDuration(key, raw string, fallback time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, InvalidError{Key: key}
	}
	return value, nil
}

// parsePositiveInt answers the default for a key nobody set, and refuses one
// set to anything but a positive integer, the way parseDuration does: a batch
// of zero would sweep nothing forever, and a negative one is not a batch.
func parsePositiveInt(key, raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, InvalidError{Key: key}
	}
	return value, nil
}

// parseJWKSURL defaults the key set to the realm endpoint of the issuer.
//
// The two are configured apart because they are not the same address: the issuer
// is what the token announces in iss, seen from wherever the client asked for
// it, while the key set is fetched from inside the network the process runs in.
func parseJWKSURL(raw, issuer string) string {
	if raw != "" {
		return raw
	}
	if issuer == "" {
		return ""
	}
	return strings.TrimSuffix(issuer, "/") + "/protocol/openid-connect/certs"
}

func parseRatio(raw string) (float64, error) {
	if raw == "" {
		return 1, nil
	}
	return boundRatio(raw)
}

func boundRatio(raw string) (float64, error) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, InvalidError{Key: "OTEL_SAMPLE_RATIO"}
	}
	if invalidRatio(value) {
		return 0, InvalidError{Key: "OTEL_SAMPLE_RATIO"}
	}
	return value, nil
}

func invalidRatio(value float64) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return true
	}
	return value < 0 || value > 1
}

func parsePPROF(raw string) string {
	if raw == "" {
		return "127.0.0.1:6060"
	}
	return raw
}
