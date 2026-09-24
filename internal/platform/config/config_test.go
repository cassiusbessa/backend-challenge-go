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
		t.Fatal("error = nil, want missing DATABASE_URL")
	}
	var missing MissingError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want MissingError", err)
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
	}
	for _, key := range keys {
		t.Run(key+" vazio impede a configuração", func(t *testing.T) {
			_, err := Load(envWith("  ", key))
			var missing MissingError
			if !errors.As(err, &missing) {
				t.Fatalf("error = %v, want MissingError", err)
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
		t.Fatalf("Load: %v", err)
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
		{name: "acima de um é inválido", value: "1.0001"},
		{name: "negativo é inválido", value: "-0.1"},
		{name: "NaN é inválido", value: "NaN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(envWith(tc.value, "OTEL_SAMPLE_RATIO"))
			var invalid InvalidError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want InvalidError", err)
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
		t.Run(value+" é válido", func(t *testing.T) {
			cfg, err := Load(envWith(value, "OTEL_SAMPLE_RATIO"))
			if err != nil {
				t.Fatalf("Load: %v", err)
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
		t.Fatalf("error = %v, want InvalidError", err)
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
	}
	if override != "" {
		base[override] = value
	}
	return func(key string) string { return base[key] }
}
