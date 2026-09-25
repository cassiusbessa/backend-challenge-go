package authz

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

// scheme is the only authorization scheme the border accepts.
const scheme = "Bearer "

// TokenVerifier is the port of the IdP, so a unit test of the guard does not
// need one.
type TokenVerifier interface {
	Client(ctx context.Context, bearer string) (string, error)
}

// Guard is the middleware of the business routes. The zero value is not used:
// NewGuard is the only constructor.
type Guard struct {
	tokens  TokenVerifier
	clients *Clients
}

func NewGuard(tokens TokenVerifier, clients *Clients) *Guard {
	return &Guard{tokens: tokens, clients: clients}
}

// Only lets just the named role reach the handler, and carries the client it
// resolved into the request context. Health stays outside this wrapper and
// therefore public.
//
// The client travels because the wager border has to check the provider of the
// body against the one of the token, and only the guard knows who the token
// belongs to.
//
// The refusal writes problem details and nothing else: it reveals no balance, no
// amount and not even whether the record behind the route exists.
func (g *Guard) Only(role Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, err := g.resolve(r)
		if err != nil {
			problem.Write(w, r, problem.Of(classOf(err)))
			return
		}
		if client.Role() != role {
			problem.Write(w, r, problem.Of(problem.Unauthorized))
			return
		}
		next.ServeHTTP(w, r.WithContext(withClient(r.Context(), client)))
	})
}

// classOf keeps the two refusals apart: a bad credential is one class, and a
// valid identity the map does not name is the other.
func classOf(err error) problem.Class {
	if errors.Is(err, ErrInvalidToken) {
		return problem.Unauthenticated
	}
	return problem.Unauthorized
}

func (g *Guard) resolve(r *http.Request) (Client, error) {
	bearer, err := bearerOf(r.Header.Get("Authorization"))
	if err != nil {
		return Client{}, err
	}
	id, err := g.tokens.Client(r.Context(), bearer)
	if err != nil {
		return Client{}, err
	}
	return g.clients.Resolve(id)
}

func bearerOf(header string) (string, error) {
	if !strings.HasPrefix(header, scheme) {
		return "", ErrInvalidToken
	}
	bearer := strings.TrimSpace(strings.TrimPrefix(header, scheme))
	if bearer == "" {
		return "", ErrInvalidToken
	}
	return bearer, nil
}
