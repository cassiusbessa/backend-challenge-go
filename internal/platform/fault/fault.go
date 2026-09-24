// Package fault wraps an infrastructure failure with the operation that saw it
// and the stack captured once.
//
// A business rejection never comes through here: there is no stack for a
// balance that does not cover a debit.
package fault

import (
	"errors"
	"fmt"
	"runtime"
)

// depth bounds the captured frames. Anything deeper than this is the runtime
// entry point, which tells nothing about the failure.
const depth = 32

// skipWrap drops runtime.Callers, capture and Wrap from the frames, so the
// first one is the boundary that saw the failure.
const skipWrap = 3

// Error is an infrastructure failure carrying the operation chain and the stack
// of the boundary that saw it first. The zero value is not used: Wrap is the
// only constructor.
type Error struct {
	op    string
	err   error
	stack []uintptr
}

// Wrap adds the operation to the chain and captures the stack only when the
// chain does not carry one yet. One failure, one stack: the boundary above only
// names its own operation.
//
// The operation is a short lowercase verb with no data — "acquire connection",
// not the connection string.
func Wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{op: op, err: err, stack: capture(err)}
}

func capture(err error) []uintptr {
	var carried *Error
	if errors.As(err, &carried) {
		return nil
	}
	pcs := make([]uintptr, depth)
	return pcs[:runtime.Callers(skipWrap, pcs)]
}

func (e *Error) Error() string {
	return e.op + ": " + e.err.Error()
}

func (e *Error) Unwrap() error {
	return e.err
}

// Stack answers the formatted frames of the failure, and an empty slice when
// the chain carries none.
func Stack(err error) []string {
	for err != nil {
		var carried *Error
		if !errors.As(err, &carried) {
			return nil
		}
		if len(carried.stack) > 0 {
			return frames(carried.stack)
		}
		err = carried.err
	}
	return nil
}

func frames(pcs []uintptr) []string {
	out := make([]string, 0, len(pcs))
	iterator := runtime.CallersFrames(pcs)
	for {
		frame, more := iterator.Next()
		out = append(out, fmt.Sprintf("%s %s:%d", frame.Function, frame.File, frame.Line))
		if !more {
			return out
		}
	}
}
