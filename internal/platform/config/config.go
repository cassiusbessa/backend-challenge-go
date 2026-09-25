package config

import (
	"math"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	SQSEndpoint     string
	SQSQueueURL     string
	SNSEndpoint     string
	SNSTopicARN     string
	OTELEndpoint    string
	IDPIssuer       string
	IDPJWKSURL      string
	ClientsPath     string
	SampleRatio     float64
	ShutdownTimeout time.Duration
	PPROFAddr       string

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
	})
}

func read(getenv func(string) string) map[string]string {
	keys := []string{
		"HTTP_ADDR",
		"DATABASE_URL",
		"SQS_ENDPOINT",
		"SQS_QUEUE_URL",
		"SNS_ENDPOINT",
		"SNS_TOPIC_ARN",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"IDP_ISSUER",
		"IDP_JWKS_URL",
		"CLIENTS_PATH",
		"OTEL_SAMPLE_RATIO",
		"SHUTDOWN_TIMEOUT",
		"PPROF_ADDR",
		"REFERENCE_TTL",
		"REFERENCE_INTERVAL",
		"OUTBOX_INTERVAL",
		"OUTBOX_LEASE",
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
		"SNS_ENDPOINT",
		"SNS_TOPIC_ARN",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"IDP_ISSUER",
		"CLIENTS_PATH",
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
	return Config{
		HTTPAddr:        raw["HTTP_ADDR"],
		DatabaseURL:     raw["DATABASE_URL"],
		SQSEndpoint:     raw["SQS_ENDPOINT"],
		SQSQueueURL:     raw["SQS_QUEUE_URL"],
		SNSEndpoint:     raw["SNS_ENDPOINT"],
		SNSTopicARN:     raw["SNS_TOPIC_ARN"],
		OTELEndpoint:    raw["OTEL_EXPORTER_OTLP_ENDPOINT"],
		IDPIssuer:       raw["IDP_ISSUER"],
		IDPJWKSURL:      parseJWKSURL(raw["IDP_JWKS_URL"], raw["IDP_ISSUER"]),
		ClientsPath:     raw["CLIENTS_PATH"],
		SampleRatio:     ratio,
		ShutdownTimeout: timing.shutdown,
		PPROFAddr:       parsePPROF(raw["PPROF_ADDR"]),

		ReferenceTTL:      timing.referenceTTL,
		ReferenceInterval: timing.referenceInterval,

		OutboxInterval: timing.outboxInterval,
		OutboxLease:    timing.outboxLease,
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
	} {
		value, err := parseDuration(each.key, raw[each.key], each.fallback)
		if err != nil {
			return timing{}, err
		}
		*each.into = value
	}
	return out, nil
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

// defaultShutdownTimeout is the deadline the process has to finish the request
// in flight, flush telemetry and stop the background work.
const defaultShutdownTimeout = 10 * time.Second

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
