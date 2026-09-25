package authz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

func TestOnly_letsTheInternalClientReachTheHandler(t *testing.T) {
	t.Parallel()
	reached := false
	recorder := guard(t, tokens{client: "wallet-internal"}).call(t, "Bearer good", &reached)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status for the internal client = %d, want 200", recorder.Code)
	}
	if !reached {
		t.Fatalf("handler reached = %v, want true for the internal client", reached)
	}
}

func TestOnly_refusesAProviderByPermission(t *testing.T) {
	t.Parallel()
	reached := false
	recorder := guard(t, tokens{client: "provider-a"}).call(t, "Bearer good", &reached)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status for a provider on the wallet route = %d, want 403", recorder.Code)
	}
	if reached {
		t.Fatalf("handler reached = %v, want false: a provider does not open a wallet", reached)
	}
}

func TestOnly_refusesAValidTokenOfAnUnmappedClientByPermission(t *testing.T) {
	t.Parallel()
	reached := false
	recorder := guard(t, tokens{client: "provider-c"}).call(t, "Bearer good", &reached)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status for a client outside the map = %d, want 403", recorder.Code)
	}
	if reached {
		t.Fatalf("handler reached = %v, want false for a client outside the map", reached)
	}
}

func TestOnly_refusesAnAbsentOrMalformedCredential(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		header string
	}{
		{name: "no header is refused", header: ""},
		{name: "another scheme is refused", header: "Basic dXNlcjpwYXNz"},
		{name: "an empty bearer is refused", header: "Bearer "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			recorder := guard(t, tokens{client: "wallet-internal"}).call(t, tc.header, &reached)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status without a credential = %d, want 401", recorder.Code)
			}
			if reached {
				t.Fatalf("handler reached = %v, want false without a credential", reached)
			}
		})
	}
}

func TestOnly_refusesAnExpiredTokenByCredential(t *testing.T) {
	t.Parallel()
	reached := false
	recorder := guard(t, tokens{err: ErrInvalidToken}).call(t, "Bearer expired", &reached)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status for an expired token = %d, want 401", recorder.Code)
	}
	if reached {
		t.Fatalf("handler reached = %v, want false for an expired token", reached)
	}
}

func TestOnly_answersEveryRefusalAsProblemDetails(t *testing.T) {
	t.Parallel()
	reached := false
	recorder := guard(t, tokens{client: "provider-a"}).call(t, "Bearer good", &reached)
	if got := recorder.Header().Get("Content-Type"); got != problem.MediaType {
		t.Fatalf("content type = %s, want %s", got, problem.MediaType)
	}
	body := recorder.Body.String()
	for _, banned := range []string{"Bearer", "good", "balance", "amount"} {
		if strings.Contains(body, banned) {
			t.Fatalf("body = %s, want it without %q", body, banned)
		}
	}
}

// The wager border checks the provider of the body against the client of the
// token, and only the guard knows who that is.
func TestOnly_handsTheResolvedClientToTheHandler(t *testing.T) {
	t.Parallel()
	var got Client
	found := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, found = ClientOf(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	recorder := guard(t, tokens{client: "provider-a"}).reach(t, Provider, next)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status with the client resolved in the context = %d, want 200", recorder.Code)
	}
	if !found {
		t.Fatalf("client found in the context = %t, want true for the one of the token", found)
	}
	if got.ProviderID().String() != "provider-a" {
		t.Fatalf("provider = %s, want provider-a", got.ProviderID())
	}
}

// A handler outside the guard has no client, and reading it answers the zero
// value instead of panicking.
func TestClientOf_answersNothingOutsideTheGuard(t *testing.T) {
	t.Parallel()
	client, found := ClientOf(context.Background())
	if found {
		t.Fatalf("client outside the guard = %+v, want none", client)
	}
	if !client.ProviderID().IsZero() {
		t.Fatalf("provider = %s, want the zero identifier", client.ProviderID())
	}
}

func TestClassOf_keepsCredentialAndPermissionApart(t *testing.T) {
	t.Parallel()
	if got := classOf(ErrInvalidToken); got != problem.Unauthenticated {
		t.Fatalf("class of an invalid token = %d, want unauthenticated", got)
	}
	if got := classOf(ErrUnmappedClient); got != problem.Unauthorized {
		t.Fatalf("class of an unmapped client = %d, want unauthorized", got)
	}
}

type tokens struct {
	client string
	err    error
}

func (v tokens) Client(context.Context, string) (string, error) {
	if v.err != nil {
		return "", v.err
	}
	return v.client, nil
}

type harness struct {
	guard *Guard
}

func guard(t *testing.T, verifier TokenVerifier) harness {
	t.Helper()
	clients, err := LoadClients(versionedMap)
	if err != nil {
		t.Fatalf("LoadClients = %v, want nil", err)
	}
	return harness{guard: NewGuard(verifier, clients)}
}

// reach runs a handler of its own behind the guard, for a case that looks at what
// the request carries and not only at whether it got through.
func (h harness) reach(t *testing.T, role Role, next http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/wagering/transactions", nil)
	request.Header.Set("Authorization", "Bearer good")
	recorder := httptest.NewRecorder()
	h.guard.Only(role, next).ServeHTTP(recorder, request)
	return recorder
}

func (h harness) call(t *testing.T, header string, reached *bool) *httptest.ResponseRecorder {
	t.Helper()
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/wallets", nil)
	if header != "" {
		request.Header.Set("Authorization", header)
	}
	recorder := httptest.NewRecorder()
	h.guard.Only(InternalWallet, next).ServeHTTP(recorder, request)
	return recorder
}
