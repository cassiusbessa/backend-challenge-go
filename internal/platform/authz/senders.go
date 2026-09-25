package authz

import (
	"errors"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

var (
	// ErrUnreadableSenderMap is the versioned map missing or malformed. A process
	// that cannot say who may send must not consume from the queue.
	ErrUnreadableSenderMap = errors.New("authz: sender map cannot be read")

	// ErrUnmappedSender is a message from an identity the configuration does not
	// name. It is the refusal a wrong map produces for every legitimate message,
	// so whoever logs it records the observed identity beside it.
	ErrUnmappedSender = errors.New("authz: sender is not in the map")

	// ErrProviderNotAllowed is a body declaring a provider outside the list of the
	// identity that sent it. The body authorizes nothing: what it claims is
	// checked against what the sender may send.
	ErrProviderNotAllowed = errors.New("authz: sender may not send for this provider")
)

// Senders is the versioned map of sender identity to the providers it may send
// for. The zero value is not used: LoadSenders is the only constructor.
//
// The identity is an opaque string and nothing here interprets it: in production
// it is the identifier of the sending principal and on the local broker it is the
// identifier of the account, and the code is the same because only the content of
// the map changes.
type Senders struct {
	byIdentity map[string]map[identity.ProviderID]struct{}
}

type senderMapFile struct {
	Senders map[string]struct {
		Providers []string `yaml:"providers"`
	} `yaml:"senders"`
}

// LoadSenders reads the versioned map. It refuses an empty map for the same
// reason it refuses an unreadable one: neither lets the process authorize any
// message, and a consumer that authorizes nothing sends every legitimate message
// to the dead-letter queue.
func LoadSenders(path string) (*Senders, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path comes from configuration the operator controls
	if err != nil {
		return nil, fmt.Errorf("read sender map: %w: %w", ErrUnreadableSenderMap, err)
	}
	var parsed senderMapFile
	if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse sender map: %w: %w", ErrUnreadableSenderMap, err)
	}
	if len(parsed.Senders) == 0 {
		return nil, fmt.Errorf("parse sender map: %w: no sender is mapped", ErrUnreadableSenderMap)
	}
	return buildSenders(parsed)
}

func buildSenders(parsed senderMapFile) (*Senders, error) {
	byIdentity := make(map[string]map[identity.ProviderID]struct{}, len(parsed.Senders))
	for sender, entry := range parsed.Senders {
		allowed, err := providersOf(sender, entry.Providers)
		if err != nil {
			return nil, err
		}
		byIdentity[sender] = allowed
	}
	return &Senders{byIdentity: byIdentity}, nil
}

// providersOf refuses an entry that names no provider: an identity mapped to an
// empty list is one whose every message is refused, which reads as a mapping and
// behaves as its absence.
func providersOf(sender string, listed []string) (map[identity.ProviderID]struct{}, error) {
	if len(listed) == 0 {
		return nil, fmt.Errorf("map sender: %w: %s names no provider", ErrUnreadableSenderMap, sender)
	}
	allowed := make(map[identity.ProviderID]struct{}, len(listed))
	for _, each := range listed {
		provider, err := identity.ParseProviderID(each)
		if err != nil {
			return nil, fmt.Errorf("map sender: %w: %w", ErrUnreadableSenderMap, err)
		}
		allowed[provider] = struct{}{}
	}
	return allowed, nil
}

// Allow answers nil when that identity may send for that provider, and names
// which of the two refusals it is otherwise: the sender is not mapped at all, or
// it is mapped and this provider is not on its list.
//
// The two are told apart because they say different things to whoever reads the
// log: the first is a map that does not know the sender, which is what a wrong
// configuration looks like, and the second is a body claiming a provider that
// sender does not speak for.
func (s *Senders) Allow(sender string, provider identity.ProviderID) error {
	allowed, ok := s.byIdentity[sender]
	if !ok {
		return fmt.Errorf("allow sender: %w", ErrUnmappedSender)
	}
	if _, ok := allowed[provider]; !ok {
		return fmt.Errorf("allow sender: %w", ErrProviderNotAllowed)
	}
	return nil
}
