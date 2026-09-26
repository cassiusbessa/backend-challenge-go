package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The token in hand is used until half of its life has passed, and renewed
// then, before it expires: a run longer than the lifespan meets no 401.
func TestToken_renewsAtHalfOfTheLifespanBeforeItExpires(t *testing.T) {
	t.Parallel()
	fake, server := newFakeService(t)
	fake.lifespan = 10
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	source := newTokenSource(optionsFor(t, server), "provider-a", "provider-a-local")
	source.now = func() time.Time { return clock }
	for _, step := range []struct {
		after time.Duration
		want  string
	}{
		{after: 0, want: "token-1"},
		{after: 4 * time.Second, want: "token-1"},
		{after: 6 * time.Second, want: "token-2"},
		{after: 9 * time.Second, want: "token-2"},
	} {
		clock = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC).Add(step.after)
		got, err := source.Token(context.Background())
		if err != nil {
			t.Fatalf("Token at %s = %v, want nil", step.after, err)
		}
		if got != step.want {
			t.Fatalf("Token at %s of a 10s lifespan = %s, want %s", step.after, got, step.want)
		}
	}
}

// An identity provider that refuses, or answers no usable token, is a failure
// naming the client.
func TestAsk_refusesAnAnswerWithoutAUsableToken(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]func(w http.ResponseWriter){
		"a refusal":       func(w http.ResponseWriter) { http.Error(w, "invalid client", http.StatusUnauthorized) },
		"no token":        func(w http.ResponseWriter) { writePlain(w, http.StatusOK, `{"expires_in":300}`) },
		"no lifespan":     func(w http.ResponseWriter) { writePlain(w, http.StatusOK, `{"access_token":"t"}`) },
		"a body not JSON": func(w http.ResponseWriter) { writePlain(w, http.StatusOK, `<html>`) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { answer(w) }))
			t.Cleanup(server.Close)
			source := newTokenSource(options{idp: server.URL, realm: "junglegaming"}, "provider-a", "provider-a-local")
			if _, err := source.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "provider-a") {
				t.Fatalf("Token after %s = %v, want a failure naming the client", name, err)
			}
		})
	}
}

// An identity provider that is not there is a failure naming where it was asked.
func TestAsk_namesTheIdentityProviderItCouldNotReach(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.NotFoundHandler())
	address := server.URL
	server.Close()
	source := newTokenSource(options{idp: address, realm: "junglegaming"}, "wallet-internal", "wallet-internal-local")
	if _, err := source.Token(context.Background()); err == nil || !strings.Contains(err.Error(), address) {
		t.Fatalf("Token from a closed provider = %v, want a failure naming %s", err, address)
	}
}
