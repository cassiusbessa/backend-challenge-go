// Package readwager answers the recorded outcome of one wager transaction.
//
// The read rehydrates no aggregate and reapplies nothing: it answers the read
// model the row carries, and a transaction of another provider answers the same
// absence as one that does not exist.
package readwager

import (
	"context"
	"fmt"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

// Service answers the read. The zero value is not used: New is the only
// constructor.
type Service struct {
	reads storage.Reads
}

func New(reads storage.Reads) *Service {
	return &Service{reads: reads}
}

// Transaction answers the outcome recorded for that transaction of that provider.
// The provider is asked of the query and not checked afterwards, so there is no
// branch able to tell a transaction of somebody else from one that is not there.
func (s *Service) Transaction(ctx context.Context, id identity.TransactionID, provider identity.ProviderID) (storage.TransactionView, error) {
	view, err := s.reads.Transaction(ctx, id, provider)
	if err != nil {
		return storage.TransactionView{}, fmt.Errorf("read wager transaction: %w", err)
	}
	return view, nil
}

// ByExternal answers the outcome recorded for the operation that provider sent
// under that external identifier. The provider is asked of the query for the same
// reason as in Transaction: an identifier of somebody else is simply not there.
func (s *Service) ByExternal(ctx context.Context, provider identity.ProviderID, external identity.ExternalTransactionID) (storage.TransactionView, error) {
	view, err := s.reads.TransactionByExternal(ctx, provider, external)
	if err != nil {
		return storage.TransactionView{}, fmt.Errorf("read wager transaction by external identifier: %w", err)
	}
	return view, nil
}
