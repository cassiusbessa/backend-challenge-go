package telemetry

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestCorrelationIDKeepsOpaqueTokenBoundaries(t *testing.T) {
	t.Parallel()
	short := "req-123.abc_DEF:ghi"
	exact := strings.Repeat("a", 64)
	for _, token := range []string{short, exact, "a"} {
		t.Run(token+" preservado", func(t *testing.T) {
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
		t.Run(header+" cai no trace", func(t *testing.T) {
			got := CorrelationID(header, trace)
			if got != trace {
				t.Fatalf("correlation = %s, want %s", got, trace)
			}
		})
	}
}

func TestAllowDropsSecretAttributes(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(Allow(slog.NewJSONHandler(&buf, nil)))
	logger.Info("pedido",
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
			t.Fatalf("log contém %q: %s", banned, line)
		}
	}
	if !strings.Contains(line, `"correlationId":"req-1"`) {
		t.Fatalf("log = %s, want correlationId", line)
	}
}
