package listledger

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
)

// ErrInvalidCursor is the single refusal of a cursor: malformed, out of range,
// or issued for another wallet. One sentinel for every case is what keeps the
// refusal of a cursor of wallet A, presented on wallet B, indistinguishable
// from the refusal of garbage. See ADR 0023.
var ErrInvalidCursor = errors.New("listledger: cursor is not valid")

// cursorParts is the shape of the token: the wallet it was issued for, the
// sequence and the identity of the last entry answered.
const cursorParts = 3

// encodeCursor writes the token of the position, bound to the wallet it was
// issued for. It is opaque to the client, not secret: what it carries is the
// pair that orders the ledger and the wallet the pair belongs to.
func encodeCursor(wallet identity.WalletID, position storage.EntryPosition) string {
	text := wallet.String() + ":" + strconv.FormatInt(position.Sequence, 10) + ":" + position.EntryID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(text))
}

// decodeCursor reads the position back, and refuses with ErrInvalidCursor a
// token that is malformed, whose sequence is below the first one, whose entry
// is not an identifier, or that was issued for a wallet other than the one in
// the URL.
func decodeCursor(wallet identity.WalletID, cursor string) (storage.EntryPosition, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return storage.EntryPosition{}, ErrInvalidCursor
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) != cursorParts {
		return storage.EntryPosition{}, ErrInvalidCursor
	}
	if !issuedFor(wallet, parts[0]) {
		return storage.EntryPosition{}, ErrInvalidCursor
	}
	return positionOf(parts[1], parts[2])
}

// issuedFor reports whether the wallet named by the token is the one in the
// URL. A cursor of another wallet answered in silence would page the wrong
// ledger from a position that means nothing there.
func issuedFor(wallet identity.WalletID, text string) bool {
	issued, err := identity.ParseWalletID(text)
	if err != nil {
		return false
	}
	return issued == wallet
}

func positionOf(sequence, entry string) (storage.EntryPosition, error) {
	parsed, err := strconv.ParseInt(sequence, 10, 64)
	if err != nil || parsed < 1 {
		return storage.EntryPosition{}, ErrInvalidCursor
	}
	entryID, err := identity.ParseLedgerEntryID(entry)
	// The nil identifier is absence, not an entry. Accepted from a token, it would
	// compare below every row of the sequence it names, and the page would answer
	// again the entry the client has already seen.
	if err != nil || entryID.IsZero() {
		return storage.EntryPosition{}, ErrInvalidCursor
	}
	return storage.EntryPosition{Sequence: parsed, EntryID: entryID}, nil
}
