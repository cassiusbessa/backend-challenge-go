package walletapi

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
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
