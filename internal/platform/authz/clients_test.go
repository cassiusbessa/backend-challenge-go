package authz

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// versionedMap is the map the challenge ships, and the one test-identity fixes.
const versionedMap = "../../../deploy/local/clients.yaml"

func TestResolve_answersTheInternalWalletRole(t *testing.T) {
	t.Parallel()
	internal := resolve(t, "wallet-internal")
	if internal.Role() != InternalWallet {
		t.Fatalf("role = %d, want the internal wallet role", internal.Role())
	}
	if !internal.ProviderID().IsZero() {
		t.Fatalf("providerId = %s, want none: the wallet has no provider", internal.ProviderID())
	}
}

func TestResolve_answersTheProviderOfEachProviderClient(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"provider-a", "provider-b"} {
		t.Run(id+" resolves to its own provider", func(t *testing.T) {
			provider := resolve(t, id)
			if provider.Role() != Provider {
				t.Fatalf("role = %d, want the provider role", provider.Role())
			}
			if provider.ProviderID().String() != id {
				t.Fatalf("providerId = %s, want %s", provider.ProviderID(), id)
			}
		})
	}
}

func resolve(t *testing.T, id string) Client {
	t.Helper()
	clients, err := LoadClients(versionedMap)
	if err != nil {
		t.Fatalf("LoadClients = %v, want nil", err)
	}
	found, err := clients.Resolve(id)
	if err != nil {
		t.Fatalf("Resolve %s = %v, want nil", id, err)
	}
	return found
}

func TestResolve_refusesAClientOutsideTheMap(t *testing.T) {
	t.Parallel()
	clients, err := LoadClients(versionedMap)
	if err != nil {
		t.Fatalf("LoadClients = %v, want nil", err)
	}
	if _, err := clients.Resolve("provider-c"); !errors.Is(err, ErrUnmappedClient) {
		t.Fatalf("Resolve provider-c = %v, want %v", err, ErrUnmappedClient)
	}
}

func TestLoadClients_refusesAMapThatCannotAuthorizeAnybody(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
	}{
		{name: "an unparseable file is refused", content: "clients: [this is not a mapping"},
		{name: "an empty map is refused", content: "clients: {}\n"},
		{name: "a client with neither role nor provider is refused", content: "clients:\n  ghost: {}\n"},
		{name: "a client with both is refused", content: "clients:\n  both:\n    role: internal-wallet\n    providerId: provider-a\n"},
		{name: "an unknown role is refused", content: "clients:\n  odd:\n    role: superuser\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadClients(fileWith(t, tc.content))
			if !errors.Is(err, ErrUnreadableClientMap) {
				t.Fatalf("LoadClients = %v, want %v", err, ErrUnreadableClientMap)
			}
		})
	}
}

func TestLoadClients_refusesAMapThatIsNotThere(t *testing.T) {
	t.Parallel()
	_, err := LoadClients(filepath.Join(t.TempDir(), "absent.yaml"))
	if !errors.Is(err, ErrUnreadableClientMap) {
		t.Fatalf("LoadClients = %v, want %v", err, ErrUnreadableClientMap)
	}
}

func fileWith(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clients.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture = %v, want nil", err)
	}
	return path
}
