package config

import (
	"errors"
	"testing"
	"time"
)

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

func envWith(value, override string) func(string) string {
	base := map[string]string{
		"HTTP_ADDR":                   "127.0.0.1:0",
		"DATABASE_URL":                "postgres://junglegaming:junglegaming@localhost:5432/junglegaming?sslmode=disable",
		"SQS_ENDPOINT":                "http://localhost:4566",
		"SQS_QUEUE_URL":               "http://localhost:4566/000000000000/wager-transactions.fifo",
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
		t.Fatalf("Load = %v, want nil", err)
	}
	want := "http://localhost:8080/realms/junglegaming/protocol/openid-connect/certs"
	if cfg.IDPJWKSURL != want {
		t.Fatalf("IDPJWKSURL = %s, want %s", cfg.IDPJWKSURL, want)
	}
}

func TestParseJWKSURL_keepsAnAddressReachableFromInsideTheNetwork(t *testing.T) {
	t.Parallel()
	explicit := "http://keycloak:8080/realms/junglegaming/protocol/openid-connect/certs"
	cfg, err := Load(envWith(explicit, "IDP_JWKS_URL"))
	if err != nil {
		t.Fatalf("Load = %v, want nil", err)
	}
	if cfg.IDPJWKSURL != explicit {
		t.Fatalf("IDPJWKSURL = %s, want %s", cfg.IDPJWKSURL, explicit)
	}
	if cfg.IDPIssuer != "http://localhost:8080/realms/junglegaming" {
		t.Fatalf("IDPIssuer = %s, want the issuer untouched", cfg.IDPIssuer)
	}
}
