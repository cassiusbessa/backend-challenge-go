package walletapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

func TestReconcile_answersTheConsistentReportWithoutTheTwoFields(t *testing.T) {
	t.Parallel()
	recorder := serve(Reconcile(&reconciler{report: consistentReport(t)}, quietReporter()), reconciliationRequestOf(walletText))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := decodeReconciliation(t, recorder)
	if !body.Consistent {
		t.Fatalf("consistent = false, want true")
	}
	if body.StoredBalance.Amount != "1025.00" || body.LedgerBalance.Amount != "1025.00" || body.StoredBalance.Currency != "BRL" {
		t.Fatalf("balances = %+v and %+v, want 1025.00 BRL on both sides", body.StoredBalance, body.LedgerBalance)
	}
	if body.WalletID != walletText || body.Version != 4 || body.EntryCount != 3 || body.LastSequence != 3 {
		t.Fatalf("report = %s at version %d with %d entries up to %d, want %s at 4 with 3 up to 3", body.WalletID, body.Version, body.EntryCount, body.LastSequence, walletText)
	}
	raw := recorder.Body.String()
	for _, absent := range []string{"divergences", "firstBreakSequence"} {
		if strings.Contains(raw, absent) {
			t.Fatalf("body = %s, want it without %q for a consistent wallet", raw, absent)
		}
	}
}

func TestReconcile_answersTheDivergentReportWithTheTokensAndTheBreak(t *testing.T) {
	t.Parallel()
	recorder := serve(Reconcile(&reconciler{report: divergentReport(t)}, quietReporter()), reconciliationRequestOf(walletText))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a divergence is a result, not a failure", recorder.Code)
	}
	body := decodeReconciliation(t, recorder)
	if body.Consistent {
		t.Fatalf("consistent = true, want false")
	}
	if len(body.Divergences) != 2 || body.Divergences[0] != "BALANCE_MISMATCH" || body.Divergences[1] != "CHAIN_BREAK" {
		t.Fatalf("divergences = %v, want BALANCE_MISMATCH and CHAIN_BREAK", body.Divergences)
	}
	if body.FirstBreakSequence != 2 {
		t.Fatalf("firstBreakSequence = %d, want 2", body.FirstBreakSequence)
	}
	if body.StoredBalance.Amount != "2000.00" || body.LedgerBalance.Amount != "1025.00" {
		t.Fatalf("balances = %+v stored and %+v rebuilt, want 2000.00 and 1025.00", body.StoredBalance, body.LedgerBalance)
	}
}

func TestReconcile_answers404ForAWalletThatDoesNotExist(t *testing.T) {
	t.Parallel()
	recorder := serve(Reconcile(&reconciler{err: storage.ErrWalletNotFound}, quietReporter()), reconciliationRequestOf(walletText))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	if recorder.Header().Get("Content-Type") != problem.MediaType {
		t.Fatalf("content type of the absence = %s, want %s", recorder.Header().Get("Content-Type"), problem.MediaType)
	}
}

func TestReconcile_refusesAnIdentityOutOfFormatWithoutAskingTheUseCase(t *testing.T) {
	t.Parallel()
	asked := &reconciler{report: consistentReport(t)}
	recorder := serve(Reconcile(asked, quietReporter()), reconciliationRequestOf("not-a-uuid"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status of an identity out of format = %d, want 400", recorder.Code)
	}
	if asked.calls != 0 {
		t.Fatalf("use case calls = %d, want 0", asked.calls)
	}
}

type reconciler struct {
	report reconcilewallet.Report
	err    error
	calls  int
}

func (r *reconciler) Reconcile(context.Context, identity.WalletID) (reconcilewallet.Report, error) {
	r.calls++
	if r.err != nil {
		return reconcilewallet.Report{}, r.err
	}
	return r.report, nil
}

type externalReconciliation struct {
	WalletID           string        `json:"walletId"`
	StoredBalance      externalMoney `json:"storedBalance"`
	LedgerBalance      externalMoney `json:"ledgerBalance"`
	Version            int64         `json:"version"`
	EntryCount         int64         `json:"entryCount"`
	LastSequence       int64         `json:"lastSequence"`
	Consistent         bool          `json:"consistent"`
	Divergences        []string      `json:"divergences"`
	FirstBreakSequence int64         `json:"firstBreakSequence"`
}

func reconciliationRequestOf(walletID string) *http.Request {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/wallets/"+walletID+"/reconciliation", nil)
	request.SetPathValue("walletId", walletID)
	return request
}

func decodeReconciliation(t *testing.T, recorder *httptest.ResponseRecorder) externalReconciliation {
	t.Helper()
	var body externalReconciliation
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal reconciliation = %v, want nil", err)
	}
	return body
}

func consistentReport(t *testing.T) reconcilewallet.Report {
	t.Helper()
	return reconcilewallet.Report{
		WalletID:      walletOf(t),
		StoredBalance: moneyOf(t, "1025.00"),
		LedgerBalance: moneyOf(t, "1025.00"),
		Version:       4,
		EntryCount:    3,
		LastSequence:  3,
		Consistent:    true,
	}
}

func divergentReport(t *testing.T) reconcilewallet.Report {
	t.Helper()
	report := consistentReport(t)
	report.StoredBalance = moneyOf(t, "2000.00")
	report.Consistent = false
	report.Divergences = []reconcilewallet.Divergence{reconcilewallet.BalanceMismatch, reconcilewallet.ChainBreak}
	report.FirstBreakSequence = 2
	return report
}
