package scenarios

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// idp takes the place of the IdP: it refuses the first requests it receives, and
// grants every one after them a numbered token that lives for the lifespan given.
type idp struct {
	mu       sync.Mutex
	asked    int
	refusing int
	lifespan int
	form     url.Values
}

func (i *idp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	i.mu.Lock()
	i.asked++
	asked, refused := i.asked, i.asked <= i.refusing
	i.form = r.PostForm
	i.mu.Unlock()
	if refused {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"access_token":"token-%d","expires_in":%d}`, asked, i.lifespan)
}

func (i *idp) requests() (int, url.Values) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.asked, i.form
}

// issuing starts a stand-in of the IdP and answers it with its token endpoint.
func issuing(t *testing.T, stand *idp) string {
	t.Helper()
	server := httptest.NewServer(stand)
	t.Cleanup(server.Close)
	return server.URL + "/protocol/openid-connect/token"
}

var issuedAt = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// A token is kept while half its life is ahead of it, and renewed at the instant
// half of it is gone.
func TestToken_keepsATokenUntilHalfItsLifeIsGone(t *testing.T) {
	t.Parallel()
	stand := &idp{lifespan: 300}
	at := issuedAt
	credential := NewCredential(issuing(t, stand), "provider-a", "provider-a-local", func() time.Time { return at })
	steps := []struct {
		at   time.Time
		want string
	}{
		{at: issuedAt, want: "token-1"},
		{at: issuedAt.Add(150*time.Second - time.Nanosecond), want: "token-1"},
		{at: issuedAt.Add(150 * time.Second), want: "token-2"},
	}
	for _, step := range steps {
		at = step.at
		got, err := credential.Token(context.Background())
		if err != nil {
			t.Fatalf("Token at %s = %v, want nil", step.at, err)
		}
		if got != step.want {
			t.Errorf("Token at %s = %s, want %s", step.at, got, step.want)
		}
	}
	if asked, _ := stand.requests(); asked != 2 {
		t.Errorf("requests that reached the IdP = %d, want the first and the renewal", asked)
	}
}

// The token is asked for by the client_credentials grant, with the identity and
// the secret of the client the credential speaks as.
func TestToken_asksByTheClientCredentialsGrantOfItsClient(t *testing.T) {
	t.Parallel()
	stand := &idp{lifespan: 300}
	credential := NewCredential(issuing(t, stand), "provider-a", "provider-a-local", func() time.Time { return issuedAt })
	if _, err := credential.Token(context.Background()); err != nil {
		t.Fatalf("Token of provider-a = %v, want nil", err)
	}
	_, form := stand.requests()
	want := url.Values{"grant_type": {"client_credentials"}, "client_id": {"provider-a"}, "client_secret": {"provider-a-local"}}
	if !maps.EqualFunc(form, want, slices.Equal) {
		t.Errorf("form the IdP received = %v, want %v", form, want)
	}
}

// Requests released together ask for the token at once, and one of them goes to
// the IdP while the others take what it brought.
func TestToken_asksTheIdPOnceForTokensAskedTogether(t *testing.T) {
	t.Parallel()
	stand := &idp{lifespan: 300}
	credential := NewCredential(issuing(t, stand), "provider-a", "provider-a-local", func() time.Time { return issuedAt })
	tokens, failures := make([]string, 20), make([]error, 20)
	var wg sync.WaitGroup
	for index := range tokens {
		wg.Go(func() {
			tokens[index], failures[index] = credential.Token(context.Background())
		})
	}
	wg.Wait()
	for index := range tokens {
		if failures[index] != nil || tokens[index] != "token-1" {
			t.Errorf("Token asked together, number %d = %q and %v, want token-1 and nil", index, tokens[index], failures[index])
		}
	}
	if asked, _ := stand.requests(); asked != 1 {
		t.Errorf("requests that reached the IdP for tokens asked together = %d, want 1", asked)
	}
}

// A refusal of the IdP is answered as an error and not kept: the next ask goes
// to the IdP again.
func TestToken_asksAgainAfterTheIdPRefused(t *testing.T) {
	t.Parallel()
	stand := &idp{lifespan: 300, refusing: 1}
	credential := NewCredential(issuing(t, stand), "provider-a", "provider-a-local", func() time.Time { return issuedAt })
	if refused, err := credential.Token(context.Background()); err == nil {
		t.Fatalf("Token while the IdP refuses = %q and nil error, want the refusal", refused)
	}
	got, err := credential.Token(context.Background())
	if err != nil {
		t.Fatalf("Token after the refusal = %v, want nil", err)
	}
	if got != "token-2" {
		t.Errorf("Token after the refusal = %s, want the token-2 of the second ask", got)
	}
}

// A request that cannot be built, and an IdP that cannot be reached, fail the
// ask and name what failed.
func TestIssue_failsWithoutReachingTheIdP(t *testing.T) {
	t.Parallel()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	cases := []struct {
		name     string
		endpoint string
		names    string
	}{
		{name: "an endpoint that is no URL fails building the request", endpoint: "http://idp\x7f", names: "build the request"},
		{name: "an IdP that is down fails the ask", endpoint: closed.URL, names: "ask the IdP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			credential := NewCredential(tc.endpoint, "provider-a", "provider-a-local", func() time.Time { return issuedAt })
			if _, err := credential.issue(context.Background()); err == nil || !strings.Contains(err.Error(), tc.names) {
				t.Errorf("issue against %q = %v, want an error naming %q", tc.endpoint, err, tc.names)
			}
		})
	}
}

// The answer of the IdP is a token only when it says it granted one and carries
// it; a refusal, a body that is not JSON and a grant without a token are errors.
func TestGranted_readsATokenOnlyFromAGrantThatCarriesIt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		body   string
		want   string
		fails  bool
		is     error
	}{
		{name: "a grant is its token", status: http.StatusOK, body: `{"access_token":"token-1","expires_in":300}`, want: "token-1"},
		{name: "a refusal is an error", status: http.StatusUnauthorized, body: `{"error":"unauthorized_client"}`, fails: true},
		{name: "a body that is not JSON is an error", status: http.StatusOK, body: "<html>", fails: true},
		{name: "a grant without a token is the error of no token", status: http.StatusOK, body: `{"expires_in":300}`, fails: true, is: errNoToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issued, err := granted(&http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))})
			if (err != nil) != tc.fails || (tc.is != nil && !errors.Is(err, tc.is)) {
				t.Fatalf("granted of %d %s error = %v, want failing %t and %v", tc.status, tc.body, err, tc.fails, tc.is)
			}
			if issued.AccessToken != tc.want {
				t.Errorf("granted of %d %s = %q, want %q", tc.status, tc.body, issued.AccessToken, tc.want)
			}
		})
	}
}
