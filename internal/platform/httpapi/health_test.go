package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLiveStaysUpWhenReadyIsDown(t *testing.T) {
	t.Parallel()
	ready := NewReady(okProbe{}, errProbe{})
	liveCode := codeOf(t, http.HandlerFunc(Live), "/health/live")
	readyCode := codeOf(t, ready, "/health/ready")
	if liveCode != http.StatusOK {
		t.Fatalf("live = %d, want 200", liveCode)
	}
	if readyCode != http.StatusServiceUnavailable {
		t.Fatalf("ready = %d, want 503", readyCode)
	}
}

func TestUnknownQueueReturns503(t *testing.T) {
	t.Parallel()
	ready := NewReady(okProbe{}, errProbe{err: errors.New("unknown queue")})
	got := codeOf(t, ready, "/health/ready")
	if got != http.StatusServiceUnavailable {
		t.Fatalf("ready = %d, want 503", got)
	}
}

func TestReadyReturns200WhenBothChecksPass(t *testing.T) {
	t.Parallel()
	got := codeOf(t, NewReady(okProbe{}, okProbe{}), "/health/ready")
	if got != http.StatusOK {
		t.Fatalf("ready = %d, want 200", got)
	}
}

func TestReadyTimeoutIsOneSecond(t *testing.T) {
	t.Parallel()
	ready := NewReady(okProbe{}, okProbe{})
	if ready.timeout != time.Second {
		t.Fatalf("timeout = %s, want 1s", ready.timeout)
	}
}

func TestReadyReturns503WhenProbeDeadlineExpires(t *testing.T) {
	t.Parallel()
	ready := &Ready{postgres: blockingProbe{}, queue: okProbe{}, timeout: time.Nanosecond}
	got := codeOf(t, ready, "/health/ready")
	if got != http.StatusServiceUnavailable {
		t.Fatalf("ready = %d, want 503", got)
	}
}

func codeOf(t *testing.T, handler http.Handler, path string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code
}

type okProbe struct{}

func (okProbe) Check(context.Context) error { return nil }

type errProbe struct{ err error }

func (p errProbe) Check(context.Context) error {
	if p.err != nil {
		return p.err
	}
	return errors.New("down")
}

type blockingProbe struct{}

func (blockingProbe) Check(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
