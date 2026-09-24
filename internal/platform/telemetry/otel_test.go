package telemetry

import (
	"context"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

func TestHostPortDropsTheScheme(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"http://otel-collector:4317":  "otel-collector:4317",
		"https://otel-collector:4317": "otel-collector:4317",
		"otel-collector:4317":         "otel-collector:4317",
		"http://otel-collector:4317/": "otel-collector:4317",
	}
	for raw, want := range cases {
		t.Run(raw+" vira "+want, func(t *testing.T) {
			got := hostPort(raw)
			if got != want {
				t.Fatalf("hostPort = %s, want %s", got, want)
			}
		})
	}
}

func TestSamplerFollowsTheConfiguredRatio(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ratio float64
		want  string
	}{
		{ratio: 1, want: "AlwaysOnSampler"},
		{ratio: 0, want: "AlwaysOffSampler"},
		{ratio: 0.25, want: "TraceIDRatioBased"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			got := sampler(c.ratio).Description()
			if !strings.HasPrefix(got, c.want) {
				t.Fatalf("sampler = %s, want %s", got, c.want)
			}
		})
	}
}

func TestShutdownWithoutStartStaysSilentAndRepeatable(t *testing.T) {
	t.Parallel()
	pipe := NewPipeline(config.Config{OTELEndpoint: "127.0.0.1:1", SampleRatio: 1})
	ctx := context.Background()
	first := pipe.Shutdown(ctx)
	if first != nil {
		t.Fatalf("primeiro shutdown = %v, want nil", first)
	}
	second := pipe.Shutdown(ctx)
	if second != nil {
		t.Fatalf("segundo shutdown = %v, want nil", second)
	}
	select {
	case <-pipe.Stopped():
	default:
		t.Fatal("Stopped não fechou depois do shutdown")
	}
}
