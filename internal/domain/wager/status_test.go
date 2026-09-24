package wager

import (
	"errors"
	"testing"
)

func TestCanMoveTo_followsTheTableAndNothingElse(t *testing.T) {
	t.Parallel()
	every := []Status{Pending, PendingReference, Processed, Rejected, Failed}
	allowed := map[Status]map[Status]bool{
		Pending:          {Processed: true, Rejected: true, PendingReference: true},
		PendingReference: {Processed: true, Rejected: true, Failed: true},
	}
	for _, from := range every {
		for _, to := range every {
			want := allowed[from][to]
			if got := from.CanMoveTo(to); got != want {
				t.Fatalf("CanMoveTo from %s to %s = %t, want %t", from, to, got, want)
			}
		}
	}
}

func TestIsTerminal_answersForTheThreeClosedStatuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status Status
		want   bool
	}{
		{status: Pending, want: false},
		{status: PendingReference, want: false},
		{status: Processed, want: true},
		{status: Rejected, want: true},
		{status: Failed, want: true},
	}
	for _, testCase := range cases {
		if got := testCase.status.IsTerminal(); got != testCase.want {
			t.Fatalf("IsTerminal on %s = %t, want %t", testCase.status, got, testCase.want)
		}
	}
}

func TestString_namesTheStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status Status
		want   string
	}{
		{status: Pending, want: "PENDING"},
		{status: PendingReference, want: "PENDING_REFERENCE"},
		{status: Processed, want: "PROCESSED"},
		{status: Rejected, want: "REJECTED"},
		{status: Failed, want: "FAILED"},
		{status: noStatus, want: ""},
		{status: Status(200), want: ""},
	}
	for _, testCase := range cases {
		if got := testCase.status.String(); got != testCase.want {
			t.Fatalf("status %d writes %q, want %q", testCase.status, got, testCase.want)
		}
	}
}

func TestParseStatus_readsTheStatusBack(t *testing.T) {
	t.Parallel()
	status, err := ParseStatus("PENDING_REFERENCE")
	if err != nil {
		t.Fatalf("ParseStatus error = %v, want nil", err)
	}
	if status != PendingReference {
		t.Fatalf("parsed status = %s, want PENDING_REFERENCE", status)
	}
	unknown, err := ParseStatus("PROCESSING")
	if !errors.Is(err, ErrUnknownStatus) {
		t.Fatalf("ParseStatus(\"PROCESSING\") error = %v, want ErrUnknownStatus", err)
	}
	if !unknown.IsZero() {
		t.Fatalf("a refused status produced %s, want the zero value", unknown)
	}
}
