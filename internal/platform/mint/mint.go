// Package mint answers the identifiers the domain does not create for itself.
//
// The domain takes identity already resolved and validates the format on its
// own, so minting belongs outside it.
package mint

import (
	"uuid"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

// UUIDv7 mints version 7 identifiers, whose time prefix keeps the primary key
// local in the index instead of scattering writes across it. The zero value is
// ready to use.
type UUIDv7 struct{}

func (UUIDv7) WalletID() (identity.WalletID, error) {
	return identity.ParseWalletID(next())
}

func (UUIDv7) TransactionID() (identity.TransactionID, error) {
	return identity.ParseTransactionID(next())
}

func (UUIDv7) EntryID() (identity.LedgerEntryID, error) {
	return identity.ParseLedgerEntryID(next())
}

func (UUIDv7) EventID() (identity.EventID, error) {
	return identity.ParseEventID(next())
}

func next() string {
	return uuid.NewV7().String()
}
