package authz

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

// versionedSenderMap is the map the challenge ships. The identity it names is the
// one the local broker registers for the principal the apply creates.
const versionedSenderMap = "../../../deploy/local/queue-senders.yaml"

// localSender is that identity, which on the local broker is the account.
const localSender = "000000000000"

func TestAllow_acceptsTheProvidersTheVersionedMapListsForItsSender(t *testing.T) {
	t.Parallel()
	senders := loadSenders(t, versionedSenderMap)
	for _, id := range []string{"provider-a", "provider-b"} {
		t.Run(id+" is allowed for the mapped sender", func(t *testing.T) {
			if err := senders.Allow(localSender, providerOf(t, id)); err != nil {
				t.Fatalf("Allow %s = %v, want nil", id, err)
			}
		})
	}
}

func TestAllow_refusesASenderTheMapDoesNotName(t *testing.T) {
	t.Parallel()
	senders := loadSenders(t, versionedSenderMap)
	err := senders.Allow("111111111111", providerOf(t, "provider-a"))
	if !errors.Is(err, ErrUnmappedSender) {
		t.Fatalf("Allow of an unmapped sender = %v, want %v", err, ErrUnmappedSender)
	}
}

// The body declares a provider and the sender is mapped to another: what the
// body claims is checked against what the sender may send, so the claim does not
// widen the list.
func TestAllow_refusesAProviderOutsideTheListOfThatSender(t *testing.T) {
	t.Parallel()
	senders := loadSenders(t, fileWith(t, "senders:\n  \"000000000000\":\n    providers:\n      - provider-a\n"))
	err := senders.Allow(localSender, providerOf(t, "provider-b"))
	if !errors.Is(err, ErrProviderNotAllowed) {
		t.Fatalf("Allow of a provider outside the list = %v, want %v", err, ErrProviderNotAllowed)
	}
	if errors.Is(err, ErrUnmappedSender) {
		t.Fatalf("Allow = %v, want it not to answer as an unmapped sender", err)
	}
}

// More than one entry is the shape production uses, and it is what lets the
// journey exercise the refusal without provisioning a second principal.
func TestLoadSenders_readsMoreThanOneEntryWithAListEach(t *testing.T) {
	t.Parallel()
	content := "senders:\n" +
		"  \"000000000000\":\n    providers:\n      - provider-a\n" +
		"  \"111111111111\":\n    providers:\n      - provider-b\n"
	senders := loadSenders(t, fileWith(t, content))
	if err := senders.Allow(localSender, providerOf(t, "provider-a")); err != nil {
		t.Fatalf("Allow of the first entry = %v, want nil", err)
	}
	if err := senders.Allow("111111111111", providerOf(t, "provider-b")); err != nil {
		t.Fatalf("Allow of the second entry = %v, want nil", err)
	}
	if err := senders.Allow("111111111111", providerOf(t, "provider-a")); !errors.Is(err, ErrProviderNotAllowed) {
		t.Fatalf("Allow across the two lists = %v, want %v", err, ErrProviderNotAllowed)
	}
}

func TestLoadSenders_refusesAMapThatCannotAuthorizeAnyMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
	}{
		{name: "an unparseable file is refused", content: "senders: [this is not a mapping"},
		{name: "an empty map is refused", content: "senders: {}\n"},
		{name: "a sender with no provider is refused", content: "senders:\n  \"000000000000\": {}\n"},
		{name: "a sender with an empty list is refused", content: "senders:\n  \"000000000000\":\n    providers: []\n"},
		{name: "a provider out of format is refused", content: "senders:\n  \"000000000000\":\n    providers:\n      - \"\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadSenders(fileWith(t, tc.content)); !errors.Is(err, ErrUnreadableSenderMap) {
				t.Fatalf("LoadSenders = %v, want %v", err, ErrUnreadableSenderMap)
			}
		})
	}
}

func TestLoadSenders_refusesAMapThatIsNotThere(t *testing.T) {
	t.Parallel()
	_, err := LoadSenders(filepath.Join(t.TempDir(), "absent.yaml"))
	if !errors.Is(err, ErrUnreadableSenderMap) {
		t.Fatalf("LoadSenders = %v, want %v", err, ErrUnreadableSenderMap)
	}
}

func loadSenders(t *testing.T, path string) *Senders {
	t.Helper()
	senders, err := LoadSenders(path)
	if err != nil {
		t.Fatalf("LoadSenders = %v, want nil", err)
	}
	return senders
}

func providerOf(t *testing.T, id string) identity.ProviderID {
	t.Helper()
	provider, err := identity.ParseProviderID(id)
	if err != nil {
		t.Fatalf("ParseProviderID %s = %v, want nil", id, err)
	}
	return provider
}
