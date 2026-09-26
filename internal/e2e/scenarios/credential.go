package scenarios

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Credential is one client of the IdP, speaking with client_credentials tokens it
// renews on its own, so a case that outlives the lifespan the realm gives a token
// still speaks with a valid one. The zero value is not a credential:
// NewCredential is the only constructor.
type Credential struct {
	endpoint string
	form     url.Values
	now      func() time.Time

	mu      sync.Mutex
	token   string
	renewAt time.Time
}

func NewCredential(endpoint, clientID, secret string, now func() time.Time) *Credential {
	return &Credential{
		endpoint: endpoint,
		form: url.Values{
			"grant_type":    {"client_credentials"},
			"client_id":     {clientID},
			"client_secret": {secret},
		},
		now: now,
	}
}

// Token answers a token with at least half its life ahead of it, asking the IdP
// for a new one once half the life of the one held is gone. Requests released
// together ask at once, and only the first of them reaches the IdP.
func (c *Credential) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && c.now().Before(c.renewAt) {
		return c.token, nil
	}
	asked := c.now()
	issued, err := c.issue(ctx)
	if err != nil {
		return "", err
	}
	// Half the life, counted from before the request left: what is left then is
	// as long as what was used, far above the flight of any request, whatever
	// lifespan the realm gives.
	c.token, c.renewAt = issued.AccessToken, asked.Add(time.Duration(issued.ExpiresIn)*time.Second/2)
	return c.token, nil
}

// grant is what the IdP answers a request for a token, in the fields a case
// uses.
type grant struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

func (c *Credential) issue(ctx context.Context) (grant, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(c.form.Encode()))
	if err != nil {
		return grant{}, fmt.Errorf("scenarios: build the request for a token: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return grant{}, fmt.Errorf("scenarios: ask the IdP for a token: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	return granted(res)
}

// errNoToken is an answer of the IdP that says it granted and carries no token.
var errNoToken = errors.New("scenarios: the IdP granted no token")

// granted reads the answer of the IdP, which is a token only when it says it
// granted one and carries it.
func granted(res *http.Response) (grant, error) {
	if res.StatusCode != http.StatusOK {
		return grant{}, fmt.Errorf("scenarios: the IdP answered %d for a token, and a case needs the realm imported", res.StatusCode)
	}
	var issued grant
	if err := json.NewDecoder(res.Body).Decode(&issued); err != nil {
		return grant{}, fmt.Errorf("scenarios: decode the token: %w", err)
	}
	if issued.AccessToken == "" {
		return grant{}, errNoToken
	}
	return issued, nil
}
