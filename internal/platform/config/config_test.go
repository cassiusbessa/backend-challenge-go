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
		"SQS_DLQ_URL",
		"SNS_ENDPOINT",
		"SNS_TOPIC_ARN",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"IDP_ISSUER",
		"CLIENTS_PATH",
		"QUEUE_SENDERS_PATH",
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
	if cfg.ShutdownTimeout != 20*time.Second {
		t.Fatalf("ShutdownTimeout = %s, want 20s", cfg.ShutdownTimeout)
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

// The three windows of the ingress consumer, with the defaults of the queue the
// apply provisions: a wait of 20s under an invisibility of 30s.
func TestLoad_defaultsTheIngressConsumerWhenNobodySetIt(t *testing.T) {
	t.Parallel()
	cfg, err := Load(envWith("", ""))
	if err != nil {
		t.Fatalf("Load with no queue knob set = %v, want nil", err)
	}
	if cfg.QueuePoll != 20*time.Second {
		t.Fatalf("QueuePoll = %s, want 20s", cfg.QueuePoll)
	}
	if cfg.QueueVisibility != 30*time.Second {
		t.Fatalf("QueueVisibility = %s, want 30s", cfg.QueueVisibility)
	}
	if cfg.QueueTimeout != 8*time.Second {
		t.Fatalf("QueueTimeout = %s, want 8s", cfg.QueueTimeout)
	}
}

// The relation between the three is an invariant and not three numbers: the
// invisibility has to cover the wait of the poll plus the decision, and violating
// it has a silent consequence — the fetch answers empty and still consumes a
// delivery, so the message burns its budget without ever being processed.
func TestLoad_defaultsTheIngressWindowsInTheOrderTheInvariantAsks(t *testing.T) {
	t.Parallel()
	cfg, err := Load(envWith("", ""))
	if err != nil {
		t.Fatalf("Load = %v, want nil", err)
	}
	if cfg.QueueVisibility <= cfg.QueuePoll {
		t.Fatalf("visibility %s, want it past the poll of %s", cfg.QueueVisibility, cfg.QueuePoll)
	}
	if cfg.QueuePoll+cfg.QueueTimeout > cfg.QueueVisibility {
		t.Fatalf("poll %s plus timeout %s, want them inside the visibility of %s", cfg.QueuePoll, cfg.QueueTimeout, cfg.QueueVisibility)
	}
}

// The dead-letter queue is required beside the ingress one: a consumer that cannot
// abandon a message would hold a poisoned one in front of its wallet forever.
func TestLoad_refusesToComeUpWithoutTheAddressOfTheDeadLetterQueue(t *testing.T) {
	t.Parallel()
	_, err := Load(envWith("", "SQS_DLQ_URL"))
	var missing MissingError
	if !errors.As(err, &missing) || missing.Key != "SQS_DLQ_URL" {
		t.Fatalf("Load with no dead-letter address = %v, want MissingError on SQS_DLQ_URL", err)
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
		"SQS_DLQ_URL":                 "http://localhost:4566/000000000000/wager-transactions-dlq.fifo",
		"SNS_ENDPOINT":                "http://localhost:4566",
		"SNS_TOPIC_ARN":               "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "localhost:4317",
		"IDP_ISSUER":                  "http://localhost:8080/realms/junglegaming",
		"CLIENTS_PATH":                "deploy/local/clients.yaml",
		"QUEUE_SENDERS_PATH":          "deploy/local/queue-senders.yaml",
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

// The three windows of the queue can each be a positive duration of its own and
// still name a process that settles nothing: with the invisibility under the wait
// of the long poll, a fetch comes back empty and spends the delivery anyway, and
// five turns send every legitimate message to the dead-letter queue without one of
// them ever being processed. Presence is not enough, so the relation refuses the
// boot the way an absent key does.
func TestIngressWindows_refusesTheRelationBetweenTheThreeWindows(t *testing.T) {
	t.Parallel()
	refusals := map[string]struct{ value, key string }{
		"invisibility under the wait of the long poll":    {"10s", "QUEUE_VISIBILITY"},
		"deadline of a message past the window it spends": {"60s", "QUEUE_TIMEOUT"},
	}
	for name, each := range refusals {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertRefused(t, each.value, each.key)
		})
	}
}

// The two boundaries are the rule itself. The invisibility has to be past the poll
// and not level with it, because a fetch that waits out the whole window leaves
// nothing of it for the decision. The poll plus the deadline may reach the window
// exactly: that is the message using every bit of what it was given, and the answer
// to the broker still goes out inside the delivery it spent.
func TestIngressWindows_holdsAtTheBoundariesOfTheRelation(t *testing.T) {
	t.Parallel()
	t.Run("invisibility level with the wait of the long poll", func(t *testing.T) {
		t.Parallel()
		assertRefused(t, "20s", "QUEUE_VISIBILITY")
	})

	t.Run("deadline reaching the window exactly", func(t *testing.T) {
		t.Parallel()
		// The poll defaults to 20s and the window to 30s, so a deadline of 10s
		// lands on the boundary from below.
		assertAccepted(t, "10s", "QUEUE_TIMEOUT")
	})
}

// A wait below one second truncates to zero on the way to the broker, and a wait of
// zero is not a long poll: the fetch answers empty at once and nothing paces the
// loop, which asks the broker twice a turn. A wait past twenty is refused together
// with the whole call, so every fetch fails and no message is ever consumed.
func TestIngressWindows_boundsTheWaitOfTheLongPoll(t *testing.T) {
	t.Parallel()
	t.Run("under the second it truncates at", func(t *testing.T) {
		t.Parallel()
		assertRefused(t, "500ms", "QUEUE_POLL")
	})

	t.Run("past what the api takes", func(t *testing.T) {
		t.Parallel()
		assertRefused(t, "30s", "QUEUE_POLL")
	})

	t.Run("on either end", func(t *testing.T) {
		t.Parallel()
		assertAccepted(t, "1s", "QUEUE_POLL")
		assertAccepted(t, "20s", "QUEUE_POLL")
	})
}

// Nothing bounds the deadline of the decision from below but this floor: one of a
// millisecond cuts every decision before it commits, and the five deliveries of a
// sound message are spent on the dead-letter queue.
func TestIngressWindows_boundsTheDeadlineOfTheDecisionFromBelow(t *testing.T) {
	t.Parallel()
	t.Run("under its floor", func(t *testing.T) {
		t.Parallel()
		assertRefused(t, "1ms", "QUEUE_TIMEOUT")
	})

	t.Run("on its floor", func(t *testing.T) {
		t.Parallel()
		assertAccepted(t, "1s", "QUEUE_TIMEOUT")
	})
}

// assertRefused loads with that one knob overridden and demands the boot be refused
// by name: the key names which of the windows the operator has to move.
func assertRefused(t *testing.T, value, key string) {
	t.Helper()
	_, err := Load(envWith(value, key))
	var invalid InvalidError
	if !errors.As(err, &invalid) {
		t.Fatalf("Load with %s of %s = %v, want an InvalidError", key, value, err)
	}
	if invalid.Key != key {
		t.Fatalf("key refused for %s of %s = %s, want %s", key, value, invalid.Key, key)
	}
}

func assertAccepted(t *testing.T, value, key string) {
	t.Helper()
	if _, err := Load(envWith(value, key)); err != nil {
		t.Fatalf("Load with %s of %s = %v, want nil", key, value, err)
	}
}

// Validate is the hook that refuses the boot before the HTTP port opens, so the
// relation is read there too: a Config assembled past Load must not come up with
// windows Load would have refused.
func TestValidate_refusesTheWindowsOutOfOrder(t *testing.T) {
	t.Parallel()
	cfg, err := Load(envWith("", ""))
	if err != nil {
		t.Fatalf("Load of the defaults the Validate case starts from = %v, want nil", err)
	}
	cfg.QueueVisibility = cfg.QueuePoll
	if err := cfg.Validate(); err == nil {
		t.Fatalf("Validate with the invisibility level with the poll = nil, want a refusal")
	}
}
