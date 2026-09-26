package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// A unit of work over a pool the process never opened refuses before it opens a
// transaction, and it refuses as a failure of infrastructure: the work never runs,
// so there is no rejection of a rule to confuse it with.
func TestWithin_refusesBeforeTheWorkWhenThePoolIsNotOpen(t *testing.T) {
	t.Parallel()
	ran := false
	err := NewUnitOfWork(&Pool{}).Within(context.Background(), func(storage.Tx) error {
		ran = true
		return nil
	})
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Within over a closed pool = %v, want ErrPoolClosed in the chain", err)
	}
	if ran {
		t.Fatalf("the work ran = true, want false: a pool that is not open decides nothing")
	}
	if frames := fault.Stack(err); len(frames) == 0 {
		t.Fatalf("frames of the refusal = 0, want the stack of an infrastructure failure")
	}
}
