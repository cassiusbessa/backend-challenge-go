package walletapi

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

func TestRefuse_logsNoAmountBalanceCredentialOrBody(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	request := openRequestOf(validBody)
	request.Header.Set("Authorization", "Bearer secret-access-token")
	serveWith(reporterWriting(&written), request)
	line := written.String()
	for _, banned := range []string{"1000.00", "secret-access-token", "Bearer", "playerId", "initialBalance"} {
		if strings.Contains(line, banned) {
			t.Fatalf("log line = %s, want it without %q", line, banned)
		}
	}
}

func TestRefuse_logsTheStatusOfTheRefusal(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	serveWith(reporterWriting(&written), openRequestOf(validBody))
	if !strings.Contains(written.String(), `"status":"409"`) {
		t.Fatalf("log line = %s, want the status of the refusal", written.String())
	}
}

func TestRefuse_logsTheTokenOfABusinessRejection(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	reporter := reporterWriting(&written)
	rejected := fmt.Errorf("open wallet: %w", wager.NewRejection(wager.InsufficientFunds, nil))
	reporter.Refuse(httptest.NewRecorder(), openRequestOf(validBody), rejected)
	if !strings.Contains(written.String(), `"failureCode":"INSUFFICIENT_FUNDS"`) {
		t.Fatalf("log line = %s, want the catalog token", written.String())
	}
}

func TestRefuse_carriesTheStackOnlyForAnInfrastructureFailure(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	reporter := reporterWriting(&written)
	broken := fault.Wrap("acquire connection", errors.New("connection refused"))
	reporter.Refuse(httptest.NewRecorder(), openRequestOf(validBody), broken)
	if !strings.Contains(written.String(), `"stack"`) {
		t.Fatalf("log line = %s, want the stack of the failure", written.String())
	}
}

func TestRefuse_leavesNoStackOnARefusalOfTheContract(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	serveWith(reporterWriting(&written), openRequestOf(validBody))
	if strings.Contains(written.String(), `"stack"`) {
		t.Fatalf("log line = %s, want no stack for a refusal", written.String())
	}
}

func TestOpened_logsTheWalletIdentityAlone(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	reporterWriting(&written).Opened(openRequestOf(validBody), walletOf(t))
	line := written.String()
	if !strings.Contains(line, `"walletId":"`+walletText+`"`) {
		t.Fatalf("log line = %s, want the wallet identity", line)
	}
	if strings.Contains(line, "1000.00") {
		t.Fatalf("log line = %s, want it without the balance", line)
	}
}

// go-observability asks every divergence to log, and the line carries the wallet
// and the tokens: never the stored balance, never the rebuilt one.
func TestDiverged_logsTheWalletAndTheTokensWithoutABalance(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	reporterWriting(&written).Diverged(reconciliationRequestOf(walletText), divergentReport(t))
	line := written.String()
	if !strings.Contains(line, `"walletId":"`+walletText+`"`) {
		t.Fatalf("log line = %s, want the wallet identity", line)
	}
	if !strings.Contains(line, "BALANCE_MISMATCH") || !strings.Contains(line, "CHAIN_BREAK") {
		t.Fatalf("log line = %s, want the two tokens of the divergence", line)
	}
	for _, banned := range []string{"2000.00", "1025.00", "storedBalance", "ledgerBalance"} {
		if strings.Contains(line, banned) {
			t.Fatalf("log line = %s, want it without %q", line, banned)
		}
	}
}

// A consistent wallet leaves no line beyond the access log: there is nothing to
// name, and a line per read would drown the ones that matter.
func TestDiverged_leavesNoLineForAConsistentWallet(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	reporterWriting(&written).Diverged(reconciliationRequestOf(walletText), consistentReport(t))
	if written.Len() != 0 {
		t.Fatalf("log line = %s, want none for a consistent wallet", written.String())
	}
}

// A divergence is a result the read reports and not a failure of the service:
// the span carries the tokens and stays ok, so the error rate of the dashboard
// does not count what the next change measures as a series of its own.
func TestDiverged_leavesTheSpanOk(t *testing.T) {
	t.Parallel()
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() {
		if closed := provider.Shutdown(context.Background()); closed != nil {
			t.Fatalf("shutdown tracer = %v, want nil", closed)
		}
	})
	ctx, span := provider.Tracer("test").Start(context.Background(), "answer")
	quietReporter().Diverged(reconciliationRequestOf(walletText).WithContext(ctx), divergentReport(t))
	span.End()
	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want exactly 1", len(ended))
	}
	if ended[0].Status().Code == codes.Error {
		t.Fatalf("span marked as an error by a divergence, want it left ok")
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
	reporterWriting(&written).record(openRequestOf(validBody).WithContext(ctx), err, problem.Of(class))
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
	bare := errors.New("openwallet: wallet state is not usable")
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

// reporterWriting logs through the same allow list the process uses, so what the
// test reads is what Loki would receive.
func reporterWriting(sink *bytes.Buffer) *Reporter {
	return NewReporter(slog.New(telemetry.Allow(slog.NewJSONHandler(sink, nil))))
}

// serveWith drives the route to a refusal of the contract — the duplicate wallet
// — which is the refusal both routes share.
func serveWith(reporter *Reporter, request *http.Request) {
	Open(&opener{err: storage.ErrWalletExists}, reporter).ServeHTTP(httptest.NewRecorder(), request)
}
