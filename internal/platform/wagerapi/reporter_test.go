package wagerapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
	"github.com/junglegaming/backend-challenge-go/internal/platform/telemetry"
)

func TestNewReporter_logsThroughTheHandlerItWasGiven(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	reporterWriting(&written).Settled(requestOf(t), settled(t, false))
	if written.Len() == 0 {
		t.Fatalf("log = %q, want the line of the settlement", written.String())
	}
}

// The log of a settlement is identifiers and the outcome. The amount and the
// balance the provider moved never reach a line.
func TestSettled_logsTheOutcomeWithNoAmountOrBalance(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	reporterWriting(&written).Settled(requestOf(t), settled(t, false))
	line := written.String()
	if !strings.Contains(line, `"status":"PROCESSED"`) || !strings.Contains(line, `"kind":"BET"`) {
		t.Fatalf("settlement line = %s, want the kind and the status of the outcome", line)
	}
	for _, banned := range []string{"25.00", observedBalance} {
		if strings.Contains(line, banned) {
			t.Fatalf("settlement line = %s, want it without %q", line, banned)
		}
	}
}

// A business rejection is an expected answer: it carries the token of the catalog
// and no stack, because nothing is broken.
func TestRefuse_logsTheTokenOfABusinessRejectionWithNoStack(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	rejected := fmt.Errorf("submit wager: %w", wager.NewRejection(wager.InsufficientFunds, nil))
	reporterWriting(&written).Refuse(httptest.NewRecorder(), requestOf(t), rejected)
	line := written.String()
	if !strings.Contains(line, `"failureCode":"INSUFFICIENT_FUNDS"`) {
		t.Fatalf("rejection line = %s, want the token of the catalog", line)
	}
	if strings.Contains(line, `"stack"`) {
		t.Fatalf("rejection line = %s, want no stack for a rule that answered", line)
	}
}

// An infrastructure failure is what should not happen: it carries the stack and
// never a token.
func TestRefuse_carriesTheStackOfAnInfrastructureFailureWithNoToken(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	broken := fault.Wrap("acquire connection", errors.New("connection refused"))
	reporterWriting(&written).Refuse(httptest.NewRecorder(), requestOf(t), broken)
	line := written.String()
	if !strings.Contains(line, `"stack"`) {
		t.Fatalf("failure line = %s, want the stack of the failure", line)
	}
	if strings.Contains(line, `"failureCode"`) {
		t.Fatalf("failure line = %s, want no token: no rule refused anything", line)
	}
}

func TestRefuse_logsNoAmountCredentialOrBody(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	request := requestOf(t)
	request.Header.Set("Authorization", "Bearer secret-access-token")
	request.Header.Set(idempotencyHeader, "key-1")
	reporterWriting(&written).Refuse(httptest.NewRecorder(), request, fmt.Errorf("submit wager: %w", storage.ErrLostWrite))
	line := written.String()
	for _, banned := range []string{"secret-access-token", "Bearer", "key-1", "25.00"} {
		if strings.Contains(line, banned) {
			t.Fatalf("refusal line = %s, want it without %q", line, banned)
		}
	}
}

// The class decides the span and the stack, and not the number: a retryable answer
// shares its number with an outage, so reading the number would mark the span of a
// request that only has to be sent again.
func TestRecord_carriesTheStackOnlyForABrokenClass(t *testing.T) {
	t.Parallel()
	broken := fault.Wrap("acquire connection", errors.New("connection refused"))
	for _, tc := range []struct {
		name   string
		class  problem.Class
		broken bool
	}{
		{name: "a business rejection", class: problem.BusinessRejection, broken: false},
		{name: "invalid input", class: problem.InvalidInput, broken: false},
		{name: "an absent credential", class: problem.Unauthenticated, broken: false},
		{name: "an identity without permission", class: problem.Unauthorized, broken: false},
		{name: "a retryable answer", class: problem.Retryable, broken: false},
		{name: "an outage", class: problem.Unavailable, broken: true},
		{name: "a defect", class: problem.Internal, broken: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marked, line := recorded(t, tc.class, broken)
			if got := strings.Contains(line, `"stack"`); got != tc.broken {
				t.Fatalf("stack present = %t, want %t for %s", got, tc.broken, tc.name)
			}
			if marked != tc.broken {
				t.Fatalf("span marked as an error = %t, want %t for %s", marked, tc.broken, tc.name)
			}
		})
	}
}

// recorded answers what record left behind for that class: whether the span was
// marked as an error, and the line that reached the handler.
//
// The span is read back from a recorder instead of from the call, because the
// class is what decides it and the number cannot: a retryable answer shares the
// number of an outage.
func recorded(t *testing.T, class problem.Class, err error) (bool, string) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() {
		if closed := provider.Shutdown(context.Background()); closed != nil {
			t.Fatalf("shutdown tracer = %v, want nil", closed)
		}
	})
	ctx, span := provider.Tracer("test").Start(context.Background(), "answer")
	var written bytes.Buffer
	reporterWriting(&written).record(requestOf(t).WithContext(ctx), err, problem.Of(class))
	span.End()
	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want exactly 1", len(ended))
	}
	return ended[0].Status().Code == codes.Error, written.String()
}

// A defect never crossed an I/O boundary, so nothing captured its frames on the
// way up. This border is where it is first seen as a failure, and that is where
// the capture belongs.
func TestStackOf_capturesTheFramesOfAChainThatCarriesNone(t *testing.T) {
	t.Parallel()
	bare := errors.New("submitwager: kind is not settled by this use case")
	if frames := fault.Stack(bare); len(frames) > 0 {
		t.Fatalf("frames of a bare error = %d, want none before the border", len(frames))
	}
	if frames := stackOf(bare); len(frames) == 0 {
		t.Fatalf("frames captured at the border = %d, want the stack of the defect", len(frames))
	}
}

// One failure keeps one stack: a chain that already carries frames is not captured
// again here.
func TestStackOf_keepsTheStackTheChainAlreadyCarries(t *testing.T) {
	t.Parallel()
	wrapped := fault.Wrap("acquire connection", errors.New("connection refused"))
	carried, answered := fault.Stack(wrapped), stackOf(wrapped)
	if len(answered) != len(carried) {
		t.Fatalf("frames = %d, want the %d the chain already carried", len(answered), len(carried))
	}
}

// The line carries the status always, and the two optional attributes only when
// they hold something.
func TestRefused_leavesOutTheAttributesThatHoldNothing(t *testing.T) {
	t.Parallel()
	var bare bytes.Buffer
	reporterWriting(&bare).refused(requestOf(t), problem.Details{Status: http.StatusBadRequest}, nil)
	if !strings.Contains(bare.String(), `"status":"400"`) {
		t.Fatalf("bare line = %s, want the status of the answer", bare.String())
	}
	for _, absent := range []string{`"failureCode"`, `"stack"`} {
		if strings.Contains(bare.String(), absent) {
			t.Fatalf("bare line = %s, want %s left out when it holds nothing", bare.String(), absent)
		}
	}
	var full bytes.Buffer
	reporterWriting(&full).refused(requestOf(t), problem.Details{Status: 422, FailureCode: "INSUFFICIENT_FUNDS"}, []string{"frame"})
	for _, present := range []string{`"failureCode":"INSUFFICIENT_FUNDS"`, `"stack"`} {
		if !strings.Contains(full.String(), present) {
			t.Fatalf("full line = %s, want %s carried when it holds something", full.String(), present)
		}
	}
}

// reporterWriting logs through the same allow list the process uses, so what the
// test reads is what Loki would receive.
func reporterWriting(sink *bytes.Buffer) *Reporter {
	return NewReporter(slog.New(telemetry.Allow(slog.NewJSONHandler(sink, nil))))
}

func requestOf(t *testing.T) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(context.Background(), http.MethodPost, Route, strings.NewReader(submission(nil)))
}
