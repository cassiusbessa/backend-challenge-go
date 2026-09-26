package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// tokenSource answers an access token of one client by client_credentials, and
// asks the identity provider again once half of the lifespan has passed: a run
// longer than the lifespan of the realm would otherwise meet a 401 halfway.
type tokenSource struct {
	endpoint string
	id       string
	secret   string
	client   *http.Client
	now      func() time.Time

	mu      sync.Mutex
	token   string
	renewAt time.Time
}

func newTokenSource(o options, id, secret string) *tokenSource {
	return &tokenSource{
		endpoint: strings.TrimRight(o.idp, "/") + "/realms/" + o.realm + "/protocol/openid-connect/token",
		id:       id,
		secret:   secret,
		client:   &http.Client{Timeout: requestTimeout},
		now:      time.Now,
	}
}

// Token answers the token in hand, or a new one once the one in hand is past
// half of its life. The lock is held across the request, so the workers that
// meet the renewal wait for one answer instead of each asking.
func (s *tokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && s.now().Before(s.renewAt) {
		return s.token, nil
	}
	token, lifespan, err := s.ask(ctx)
	if err != nil {
		return "", err
	}
	s.token, s.renewAt = token, s.now().Add(lifespan/2)
	return s.token, nil
}

func (s *tokenSource) ask(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {s.id}, "client_secret": {s.secret}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, strings.NewReader(form.Encode())) //nolint:gosec // the operator names the identity provider
	if err != nil {
		return "", 0, fmt.Errorf("build the token request of %s: %w", s.id, err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(request) //nolint:gosec // the operator names the identity provider
	if err != nil {
		return "", 0, fmt.Errorf("ask %s for a token of %s: %w", s.endpoint, s.id, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("identity provider answered %d to the token request of %s", response.StatusCode, s.id)
	}
	var issued struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&issued); err != nil {
		return "", 0, fmt.Errorf("read the token of %s: %w", s.id, err)
	}
	if issued.AccessToken == "" || issued.ExpiresIn <= 0 {
		return "", 0, fmt.Errorf("identity provider answered no usable token for %s", s.id)
	}
	return issued.AccessToken, time.Duration(issued.ExpiresIn) * time.Second, nil
}
