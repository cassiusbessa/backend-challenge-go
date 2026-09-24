package authz

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

// ErrInvalidToken is a credential absent, malformed, expired or from another
// issuer. It is one class of refusal; a valid identity without permission is
// another, and the two never merge.
var ErrInvalidToken = errors.New("authz: access token is not valid")

// Verifier validates the access token against the key set of the realm:
// signature, issuer and expiry.
//
// The audience is not checked. The client_credentials token of the IdP carries
// no audience for this service, and go-authorization puts the decision on the
// client of the token instead.
type Verifier struct {
	tokens *oidc.IDTokenVerifier
}

// NewVerifier builds the verifier without reaching the IdP. The remote key set
// fetches on the first verification and caches with rotation, so a restart does
// not need the IdP to be up and readiness keeps covering only PostgreSQL and
// SQS.
//
// The context is the process one on purpose: the key set refreshes for as long
// as the process lives, which outlasts any request.
func NewVerifier(cfg config.Config) *Verifier {
	keys := oidc.NewRemoteKeySet(context.Background(), cfg.IDPJWKSURL)
	return &Verifier{
		tokens: oidc.NewVerifier(cfg.IDPIssuer, keys, &oidc.Config{SkipClientIDCheck: true}),
	}
}

// clientClaims reads the client of the token. Keycloak puts the authorized
// party of a client_credentials grant in azp.
type clientClaims struct {
	AuthorizedParty string `json:"azp"`
}

// Client answers the client identifier the token was issued to.
func (v *Verifier) Client(ctx context.Context, bearer string) (string, error) {
	token, err := v.tokens.Verify(ctx, bearer)
	if err != nil {
		return "", fmt.Errorf("verify access token: %w: %w", ErrInvalidToken, err)
	}
	var claims clientClaims
	if err := token.Claims(&claims); err != nil {
		return "", fmt.Errorf("read token claims: %w: %w", ErrInvalidToken, err)
	}
	if claims.AuthorizedParty == "" {
		return "", fmt.Errorf("read token claims: %w: no authorized party", ErrInvalidToken)
	}
	return claims.AuthorizedParty, nil
}
