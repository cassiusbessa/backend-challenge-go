package storage

import (
	"context"

	"github.com/junglegaming/backend-challenge-go/internal/domain/event"
)

// Record writes the events of one commit through the outbox of that very
// transaction, in the order the domain answers them.
//
// It is here rather than in each use case so that the three that commit write
// the same set the same way: what a commit emits is decided once, by the
// domain, and what is done with the answer is this loop.
func Record(ctx context.Context, tx Tx, commit event.Commit) error {
	events, err := event.Of(commit)
	if err != nil {
		return err
	}
	for _, envelope := range events {
		if err := tx.Outbox().Insert(ctx, envelope); err != nil {
			return err
		}
	}
	return nil
}
