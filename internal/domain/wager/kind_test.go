package wager

import (
	"errors"
	"testing"
)

func TestString_namesTheKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind Kind
		want string
	}{
		{kind: KindOpening, want: "OPENING"},
		{kind: KindBet, want: "BET"},
		{kind: KindWin, want: "WIN"},
		{kind: KindLoss, want: "LOSS"},
		{kind: KindRefund, want: "REFUND"},
		{kind: KindRollback, want: "ROLLBACK"},
		{kind: noKind, want: ""},
		{kind: Kind(200), want: ""},
		{kind: kindCount, want: ""},
	}
	for _, testCase := range cases {
		if got := testCase.kind.String(); got != testCase.want {
			t.Fatalf("kind %d writes %q, want %q", testCase.kind, got, testCase.want)
		}
	}
}

func TestParseKind_readsTheKindBack(t *testing.T) {
	t.Parallel()
	kind, err := ParseKind("ROLLBACK")
	if err != nil {
		t.Fatalf("ParseKind error = %v, want nil", err)
	}
	if kind != KindRollback {
		t.Fatalf("parsed kind = %s, want ROLLBACK", kind)
	}
	unknown, err := ParseKind("CASHOUT")
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("ParseKind(\"CASHOUT\") error = %v, want ErrUnknownKind", err)
	}
	if !unknown.IsZero() {
		t.Fatalf("a refused kind produced %s, want the zero value", unknown)
	}
}

func TestParseKind_refusesTheEmptyToken(t *testing.T) {
	t.Parallel()
	empty, err := ParseKind("")
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("the empty token error = %v, want ErrUnknownKind", err)
	}
	if !empty.IsZero() {
		t.Fatalf("the empty token produced %s, want the zero value", empty)
	}
}

func TestIsReversal_answersForRefundAndRollback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind Kind
		want bool
	}{
		{kind: KindOpening, want: false},
		{kind: KindBet, want: false},
		{kind: KindWin, want: false},
		{kind: KindLoss, want: false},
		{kind: KindRefund, want: true},
		{kind: KindRollback, want: true},
	}
	for _, testCase := range cases {
		if got := testCase.kind.IsReversal(); got != testCase.want {
			t.Fatalf("IsReversal on %s = %t, want %t", testCase.kind, got, testCase.want)
		}
	}
}

func TestMovesMoney_answersFalseOnlyForLoss(t *testing.T) {
	t.Parallel()
	for _, kind := range []Kind{KindOpening, KindBet, KindWin, KindRefund, KindRollback} {
		if !kind.MovesMoney() {
			t.Fatalf("MovesMoney on %s = false, want true", kind)
		}
	}
	if KindLoss.MovesMoney() {
		t.Fatalf("MovesMoney on LOSS = true, want false")
	}
}
