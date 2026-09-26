//go:build integration

package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

// The secrets of the local realm. They are the documented example values of the
// challenge, versioned next to the realm import, and not a production
// credential.
const (
	internalClient = "wallet-internal"
	internalSecret = "wallet-internal-local"
	providerClient = "provider-a"
	providerSecret = "provider-a-local"
)

// tokenFor asks the IdP for a client_credentials token. The service never mints
// one: whoever issues is the IdP.
func tokenFor(ctx context.Context, t *testing.T, clientID, secret string) string {
	t.Helper()
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issuer()+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("token request = %v, want nil", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var answered struct {
		AccessToken string `json:"access_token"`
	}
	if status := call(t, req, &answered); status != http.StatusOK {
		t.Fatalf("token status = %d, want 200: the suite needs the realm imported", status)
	}
	if answered.AccessToken == "" {
		t.Fatalf("access token is empty, want one issued for %s", clientID)
	}
	return answered.AccessToken
}

// shortenTokenLifespan makes the realm issue tokens that expire almost at once,
// so the suite can prove the border refuses an expired one with a real token
// instead of a forged claim. It answers the function that puts the realm back.
func shortenTokenLifespan(ctx context.Context, t *testing.T, seconds int) func() {
	t.Helper()
	admin := adminToken(ctx, t)
	realm := realmRepresentation(ctx, t, admin)
	previous, existed := realm["accessTokenLifespan"]
	realm["accessTokenLifespan"] = seconds
	putRealm(ctx, t, admin, realm)
	// The restore runs from a cleanup, after the test context is cancelled, so it
	// carries the same context without its deadline.
	restoring := context.WithoutCancel(ctx)
	return func() {
		restored := realmRepresentation(restoring, t, admin)
		if existed {
			restored["accessTokenLifespan"] = previous
		} else {
			delete(restored, "accessTokenLifespan")
		}
		putRealm(restoring, t, admin, restored)
	}
}

func adminToken(ctx context.Context, t *testing.T) string {
	t.Helper()
	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {suiteenv.Or("KC_BOOTSTRAP_ADMIN_USERNAME", "admin")},
		"password":   {suiteenv.Or("KC_BOOTSTRAP_ADMIN_PASSWORD", "admin")},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, keycloakBase()+"/realms/master/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("admin token request = %v, want nil", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var answered struct {
		AccessToken string `json:"access_token"`
	}
	if status := call(t, req, &answered); status != http.StatusOK {
		t.Fatalf("admin token status = %d, want 200", status)
	}
	return answered.AccessToken
}

func realmRepresentation(ctx context.Context, t *testing.T, admin string) map[string]any {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adminRealmURL(), nil)
	if err != nil {
		t.Fatalf("realm request = %v, want nil", err)
	}
	req.Header.Set("Authorization", "Bearer "+admin)
	realm := map[string]any{}
	if status := call(t, req, &realm); status != http.StatusOK {
		t.Fatalf("realm status = %d, want 200", status)
	}
	return realm
}

func putRealm(ctx context.Context, t *testing.T, admin string, realm map[string]any) {
	t.Helper()
	body, err := json.Marshal(realm)
	if err != nil {
		t.Fatalf("marshal realm = %v, want nil", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, adminRealmURL(), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("realm update request = %v, want nil", err)
	}
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("Content-Type", "application/json")
	if status := call(t, req, nil); status != http.StatusNoContent {
		t.Fatalf("realm update status = %d, want 204", status)
	}
}

func adminRealmURL() string {
	return keycloakBase() + "/admin/realms/junglegaming"
}

func call(t *testing.T, req *http.Request, into any) int {
	t.Helper()
	client := &http.Client{Timeout: 15 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("call %s = %v, want nil", req.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	if into == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read %s = %v, want nil", req.URL, err)
	}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, into); err != nil {
			t.Fatalf("unmarshal %s = %v, want nil", req.URL, err)
		}
	}
	return res.StatusCode
}

func issuer() string {
	return suiteenv.Or("IDP_ISSUER", keycloakBase()+"/realms/junglegaming")
}

func keycloakBase() string {
	return suiteenv.Or("KEYCLOAK_BASE_URL", "http://localhost:8080")
}
