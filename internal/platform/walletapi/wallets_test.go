package walletapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/app/openwallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

func TestOpen_answers201WithTheWalletAsItWasCommitted(t *testing.T) {
	t.Parallel()
	recorder := serve(Open(&opener{result: resultOf(t, "1000.00")}, quietReporter()), openRequestOf(validBody))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", recorder.Code)
	}
	assertWalletBody(t, decodeWallet(t, recorder), "1000.00", 1)
}

func TestOpen_writesZeroWithTwoPlaces(t *testing.T) {
	t.Parallel()
	recorder := serve(Open(&opener{result: resultOf(t, "0.00")}, quietReporter()), openRequestOf(validBody))
	if body := decodeWallet(t, recorder); body.Balance.Amount != "0.00" {
		t.Fatalf("balance = %s, want 0.00", body.Balance.Amount)
	}
}

func TestOpen_refusesTheSecondWalletOfThePlayerInTheSameCurrency(t *testing.T) {
	t.Parallel()
	recorder := serve(Open(&opener{err: storage.ErrWalletExists}, quietReporter()), openRequestOf(validBody))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	body := decodeProblem(t, recorder)
	if body.FailureCode != "" {
		t.Fatalf("failureCode = %s, want empty for a duplicate wallet", body.FailureCode)
	}
	if recorder.Header().Get("Content-Type") != problem.MediaType {
		t.Fatalf("content type of the duplicate = %s, want %s", recorder.Header().Get("Content-Type"), problem.MediaType)
	}
}

func TestOpen_refusesInvalidInputWithoutReachingTheUseCase(t *testing.T) {
	t.Parallel()
	called := &opener{result: resultOf(t, "1000.00")}
	recorder := serve(Open(called, quietReporter()), openRequestOf(`{"playerId":"nope","initialBalance":{"amount":"25.005","currency":"BRL"}}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status of an invalid opening = %d, want 400", recorder.Code)
	}
	if called.calls != 0 {
		t.Fatalf("use case calls = %d, want 0 for invalid input", called.calls)
	}
	if body := decodeProblem(t, recorder); body.FailureCode != "" {
		t.Fatalf("failureCode = %s, want empty for invalid input", body.FailureCode)
	}
}

func TestOpen_answers503WhenTheDatabaseIsUnavailable(t *testing.T) {
	t.Parallel()
	recorder := serve(Open(&opener{err: fault.Wrap("acquire connection", errors.New("refused"))}, quietReporter()), openRequestOf(validBody))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if body := decodeProblem(t, recorder); body.FailureCode != "" {
		t.Fatalf("failureCode = %s, want empty for unavailability", body.FailureCode)
	}
}

func TestRead_answersTheStoredBalanceAndVersion(t *testing.T) {
	t.Parallel()
	recorder := serve(Read(&reader{view: viewOf(t, "1000.00", 4)}, quietReporter()), readRequestOf(walletText))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := decodeWallet(t, recorder)
	if body.Balance.Amount != "1000.00" {
		t.Fatalf("balance = %s, want 1000.00", body.Balance.Amount)
	}
	if body.Version != 4 {
		t.Fatalf("version = %d, want 4", body.Version)
	}
}

func TestRead_answers404ForAWalletThatDoesNotExist(t *testing.T) {
	t.Parallel()
	recorder := serve(Read(&reader{err: storage.ErrWalletNotFound}, quietReporter()), readRequestOf(walletText))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	if recorder.Header().Get("Content-Type") != problem.MediaType {
		t.Fatalf("content type of the absence = %s, want %s", recorder.Header().Get("Content-Type"), problem.MediaType)
	}
}

func TestRead_refusesAnIdentityOutOfFormatWithoutAskingTheReadModel(t *testing.T) {
	t.Parallel()
	asked := &reader{view: viewOf(t, "0.00", 1)}
	recorder := serve(Read(asked, quietReporter()), readRequestOf("not-a-uuid"))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status of an identity out of format = %d, want 400", recorder.Code)
	}
	if asked.calls != 0 {
		t.Fatalf("read model calls = %d, want 0", asked.calls)
	}
}

const (
	walletText = "11111111-1111-4111-8111-111111111111"
	playerText = "22222222-2222-4222-8222-222222222222"
	validBody  = `{"playerId":"22222222-2222-4222-8222-222222222222","initialBalance":{"amount":"1000.00","currency":"BRL"}}`
)

type opener struct {
	result openwallet.Result
	err    error
	calls  int
}

func (o *opener) Open(context.Context, openwallet.Command) (openwallet.Result, error) {
	o.calls++
	if o.err != nil {
		return openwallet.Result{}, o.err
	}
	return o.result, nil
}

type reader struct {
	view  storage.WalletView
	err   error
	calls int
}

func (r *reader) Wallet(context.Context, identity.WalletID) (storage.WalletView, error) {
	r.calls++
	if r.err != nil {
		return storage.WalletView{}, r.err
	}
	return r.view, nil
}

// externalMoney is the money of the contract as the client reads it: two
// strings, never a JSON number.
type externalMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type externalWallet struct {
	ID       string        `json:"id"`
	PlayerID string        `json:"playerId"`
	Balance  externalMoney `json:"balance"`
	Version  int64         `json:"version"`
}

func serve(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func readRequestOf(walletID string) *http.Request {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/wallets/"+walletID, nil)
	request.SetPathValue("walletId", walletID)
	return request
}

func quietReporter() *Reporter {
	return NewReporter(slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

// assertWalletBody checks the contract both routes answer: the identities, the
// version and money as a decimal string of two places.
func assertWalletBody(t *testing.T, body externalWallet, amount string, version int64) {
	t.Helper()
	if body.Balance.Amount != amount || body.Balance.Currency != "BRL" {
		t.Fatalf("balance = %+v, want {%s BRL}", body.Balance, amount)
	}
	if body.Version != version {
		t.Fatalf("version = %d, want %d", body.Version, version)
	}
	if body.ID != walletText || body.PlayerID != playerText {
		t.Fatalf("identities = %s and %s, want %s and %s", body.ID, body.PlayerID, walletText, playerText)
	}
}

func decodeWallet(t *testing.T, recorder *httptest.ResponseRecorder) externalWallet {
	t.Helper()
	var body externalWallet
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal wallet = %v, want nil", err)
	}
	return body
}

func decodeProblem(t *testing.T, recorder *httptest.ResponseRecorder) problem.Details {
	t.Helper()
	var body problem.Details
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal problem = %v, want nil", err)
	}
	return body
}

func resultOf(t *testing.T, amount string) openwallet.Result {
	t.Helper()
	balance, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse of the result balance = %v, want nil", err)
	}
	return openwallet.Result{
		WalletID: walletOf(t),
		PlayerID: playerOf(t),
		Balance:  balance,
		Version:  1,
	}
}

func viewOf(t *testing.T, amount string, version int64) storage.WalletView {
	t.Helper()
	balance, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse of the view balance = %v, want nil", err)
	}
	at := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	return storage.WalletView{
		ID:        walletOf(t),
		PlayerID:  playerOf(t),
		Balance:   balance,
		Version:   version,
		CreatedAt: at,
		UpdatedAt: at,
	}
}

func walletOf(t *testing.T) identity.WalletID {
	t.Helper()
	id, err := identity.ParseWalletID(walletText)
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	return id
}

func playerOf(t *testing.T) identity.PlayerID {
	t.Helper()
	id, err := identity.ParsePlayerID(playerText)
	if err != nil {
		t.Fatalf("ParsePlayerID = %v, want nil", err)
	}
	return id
}
