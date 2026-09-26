package listledger

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
)

// nilEntryText is the one identity the UUID parser accepts and the ledger never
// carries.
const nilEntryText = "00000000-0000-0000-0000-000000000000"

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

// The shape of the envelope is what decodeCursor owns: the encoding and the
// three parts. What each part has to be belongs to issuedFor and positionOf,
// and is asserted there.
func TestDecodeCursor_refusesATokenThatIsNotTheEnvelopeItIssues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		cursor string
	}{
		{name: "a token that is not base64url is refused", cursor: "not base64!"},
		{name: "a token with two parts is refused", cursor: raw(walletText + ":7")},
		{name: "a token with four parts is refused", cursor: raw(walletText + ":7:" + entryText + ":x")},
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

func TestIssuedFor_answersOnlyForTheWalletNamedInTheToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want bool
	}{
		{name: "the wallet of the URL", text: walletText, want: true},
		{name: "another wallet", text: otherWalletText, want: false},
		{name: "a wallet out of format", text: "not-a-uuid", want: false},
		{name: "no wallet at all", text: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is answered", func(t *testing.T) {
			if got := issuedFor(walletOf(t, walletText), tc.text); got != tc.want {
				t.Fatalf("issuedFor %s = %t, want %t", tc.name, got, tc.want)
			}
		})
	}
}

// The first sequence of a ledger is one, and a cursor pointing at it is a
// cursor the route issues after a page of one entry: the boundary is accepted.
func TestPositionOf_acceptsTheFirstSequence(t *testing.T) {
	t.Parallel()
	position, err := positionOf("1", entryText)
	if err != nil {
		t.Fatalf("positionOf at the first sequence = %v, want nil", err)
	}
	if position.Sequence != 1 || position.EntryID != entryOf(t, entryText) {
		t.Fatalf("position = %+v, want the first sequence and the entry of the token", position)
	}
}

// The nil identifier is the one shape the UUID parser accepts and the ledger
// never carries: it would compare below every row of the sequence it names, and
// the page would answer an entry the client has already seen.
func TestPositionOf_refusesEveryPairTheRouteDidNotIssue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		sequence string
		entry    string
	}{
		{name: "a sequence of zero", sequence: "0", entry: entryText},
		{name: "a negative sequence", sequence: "-1", entry: entryText},
		{name: "a sequence that is not a number", sequence: "seven", entry: entryText},
		{name: "a sequence past what int64 holds", sequence: "9223372036854775808", entry: entryText},
		{name: "an entry out of format", sequence: "7", entry: "not-a-uuid"},
		{name: "the nil entry identifier", sequence: "7", entry: nilEntryText},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			position, err := positionOf(tc.sequence, tc.entry)
			if !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("positionOf with %s = %v, want %v", tc.name, err, ErrInvalidCursor)
			}
			if position != (storage.EntryPosition{}) {
				t.Fatalf("position answered with %s = %+v, want the zero value", tc.name, position)
			}
		})
	}
}

// raw writes the token the way encodeCursor does, so a case can spell what is
// inside without going through the encoder it is testing.
func raw(text string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(text))
}
