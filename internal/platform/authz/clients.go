// Package authz decides who may speak to each business route.
//
// The decision is the client of the token, never what the body or the URL claim
// to be: a provider identifier in a payload authorizes nothing.
package authz

import (
	"errors"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

var (
	// ErrUnreadableClientMap is the versioned map missing or malformed. A
	// process without the map cannot authorize anyone, so it must not listen.
	ErrUnreadableClientMap = errors.New("authz: client map cannot be read")

	// ErrUnmappedClient is a valid token from a client the configuration does
	// not name. It is an identity without permission, not a bad credential.
	ErrUnmappedClient = errors.New("authz: client is not in the map")
)

// internalRole is the token the map uses for the wallet role. The provider entry
// names its providerId instead, because a provider is one of many and the
// internal client is a role.
const internalRole = "internal-wallet"

// Role is what the configuration maps a client to. The zero value is not a
// role.
type Role uint8

const (
	// InternalWallet opens, reads and reconciles. It does not send wagers.
	InternalWallet Role = iota + 1
	// Provider sends wagers and reads its own transactions. It does not open a
	// wallet and does not read a balance.
	Provider
)

// Client is the identity the token resolved to. ProviderID is set only for the
// provider role: the wallet has no provider, its owner is the player.
type Client struct {
	role     Role
	provider identity.ProviderID
}

func (c Client) Role() Role {
	return c.role
}

func (c Client) ProviderID() identity.ProviderID {
	return c.provider
}

// Clients is the versioned map of client to role. The zero value is not used:
// LoadClients is the only constructor.
type Clients struct {
	byID map[string]Client
}

type clientMapFile struct {
	Clients map[string]struct {
		Role       string `yaml:"role"`
		ProviderID string `yaml:"providerId"`
	} `yaml:"clients"`
}

// LoadClients reads the versioned map. It refuses an empty map for the same
// reason it refuses an unreadable one: neither lets the process authorize
// anybody.
func LoadClients(path string) (*Clients, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path comes from configuration the operator controls
	if err != nil {
		return nil, fmt.Errorf("read client map: %w: %w", ErrUnreadableClientMap, err)
	}
	var parsed clientMapFile
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse client map: %w: %w", ErrUnreadableClientMap, err)
	}
	if len(parsed.Clients) == 0 {
		return nil, fmt.Errorf("parse client map: %w: no client is mapped", ErrUnreadableClientMap)
	}
	return build(parsed)
}

func build(parsed clientMapFile) (*Clients, error) {
	byID := make(map[string]Client, len(parsed.Clients))
	for id, entry := range parsed.Clients {
		resolved, err := clientOf(id, entry.Role, entry.ProviderID)
		if err != nil {
			return nil, err
		}
		byID[id] = resolved
	}
	return &Clients{byID: byID}, nil
}

// clientOf refuses an entry that names both a role and a provider, or neither:
// an ambiguous mapping is the kind that authorizes the wrong client.
func clientOf(id, role, providerID string) (Client, error) {
	switch {
	case role == internalRole && providerID == "":
		return Client{role: InternalWallet}, nil
	case role == "" && providerID != "":
		return providerClient(providerID)
	}
	return Client{}, fmt.Errorf("map client: %w: %s names neither one role nor one provider", ErrUnreadableClientMap, id)
}

func providerClient(providerID string) (Client, error) {
	provider, err := identity.ParseProviderID(providerID)
	if err != nil {
		return Client{}, fmt.Errorf("map client: %w: %w", ErrUnreadableClientMap, err)
	}
	return Client{role: Provider, provider: provider}, nil
}

// Resolve answers the client the configuration maps, or ErrUnmappedClient.
func (c *Clients) Resolve(id string) (Client, error) {
	found, ok := c.byID[id]
	if !ok {
		return Client{}, fmt.Errorf("resolve client: %w", ErrUnmappedClient)
	}
	return found, nil
}
