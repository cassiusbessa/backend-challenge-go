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
	OTELEndpoint    string
	SampleRatio     float64
	ShutdownTimeout time.Duration
	PPROFAddr       string
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
		"OTEL_EXPORTER_OTLP_ENDPOINT": c.OTELEndpoint,
	})
}

func read(getenv func(string) string) map[string]string {
	keys := []string{
		"HTTP_ADDR",
		"DATABASE_URL",
		"SQS_ENDPOINT",
		"SQS_QUEUE_URL",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_SAMPLE_RATIO",
		"SHUTDOWN_TIMEOUT",
		"PPROF_ADDR",
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
		"OTEL_EXPORTER_OTLP_ENDPOINT",
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
	timeout, err := parseTimeout(raw["SHUTDOWN_TIMEOUT"])
	if err != nil {
		return Config{}, err
	}
	return Config{
		HTTPAddr:        raw["HTTP_ADDR"],
		DatabaseURL:     raw["DATABASE_URL"],
		SQSEndpoint:     raw["SQS_ENDPOINT"],
		SQSQueueURL:     raw["SQS_QUEUE_URL"],
		OTELEndpoint:    raw["OTEL_EXPORTER_OTLP_ENDPOINT"],
		SampleRatio:     ratio,
		ShutdownTimeout: timeout,
		PPROFAddr:       parsePPROF(raw["PPROF_ADDR"]),
	}, nil
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

func parseTimeout(raw string) (time.Duration, error) {
	if raw == "" {
		return 10 * time.Second, nil
	}
	return positiveTimeout(raw)
}

func positiveTimeout(raw string) (time.Duration, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 {
		return 0, InvalidError{Key: "SHUTDOWN_TIMEOUT"}
	}
	return duration, nil
}

func parsePPROF(raw string) string {
	if raw == "" {
		return "127.0.0.1:6060"
	}
	return raw
}
