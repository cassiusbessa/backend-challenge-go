package listledger

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
)

func TestDecodeCursor_readsBackWhatEncodeCursorWrote(t *testing.T) {
	t.Parallel()
	position := storage.EntryPosition{Sequence: 7, EntryID: entryOf(t, entryText)}
	decoded, err := decodeCursor(walletOf(t, walletText), encodeCursor(walletOf(t, walletText), position))
	if err != nil {
		t.Fatalf("decodeCursor of an issued cursor = %v, want nil", err)
	}
	if decoded != position {
		t.Fatalf("position = %+v, want %+v", decoded, position)
	}
}

// The token is opaque, not readable: a client that continues does not need to
// know what is inside, and what is inside is neither an amount nor a credential.
func TestEncodeCursor_writesTheThreePartsWithoutPadding(t *testing.T) {
	t.Parallel()
	cursor := encodeCursor(walletOf(t, walletText), storage.EntryPosition{Sequence: 7, EntryID: entryOf(t, entryText)})
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		t.Fatalf("cursor is not unpadded base64url: %v", err)
	}
	if string(decoded) != walletText+":7:"+entryText {
		t.Fatalf("cursor carries %q, want the wallet, the sequence and the entry", decoded)
	}
}

// The first sequence of a ledger is one, and a cursor pointing at it is a
// cursor the route issues after a page of one entry: the boundary is accepted.
func TestDecodeCursor_acceptsTheFirstSequence(t *testing.T) {
	t.Parallel()
	position, err := decodeCursor(walletOf(t, walletText), raw(walletText+":1:"+entryText))
	if err != nil {
		t.Fatalf("decodeCursor at the first sequence = %v, want nil", err)
	}
	if position.Sequence != 1 {
		t.Fatalf("sequence = %d, want 1", position.Sequence)
	}
}

func TestDecodeCursor_refusesEveryTokenTheRouteDidNotIssue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		cursor string
	}{
		{name: "a token that is not base64url is refused", cursor: "not base64!"},
		{name: "a token with two parts is refused", cursor: raw(walletText + ":7")},
		{name: "a token with four parts is refused", cursor: raw(walletText + ":7:" + entryText + ":x")},
		{name: "a sequence of zero is refused", cursor: raw(walletText + ":0:" + entryText)},
		{name: "a negative sequence is refused", cursor: raw(walletText + ":-1:" + entryText)},
		{name: "a sequence that is not a number is refused", cursor: raw(walletText + ":seven:" + entryText)},
		{name: "an entry out of format is refused", cursor: raw(walletText + ":7:not-a-uuid")},
		{name: "a wallet out of format is refused", cursor: raw("not-a-uuid:7:" + entryText)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeCursor(walletOf(t, walletText), tc.cursor)
			if !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("decodeCursor = %v, want %v for %s", err, ErrInvalidCursor, tc.name)
			}
		})
	}
}

// A cursor of wallet A presented on wallet B is refused with the very same error
// as garbage, so the refusal says nothing about wallet A and the route never
// pages the wrong ledger from a position that means nothing there.
func TestDecodeCursor_refusesTheCursorOfAnotherWalletTheSameWayAsGarbage(t *testing.T) {
	t.Parallel()
	issued := encodeCursor(walletOf(t, walletText), storage.EntryPosition{Sequence: 7, EntryID: entryOf(t, entryText)})
	_, otherWallet := decodeCursor(walletOf(t, otherWalletText), issued)
	_, garbage := decodeCursor(walletOf(t, otherWalletText), "not base64!")
	if !errors.Is(otherWallet, ErrInvalidCursor) {
		t.Fatalf("decodeCursor of another wallet = %v, want %v", otherWallet, ErrInvalidCursor)
	}
	if otherWallet.Error() != garbage.Error() {
		t.Fatalf("refusal of another wallet = %q, want the same text as garbage, %q", otherWallet, garbage)
	}
}

// raw writes the token the way encodeCursor does, so a case can spell what is
// inside without going through the encoder it is testing.
func raw(text string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(text))
}
