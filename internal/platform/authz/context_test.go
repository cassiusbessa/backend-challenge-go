package authz

import (
	"context"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

func TestWithClient_putsTheResolvedClientWhereTheHandlerReadsIt(t *testing.T) {
	t.Parallel()
	resolved := Client{role: Provider, provider: providerNamed(t, "provider-a")}
	got, ok := ClientOf(withClient(context.Background(), resolved))
	if !ok {
		t.Fatalf("client carried in the context = %t, want true", ok)
	}
	if got.Role() != Provider {
		t.Fatalf("role = %v, want the provider role of the token", got.Role())
	}
	if got.ProviderID().String() != "provider-a" {
		t.Fatalf("provider = %s, want provider-a", got.ProviderID())
	}
}

// A handler outside the guard has no client, and it gets the zero Client rather
// than a panic.
func TestClientOf_answersTheZeroClientOutsideTheGuard(t *testing.T) {
	t.Parallel()
	got, ok := ClientOf(context.Background())
	if ok {
		t.Fatalf("client carried outside the guard = %t, want false", ok)
	}
	if got != (Client{}) {
		t.Fatalf("client = %+v, want the zero value outside the guard", got)
	}
}

// The key of the context is an unexported type, so a value another package put
// under its own key is not the client the guard resolved.
func TestClientOf_ignoresAValueAnotherPackageCouldHavePut(t *testing.T) {
	t.Parallel()
	type foreignKey struct{}
	ctx := context.WithValue(context.Background(), foreignKey{}, Client{role: Provider})
	if _, ok := ClientOf(ctx); ok {
		t.Fatalf("client read from a foreign key = %t, want false", ok)
	}
}

func providerNamed(t *testing.T, text string) identity.ProviderID {
	t.Helper()
	parsed, err := identity.ParseProviderID(text)
	if err != nil {
		t.Fatalf("ParseProviderID = %v, want nil", err)
	}
	return parsed
}
