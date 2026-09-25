//go:build integration

// The client of the topic against the real broker: what it sends, and the two
// fields the FIFO topic requires of every send.
package events

import (
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/relayoutbox"
	"github.com/junglegaming/backend-challenge-go/internal/platform/broker"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

func TestPublish_reachesTheTopicWithTheWalletAsGroupAndTheEventAsDeduplication(t *testing.T) {
	ctx, _ := open(t)
	reader := subscribe(ctx, t)
	topic := broker.NewTopic(config.Config{SNSEndpoint: endpoint(), SNSTopicARN: topicARN()})
	if err := topic.Open(ctx); err != nil {
		t.Fatalf("Open the topic = %v, want nil", err)
	}
	wallet, event := newID(), newID()
	sent := relayoutbox.Message{
		Body:            `{"eventId":"` + event + `","eventType":"WalletBalanceChanged"}`,
		GroupID:         wallet,
		DeduplicationID: event,
	}
	if err := topic.Publish(ctx, sent); err != nil {
		t.Fatalf("Publish = %v, want nil", err)
	}
	got := reader.receive(ctx, t, 1)[0]
	if got.Body != sent.Body {
		t.Fatalf("body = %s, want %s", got.Body, sent.Body)
	}
	if got.GroupID != wallet {
		t.Fatalf("group = %s, want the wallet %s", got.GroupID, wallet)
	}
	if got.DeduplicationID != event {
		t.Fatalf("deduplication = %s, want the event %s", got.DeduplicationID, event)
	}
}
