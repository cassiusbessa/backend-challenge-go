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
	assertConsistentBody(t, decodeReconciliation(t, recorder))
	assertWithoutDivergenceFields(t, recorder.Body.String())
}

func assertConsistentBody(t *testing.T, body externalReconciliation) {
	t.Helper()
	if !body.Consistent {
		t.Fatalf("consistent = false, want true")
	}
	assertBalances(t, body, "1025.00", "1025.00")
	assertCounters(t, body)
}

func assertBalances(t *testing.T, body externalReconciliation, stored, rebuilt string) {
	t.Helper()
	if body.StoredBalance.Amount != stored || body.LedgerBalance.Amount != rebuilt || body.StoredBalance.Currency != "BRL" {
		t.Fatalf("balances = %+v stored and %+v rebuilt, want %s and %s in BRL", body.StoredBalance, body.LedgerBalance, stored, rebuilt)
	}
}

func assertCounters(t *testing.T, body externalReconciliation) {
	t.Helper()
	if body.WalletID != walletText || body.Version != 4 || body.EntryCount != 3 || body.LastSequence != 3 {
		t.Fatalf("report = %s at version %d with %d entries up to %d, want %s at 4 with 3 up to 3", body.WalletID, body.Version, body.EntryCount, body.LastSequence, walletText)
	}
}

// assertWithoutDivergenceFields reads the raw body: a decoded struct cannot tell
// an absent field from an empty one, and absent is what the contract promises.
func assertWithoutDivergenceFields(t *testing.T, raw string) {
	t.Helper()
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
	assertDivergentBody(t, decodeReconciliation(t, recorder))
}

func assertDivergentBody(t *testing.T, body externalReconciliation) {
	t.Helper()
	if body.Consistent {
		t.Fatalf("consistent = true, want false")
	}
	if strings.Join(body.Divergences, ",") != "BALANCE_MISMATCH,CHAIN_BREAK" {
		t.Fatalf("divergences = %v, want BALANCE_MISMATCH and CHAIN_BREAK", body.Divergences)
	}
	if body.FirstBreakSequence != 2 {
		t.Fatalf("firstBreakSequence = %d, want 2", body.FirstBreakSequence)
	}
	assertBalances(t, body, "2000.00", "1025.00")
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
