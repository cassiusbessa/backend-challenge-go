package config

import (
	"errors"
	"testing"
	"time"
)

// The two refusals of the configuration reach the operator as a line and
// nothing else: the process did not come up, and the key is the whole message.
// They also have to read apart from each other, because what the operator does
// about an absent value is not what they do about an invalid one.
func TestError_namesTheKeyAndWhatIsWrongWithIt(t *testing.T) {
	t.Parallel()
	cases := map[error]string{
		MissingError{Key: "DATABASE_URL"}: "config: DATABASE_URL is missing",
		InvalidError{Key: "OUTBOX_LEASE"}: "config: OUTBOX_LEASE is not valid",
	}
	for err, want := range cases {
		t.Run(want, func(t *testing.T) {
			if got := err.Error(); got != want {
				t.Fatalf("Error() = %q, want %q", got, want)
			}
		})
	}
}

func TestEmptyDatabaseURLDoesNotListen(t *testing.T) {
	t.Parallel()
	cfg, err := Load(envWith("", "DATABASE_URL"))
	if err == nil {
		t.Fatalf("error = %v, want MissingError for a blank DATABASE_URL", err)
	}
	var missing MissingError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want a typed MissingError", err)
	}
	if missing.Key != "DATABASE_URL" {
		t.Fatalf("key = %s, want DATABASE_URL", missing.Key)
	}
	if cfg.HTTPAddr != "" {
		t.Fatalf("HTTPAddr = %q, want empty config", cfg.HTTPAddr)
	}
}

func TestLoadRejectsBlankRequiredValues(t *testing.T) {
	t.Parallel()
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
		t.Run("blank "+key+" blocks the configuration", func(t *testing.T) {
			_, err := Load(envWith("  ", key))
			var missing MissingError
			if !errors.As(err, &missing) {
				t.Fatalf("error = %v, want MissingError on %s", err, key)
			}
			if missing.Key != key {
				t.Fatalf("key = %s, want %s", missing.Key, key)
			}
		})
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := Load(envWith("", ""))
	if err != nil {
		t.Fatalf("Load with defaults = %v, want nil", err)
	}
	if cfg.SampleRatio != 1 {
		t.Fatalf("SampleRatio = %v, want 1", cfg.SampleRatio)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Fatalf("ShutdownTimeout = %s, want 10s", cfg.ShutdownTimeout)
	}
	if cfg.PPROFAddr != "127.0.0.1:6060" {
		t.Fatalf("PPROFAddr = %s, want 127.0.0.1:6060", cfg.PPROFAddr)
	}
}

func TestLoadRejectsSampleRatioOutsideZeroAndOne(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value string
	}{
		{name: "above one is rejected", value: "1.0001"},
		{name: "negative is rejected", value: "-0.1"},
		{name: "NaN is rejected", value: "NaN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(envWith(tc.value, "OTEL_SAMPLE_RATIO"))
			var invalid InvalidError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want InvalidError on OTEL_SAMPLE_RATIO", err)
			}
			if invalid.Key != "OTEL_SAMPLE_RATIO" {
				t.Fatalf("key = %s, want OTEL_SAMPLE_RATIO", invalid.Key)
			}
		})
	}
}

func TestLoadAcceptsSampleRatioBoundaries(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"0", "1"} {
		t.Run(value+" is accepted", func(t *testing.T) {
			cfg, err := Load(envWith(value, "OTEL_SAMPLE_RATIO"))
			if err != nil {
				t.Fatalf("Load with ratio %s = %v, want nil", value, err)
			}
			want := 0.0
			if value == "1" {
				want = 1
			}
			if cfg.SampleRatio != want {
				t.Fatalf("SampleRatio = %v, want %v", cfg.SampleRatio, want)
			}
		})
	}
}

func TestLoadRejectsNonPositiveShutdownTimeout(t *testing.T) {
	t.Parallel()
	_, err := Load(envWith("0s", "SHUTDOWN_TIMEOUT"))
	var invalid InvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %v, want InvalidError on SHUTDOWN_TIMEOUT", err)
	}
	if invalid.Key != "SHUTDOWN_TIMEOUT" {
		t.Fatalf("key = %s, want SHUTDOWN_TIMEOUT", invalid.Key)
	}
}

// The two knobs of the reference wait: the TTL go-reference-wait says is
// configurable, and the interval the integration suite shortens so a case does
// not wait out the default of production.
func TestLoad_defaultsTheReferenceWaitWhenNobodySetIt(t *testing.T) {
	t.Parallel()
	cfg, err := Load(envWith("", ""))
	if err != nil {
		t.Fatalf("Load with no reference wait set = %v, want nil", err)
	}
	if cfg.ReferenceTTL != 15*time.Minute {
		t.Fatalf("ReferenceTTL = %s, want the 15m of the rule", cfg.ReferenceTTL)
	}
	if cfg.ReferenceInterval != time.Second {
		t.Fatalf("ReferenceInterval = %s, want 1s", cfg.ReferenceInterval)
	}
}

func TestLoad_defaultsTheRelayWhenNobodySetIt(t *testing.T) {
	t.Parallel()
	cfg, err := Load(envWith("", ""))
	if err != nil {
		t.Fatalf("Load with no relay knob set = %v, want nil", err)
	}
	if cfg.OutboxInterval != time.Second {
		t.Fatalf("OutboxInterval = %s, want 1s", cfg.OutboxInterval)
	}
	if cfg.OutboxLease != 30*time.Second {
		t.Fatalf("OutboxLease = %s, want 30s", cfg.OutboxLease)
	}
}

// The topic is what the relay publishes to, so a process without it cannot do
// the work it would be coming up for.
func TestLoad_refusesToComeUpWithoutTheAddressOfTheTopic(t *testing.T) {
	t.Parallel()
	_, err := Load(envWith("", "SNS_TOPIC_ARN"))
	var missing MissingError
	if !errors.As(err, &missing) || missing.Key != "SNS_TOPIC_ARN" {
		t.Fatalf("Load with no topic address = %v, want MissingError on SNS_TOPIC_ARN", err)
	}
}

func TestLoad_takesTheReferenceWaitTheEnvironmentSet(t *testing.T) {
	t.Parallel()
	cfg, err := Load(envWith("30s", "REFERENCE_TTL"))
	if err != nil {
		t.Fatalf("Load with a reference TTL of 30s = %v, want nil", err)
	}
	if cfg.ReferenceTTL != 30*time.Second {
		t.Fatalf("ReferenceTTL = %s, want 30s", cfg.ReferenceTTL)
	}
}

// A value that cannot be read is not rounded to the default: a process
// configured wrong does not come up, so it does not close a wait early or late
// without anyone noticing.
func TestLoad_refusesAReferenceWaitThatIsNotAPositiveDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "a TTL that is not a duration", key: "REFERENCE_TTL", value: "fifteen minutes"},
		{name: "a TTL of zero", key: "REFERENCE_TTL", value: "0s"},
		{name: "an interval that is negative", key: "REFERENCE_INTERVAL", value: "-1s"},
		{name: "an interval that is not a duration", key: "REFERENCE_INTERVAL", value: "often"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(envWith(tc.value, tc.key))
			var invalid InvalidError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want InvalidError on %s", err, tc.key)
			}
			if invalid.Key != tc.key {
				t.Fatalf("refused key = %s, want %s", invalid.Key, tc.key)
			}
		})
	}
}

func envWith(value, override string) func(string) string {
	base := map[string]string{
		"HTTP_ADDR":                   "127.0.0.1:0",
		"DATABASE_URL":                "postgres://junglegaming:junglegaming@localhost:5432/junglegaming?sslmode=disable",
		"SQS_ENDPOINT":                "http://localhost:4566",
		"SQS_QUEUE_URL":               "http://localhost:4566/000000000000/wager-transactions.fifo",
		"SNS_ENDPOINT":                "http://localhost:4566",
		"SNS_TOPIC_ARN":               "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "localhost:4317",
		"IDP_ISSUER":                  "http://localhost:8080/realms/junglegaming",
		"CLIENTS_PATH":                "deploy/local/clients.yaml",
	}
	if override != "" {
		base[override] = value
	}
	return func(key string) string { return base[key] }
}

func TestParseJWKSURL_defaultsToTheRealmEndpointOfTheIssuer(t *testing.T) {
	t.Parallel()
	cfg, err := Load(envWith("", ""))
	if err != nil {
		t.Fatalf("Load with no key set address = %v, want nil", err)
	}
	want := "http://localhost:8080/realms/junglegaming/protocol/openid-connect/certs"
	if cfg.IDPJWKSURL != want {
		t.Fatalf("defaulted key set = %s, want %s", cfg.IDPJWKSURL, want)
	}
}

func TestParseJWKSURL_keepsAnAddressReachableFromInsideTheNetwork(t *testing.T) {
	t.Parallel()
	explicit := "http://keycloak:8080/realms/junglegaming/protocol/openid-connect/certs"
	cfg, err := Load(envWith(explicit, "IDP_JWKS_URL"))
	if err != nil {
		t.Fatalf("Load with the key set set apart = %v, want nil", err)
	}
	if cfg.IDPJWKSURL != explicit {
		t.Fatalf("IDPJWKSURL = %s, want %s", cfg.IDPJWKSURL, explicit)
	}
	if cfg.IDPIssuer != "http://localhost:8080/realms/junglegaming" {
		t.Fatalf("IDPIssuer = %s, want the issuer untouched", cfg.IDPIssuer)
	}
}

// Every duration of the configuration reads through here: absent falls to the
// default, and a value that was set is taken as it was written.
func TestParseDuration_answersTheDefaultOrTheValueThatWasSet(t *testing.T) {
	t.Parallel()
	got, err := parseDuration("REFERENCE_TTL", "", 15*time.Minute)
	if err != nil || got != 15*time.Minute {
		t.Fatalf("parseDuration of an unset key = %s with %v, want the default with nil", got, err)
	}
	got, err = parseDuration("REFERENCE_TTL", "90s", 15*time.Minute)
	if err != nil || got != 90*time.Second {
		t.Fatalf("parseDuration of a value that was set = %s with %v, want 1m30s with nil", got, err)
	}
}

// What is not a positive duration keeps the process from coming up rather than
// being rounded to the default: a knob nobody can read is not a knob at its
// default.
func TestParseDuration_refusesWhatIsNotAPositiveDuration(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"soon", "0s", "-1s"} {
		refused, err := parseDuration("REFERENCE_TTL", raw, 15*time.Minute)
		var invalid InvalidError
		if !errors.As(err, &invalid) || invalid.Key != "REFERENCE_TTL" {
			t.Fatalf("parseDuration of %q = %v, want InvalidError on REFERENCE_TTL", raw, err)
		}
		if refused != 0 {
			t.Fatalf("duration beside the refusal of %q = %s, want the zero value", raw, refused)
		}
	}
}
