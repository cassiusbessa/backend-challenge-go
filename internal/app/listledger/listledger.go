// Package listledger answers one page of the ledger of a wallet.
//
// The read rehydrates no aggregate and rebuilds no balance: it answers the
// entries the SQL rows carry, in the order the ledger fixes, and the token
// that continues from the last one.
package listledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

// The size of a page. The default is what a client gets when it asks for
// nothing, and the ceiling is what the route refuses past rather than clamps
// to: a client that asked for more than the route serves is told so, the same
// way money.Parse does not round an amount it cannot accept.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// ErrInvalidLimit is a limit outside 1 to MaxLimit. Zero is not one: it is the
// absence of a limit, and the default takes its place.
var ErrInvalidLimit = errors.New("listledger: limit is outside the range of a page")

// Query is one page asked for. Limit at zero means the default; Cursor empty
// means the first page.
type Query struct {
	WalletID identity.WalletID
	Limit    int
	Cursor   string
}

// Page is what the read answers. NextCursor is empty on the last page, which
// is what tells the client to stop.
type Page struct {
	Entries    []storage.EntryView
	NextCursor string
}

// Service answers the read. The zero value is not used: New is the only
// constructor.
type Service struct {
	reads storage.Reads
}

func New(reads storage.Reads) *Service {
	return &Service{reads: reads}
}

// Page answers the entries after the cursor, up to the limit, and the cursor of
// the last one when there is a next page. It refuses the limit and the cursor
// before asking the ledger anything, and writes no row.
func (s *Service) Page(ctx context.Context, query Query) (Page, error) {
	limit, err := limitOf(query.Limit)
	if err != nil {
		return Page{}, err
	}
	after, err := positionOfQuery(query)
	if err != nil {
		return Page{}, err
	}
	// One row past the page is what says there is a next one, without a COUNT
	// over the table.
	entries, err := s.reads.Ledger(ctx, query.WalletID, after, limit+1)
	if err != nil {
		return Page{}, fmt.Errorf("read ledger: %w", err)
	}
	return pageOf(query.WalletID, entries, limit), nil
}

func limitOf(asked int) (int, error) {
	if asked == 0 {
		return DefaultLimit, nil
	}
	if asked < 1 || asked > MaxLimit {
		return 0, ErrInvalidLimit
	}
	return asked, nil
}

func positionOfQuery(query Query) (storage.EntryPosition, error) {
	if query.Cursor == "" {
		return storage.EntryPosition{}, nil
	}
	return decodeCursor(query.WalletID, query.Cursor)
}

// pageOf cuts the row past the page and issues the cursor of the last one
// answered only when that row existed.
func pageOf(wallet identity.WalletID, entries []storage.EntryView, limit int) Page {
	if len(entries) <= limit {
		return Page{Entries: entries}
	}
	kept := entries[:limit]
	last := kept[len(kept)-1]
	return Page{
		Entries:    kept,
		NextCursor: encodeCursor(wallet, storage.EntryPosition{Sequence: last.Sequence, EntryID: last.ID}),
	}
}
