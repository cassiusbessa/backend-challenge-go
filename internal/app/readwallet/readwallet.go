// Package readwallet answers the stored state of a wallet.
//
// The read rehydrates no aggregate and calls no movement: it answers the read
// model the SQL row carries.
package readwallet

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

// Wallet answers the balance and the version as they were stored. It writes no
// row and changes neither of the two.
func (s *Service) Wallet(ctx context.Context, id identity.WalletID) (storage.WalletView, error) {
	view, err := s.reads.Wallet(ctx, id)
	if err != nil {
		return storage.WalletView{}, fmt.Errorf("read wallet: %w", err)
	}
	return view, nil
}
