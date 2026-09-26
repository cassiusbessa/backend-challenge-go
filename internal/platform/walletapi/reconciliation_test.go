package walletapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
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
		t.Fatalf("consistent = %t, want true", body.Consistent)
	}
	assertBalances(t, body, "1025.00", "1025.00")
	assertCounters(t, body)
}

func assertBalances(t *testing.T, body externalReconciliation, stored, rebuilt string) {
	t.Helper()
	if body.StoredBalance.Amount != stored || body.CalculatedBalance.Amount != rebuilt || body.StoredBalance.Currency != "BRL" {
		t.Fatalf("balances = %+v stored and %+v calculated, want %s and %s in BRL", body.StoredBalance, body.CalculatedBalance, stored, rebuilt)
	}
}

func assertCounters(t *testing.T, body externalReconciliation) {
	t.Helper()
	if body.WalletID != walletText || body.Version != 4 || body.CheckedEntries != 3 || body.LastSequence != 3 {
		t.Fatalf("report = %s at version %d with %d entries up to %d, want %s at 4 with 3 up to 3", body.WalletID, body.Version, body.CheckedEntries, body.LastSequence, walletText)
	}
}

// The body carries the names of the challenge statement and none of the former
// ones, read from the raw body so a field left out is told from a zero one.
func TestReconcile_answersTheNamesOfTheStatement(t *testing.T) {
	t.Parallel()
	recorder := serve(Reconcile(&reconciler{report: consistentReport(t)}, quietReporter()), reconciliationRequestOf(walletText))
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &fields); err != nil {
		t.Fatalf("unmarshal the fields of the reconciliation = %v, want nil", err)
	}
	for _, name := range []string{"calculatedBalance", "difference", "checkedEntries"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("body = %s, want %q in it", recorder.Body, name)
		}
	}
	for _, former := range []string{"ledgerBalance", "entryCount"} {
		if _, ok := fields[former]; ok {
			t.Fatalf("body = %s, want no former name %q", recorder.Body, former)
		}
	}
}

// The difference leaves with its sign and in the currency of the wallet: zero
// when the two close, and the direction of the drift when they do not.
func TestReconcile_answersTheDifferenceWithItsSign(t *testing.T) {
	t.Parallel()
	below := divergentReport(t)
	below.StoredBalance = moneyOf(t, "925.00")
	// Parse refuses a negative amount, which is the rule of external input; a
	// difference is internal, and it is built the way the use case builds it.
	difference, err := money.FromCents(-10000, below.StoredBalance.Currency())
	if err != nil {
		t.Fatalf("money.FromCents of the negative difference = %v, want nil", err)
	}
	below.Difference = difference
	cases := map[string]struct {
		report reconcilewallet.Report
		want   string
	}{
		"consistent": {report: consistentReport(t), want: "0.00"},
		"above":      {report: divergentReport(t), want: "975.00"},
		"below":      {report: below, want: "-100.00"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := decodeReconciliation(t, serve(Reconcile(&reconciler{report: tc.report}, quietReporter()), reconciliationRequestOf(walletText)))
			if body.Difference.Amount != tc.want || body.Difference.Currency != "BRL" {
				t.Fatalf("difference = %+v, want %s BRL", body.Difference, tc.want)
			}
		})
	}
}

// The route takes no body: one that is sent, even one naming another wallet and
// other balances, answers the same bytes as none at all.
func TestReconcile_answersTheSameWhateverBodyIsSent(t *testing.T) {
	t.Parallel()
	bare := serve(Reconcile(&reconciler{report: consistentReport(t)}, quietReporter()), reconciliationRequestOf(walletText))
	sent := reconciliationRequestOf(walletText)
	sent.Body = io.NopCloser(strings.NewReader(`{"walletId":"` + walletText + `","storedBalance":{"amount":"1.00","currency":"BRL"}}`))
	sent.Header.Set("Content-Type", "application/json")
	withBody := serve(Reconcile(&reconciler{report: consistentReport(t)}, quietReporter()), sent)
	if withBody.Code != bare.Code || withBody.Body.String() != bare.Body.String() {
		t.Fatalf("with a body = %d %s, want the %d %s of none", withBody.Code, withBody.Body, bare.Code, bare.Body)
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
		t.Fatalf("consistent = %t, want false", body.Consistent)
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

// No divergence is nil and not an empty list, which is what keeps the field out
// of the body of a consistent wallet.
func TestTokensOf_answersNilForNoDivergenceAndTheTokensOtherwise(t *testing.T) {
	t.Parallel()
	if none := tokensOf(nil); none != nil {
		t.Fatalf("tokensOf(nil) = %v, want nil", none)
	}
	tokens := tokensOf([]reconcilewallet.Divergence{reconcilewallet.SequenceGap, reconcilewallet.ChainBreak})
	if strings.Join(tokens, ",") != "SEQUENCE_GAP,CHAIN_BREAK" {
		t.Fatalf("tokensOf = %v, want SEQUENCE_GAP and CHAIN_BREAK in order", tokens)
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
	CalculatedBalance  externalMoney `json:"calculatedBalance"`
	Difference         externalMoney `json:"difference"`
	Version            int64         `json:"version"`
	CheckedEntries     int64         `json:"checkedEntries"`
	LastSequence       int64         `json:"lastSequence"`
	Consistent         bool          `json:"consistent"`
	Divergences        []string      `json:"divergences"`
	FirstBreakSequence int64         `json:"firstBreakSequence"`
}

func reconciliationRequestOf(walletID string) *http.Request {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/wallets/"+walletID+"/reconciliation", nil)
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
		Difference:    moneyOf(t, "0.00"),
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
	report.Difference = moneyOf(t, "975.00")
	report.Consistent = false
	report.Divergences = []reconcilewallet.Divergence{reconcilewallet.BalanceMismatch, reconcilewallet.ChainBreak}
	report.FirstBreakSequence = 2
	return report
}
