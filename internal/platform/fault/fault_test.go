package fault

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestWrap_readsAsATrailOfOperations(t *testing.T) {
	t.Parallel()
	err := Wrap("submit wager", Wrap("debit wallet", Wrap("acquire connection", errors.New("context deadline exceeded"))))
	want := "submit wager: debit wallet: acquire connection: context deadline exceeded"
	if err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}

func TestWrap_answersNilForNoFailure(t *testing.T) {
	t.Parallel()
	if err := Wrap("acquire connection", nil); err != nil {
		t.Fatalf("Wrap of nil = %v, want nil", err)
	}
}

func TestWrap_keepsTheCauseMatchable(t *testing.T) {
	t.Parallel()
	cause := errors.New("connection refused")
	err := Wrap("publish event", Wrap("acquire connection", cause))
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is = false, want the cause %v to stay matchable", cause)
	}
}

func TestStack_capturesOneStackForOneFailure(t *testing.T) {
	t.Parallel()
	inner := Wrap("acquire connection", errors.New("connection refused"))
	outer := Wrap("debit wallet", inner)
	if len(Stack(inner)) == 0 {
		t.Fatalf("frames of the first boundary = 0, want at least one")
	}
	if got, want := len(Stack(outer)), len(Stack(inner)); got != want {
		t.Fatalf("frames = %d, want %d: the second boundary captures nothing", got, want)
	}
}

func TestStack_namesTheBoundaryThatSawTheFailure(t *testing.T) {
	t.Parallel()
	frames := Stack(Wrap("acquire connection", errors.New("connection refused")))
	if len(frames) == 0 {
		t.Fatalf("frames = 0, want at least one")
	}
	if !strings.Contains(frames[0], "TestStack_namesTheBoundaryThatSawTheFailure") {
		t.Fatalf("first frame = %q, want the calling test", frames[0])
	}
}

func TestStack_answersNothingForARefusalOfBusiness(t *testing.T) {
	t.Parallel()
	rejection := fmt.Errorf("submit wager: %w", errors.New("insufficient funds"))
	if frames := Stack(rejection); len(frames) != 0 {
		t.Fatalf("frames = %d, want 0: a business rejection carries no stack", len(frames))
	}
}

func TestStack_answersNothingForNoFailure(t *testing.T) {
	t.Parallel()
	if frames := Stack(nil); len(frames) != 0 {
		t.Fatalf("frames = %d, want 0", len(frames))
	}
}
