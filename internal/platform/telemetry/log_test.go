package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestCorrelationIDKeepsOpaqueTokenBoundaries(t *testing.T) {
	t.Parallel()
	short := "req-123.abc_DEF:ghi"
	exact := strings.Repeat("a", 64)
	for _, token := range []string{short, exact, "a"} {
		t.Run(token+" is kept", func(t *testing.T) {
			got := CorrelationID(token, "trace")
			if got != token {
				t.Fatalf("correlation = %s, want %s", got, token)
			}
		})
	}
}

func TestCorrelationIDFallsBackToTrace(t *testing.T) {
	t.Parallel()
	trace := "0123456789abcdef0123456789abcdef"
	cases := []string{"bad id", strings.Repeat("a", 65), "", "tok en"}
	for _, header := range cases {
		t.Run(header+" falls back to the trace", func(t *testing.T) {
			got := CorrelationID(header, trace)
			if got != trace {
				t.Fatalf("correlation = %s, want the trace %s", got, trace)
			}
		})
	}
}

func TestAllowDropsSecretAttributes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(Allow(slog.NewJSONHandler(&buf, nil)))
	logger.Info("request",
		slog.String("authorization", "Bearer super-secret-token"),
		slog.String("body", `{"amount":"25.00"}`),
		slog.String("amount", "25.00"),
		slog.String("balance", "10.00"),
		slog.String("secret", "segredo"),
		slog.String("token", "super-secret-token"),
		slog.String("correlationId", "req-1"),
		slog.String("status", "200"),
	)
	line := buf.String()
	for _, banned := range []string{"Bearer", "super-secret-token", "25.00", "10.00", "segredo", "authorization", "amount", "balance"} {
		if strings.Contains(line, banned) {
			t.Fatalf("log contains %q: %s", banned, line)
		}
	}
	if !strings.Contains(line, `"correlationId":"req-1"`) {
		t.Fatalf("log = %s, want a correlationId", line)
	}
}

func TestAllowed_answersOnlyForTheListedKeys(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"walletId":      true,
		"failureCode":   true,
		"stack":         true,
		"error":         true,
		"authorization": false,
		"amount":        false,
	}
	for key, want := range cases {
		t.Run(key, func(t *testing.T) {
			got := allowed(key)
			if got != want {
				t.Fatalf("allowed(%q) = %v, want %v", key, got, want)
			}
		})
	}
}

// The chain of a failure reaches the collector. It is the line that says which
// operation could not be made and why, and a filter that dropped it would leave
// the message alone to answer both.
func TestAllow_keepsTheChainOfAFailure(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(Allow(slog.NewJSONHandler(&buf, nil)))
	logger.Error("relay an outbox event",
		slog.String("error", "publish event: acquire connection: context deadline exceeded"),
		slog.String("body", `{"amount":"25.00"}`),
	)
	line := buf.String()
	if !strings.Contains(line, "acquire connection") {
		t.Fatalf("log = %s, want the chain of the failure in it", line)
	}
	if strings.Contains(line, "25.00") {
		t.Fatalf("log = %s, want the body left out of it", line)
	}
}

func TestWithCorrelation_carriesTheTokenTheBorderDecidedDownTheCall(t *testing.T) {
	t.Parallel()
	carried := telemetryContext(t, "req-42")
	if got := Correlation(carried); got != "req-42" {
		t.Fatalf("Correlation = %q, want req-42", got)
	}
}

// Work no border started has no request to correlate with, and the absence is
// not a failure: the value is simply empty.
func TestCorrelation_answersEmptyWhenNobodyPutOneThere(t *testing.T) {
	t.Parallel()
	if got := Correlation(context.Background()); got != "" {
		t.Fatalf("Correlation of a bare context = %q, want the empty token", got)
	}
}

func telemetryContext(t *testing.T, correlation string) context.Context {
	t.Helper()
	return WithCorrelation(context.Background(), correlation)
}

// A logger built with attributes of its own filters them the same way: the
// handler is the boundary, so nothing gets past it by being attached early.
func TestWithAttrs_keepsOnlyTheAllowedAttributesOfTheLogger(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	logger := slog.New(Allow(slog.NewJSONHandler(&written, nil))).With(
		slog.String("walletId", "wallet-1"),
		slog.String("amount", "25.00"),
	)
	logger.Info("wager")
	line := written.String()
	if !strings.Contains(line, "wallet-1") {
		t.Fatalf("line = %q, want the wallet on it", line)
	}
	if strings.Contains(line, "25.00") {
		t.Fatalf("line = %q, want the amount kept off it", line)
	}
}

// A group does not open a way around the filter: the handler answers itself, so
// what is attached under a group is filtered by the same allow list.
func TestWithGroup_doesNotLetAGroupCarryWhatTheFilterRefuses(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	logger := slog.New(Allow(slog.NewJSONHandler(&written, nil))).WithGroup("wager")
	logger.Info("wager", slog.String("balance", "1000.00"), slog.String("status", "PROCESSED"))
	line := written.String()
	if strings.Contains(line, "1000.00") {
		t.Fatalf("line = %q, want the balance kept off it", line)
	}
	if !strings.Contains(line, "PROCESSED") {
		t.Fatalf("line = %q, want the status on it", line)
	}
}
