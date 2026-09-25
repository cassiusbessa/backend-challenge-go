package wagerapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/authz"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

// versionedMap is the map the challenge ships, and the one the realm fixes.
const versionedMap = "../../../deploy/local/clients.yaml"

func TestSubmit_answers201PointingAtTheCreatedResource(t *testing.T) {
	t.Parallel()
	use := &submitter{result: settled(t, false)}
	recorder := post(t, use, "provider-a", submission(nil))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != Route+"/"+transactionID {
		t.Fatalf("location = %s, want %s", got, Route+"/"+transactionID)
	}
	assertCreated(t, recorder)
}

func assertCreated(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	answered := answerOf(t, recorder)
	if answered.ID != transactionID || answered.Status != "PROCESSED" || answered.Kind != "BET" {
		t.Fatalf("answered = %+v, want the created transaction", answered)
	}
	assertFirstCompletion(t, answered)
}

func assertFirstCompletion(t *testing.T, answered externalTransaction) {
	t.Helper()
	if answered.ObservedBalance.Amount != "975.00" {
		t.Fatalf("observed balance = %s, want 975.00", answered.ObservedBalance.Amount)
	}
	if answered.IdempotentReplay {
		t.Fatalf("idempotentReplay = %t, want false on the first completion", answered.IdempotentReplay)
	}
}

func TestSubmit_answers200OnTheMarkedReplay(t *testing.T) {
	t.Parallel()
	use := &submitter{result: settled(t, true)}
	recorder := post(t, use, "provider-a", submission(nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status of the marked replay = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != "" {
		t.Fatalf("location = %s, want none: the replay created nothing", got)
	}
	answered := answerOf(t, recorder)
	if !answered.IdempotentReplay {
		t.Fatalf("idempotentReplay = %t, want true on the replay", answered.IdempotentReplay)
	}
	if answered.ObservedBalance.Amount != "975.00" {
		t.Fatalf("observed balance = %s, want the one of the original completion", answered.ObservedBalance.Amount)
	}
}

// Money crosses the contract as text, in both directions.
func TestSubmit_answersMoneyAsADecimalStringOfTwoPlaces(t *testing.T) {
	t.Parallel()
	use := &submitter{result: settled(t, false)}
	raw := post(t, use, "provider-a", submission(nil)).Body.String()
	for _, want := range []string{`"amount":"25.00"`, `"amount":"975.00"`, `"currency":"BRL"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("body = %s, want it carrying %s", raw, want)
		}
	}
}

func TestSubmit_answersProblemDetailsWithTheTokenOfARejection(t *testing.T) {
	t.Parallel()
	use := &submitter{err: fmt.Errorf("submit wager: %w", wager.NewRejection(wager.InsufficientFunds, nil))}
	recorder := post(t, use, "provider-a", submission(nil))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != problem.MediaType {
		t.Fatalf("content type = %s, want %s", got, problem.MediaType)
	}
	refusal := refusalOf(t, recorder)
	if refusal.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Fatalf("failureCode = %s, want INSUFFICIENT_FUNDS", refusal.FailureCode)
	}
	if refusal.IdempotentReplay {
		t.Fatalf("idempotentReplay = %t, want false on the first refusal of the route", refusal.IdempotentReplay)
	}
}

// A refusal that was already recorded answers the token of the first one and the
// marker beside it.
func TestSubmit_marksTheReplayedRejectionBesideItsToken(t *testing.T) {
	t.Parallel()
	recorded := recordedRefusal{rejection: wager.NewRejection(wager.InsufficientFunds, nil)}
	use := &submitter{err: fmt.Errorf("submit wager: %w", recorded)}
	refusal := refusalOf(t, post(t, use, "provider-a", submission(nil)))
	if refusal.FailureCode != "INSUFFICIENT_FUNDS" || !refusal.IdempotentReplay {
		t.Fatalf("refusal = %+v, want the token and the marker", refusal)
	}
}

func TestSubmit_answersUnavailabilityWithoutATokenOnAFailure(t *testing.T) {
	t.Parallel()
	use := &submitter{err: fmt.Errorf("submit wager: %w", storage.ErrLostWrite)}
	recorder := post(t, use, "provider-a", submission(nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	refusal := refusalOf(t, recorder)
	if refusal.FailureCode != "" {
		t.Fatalf("failureCode = %s, want empty: no rule refused the operation", refusal.FailureCode)
	}
}

// What authorizes is the client of the token. A body speaking for somebody else is
// refused before the use case, so no movement and no row can follow.
func TestSubmit_refusesABodyThatNamesAnotherProviderWithoutReachingTheUseCase(t *testing.T) {
	t.Parallel()
	use := &submitter{result: settled(t, false)}
	recorder := post(t, use, "provider-b", submission(nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status of a body naming another provider = %d, want 403", recorder.Code)
	}
	if use.calls != 0 {
		t.Fatalf("use case calls for another provider = %d, want 0", use.calls)
	}
	if got := refusalOf(t, recorder).FailureCode; got != "" {
		t.Fatalf("failureCode = %q, want none: no rule refused the operation", got)
	}
}

func TestSubmit_refusesARequestWithNoResolvedClient(t *testing.T) {
	t.Parallel()
	use := &submitter{result: settled(t, false)}
	recorder := post(t, use, "", submission(nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status of a submission with no resolved client = %d, want 403", recorder.Code)
	}
	if use.calls != 0 {
		t.Fatalf("use case calls with no resolved client = %d, want 0", use.calls)
	}
}

func TestSubmit_refusesInvalidInputWithoutReachingTheUseCase(t *testing.T) {
	t.Parallel()
	use := &submitter{result: settled(t, false)}
	recorder := post(t, use, "provider-a", "{")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status of invalid input on the submission = %d, want 400", recorder.Code)
	}
	if use.calls != 0 {
		t.Fatalf("use case calls for invalid input = %d, want 0", use.calls)
	}
}

func TestRead_answers200WithTheRecordedOutcome(t *testing.T) {
	t.Parallel()
	rows := &reader{view: view(t, wager.Processed, "975.00")}
	recorder := get(t, rows, "provider-a", transactionID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status of the recorded outcome = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %s, want application/json", got)
	}
	answered := answerOf(t, recorder)
	if answered.Status != "PROCESSED" || answered.ObservedBalance.Amount != "975.00" {
		t.Fatalf("answered = %+v, want the recorded outcome", answered)
	}
}

// The read of a transaction closed by a rule answers 200 with its token: the read
// succeeded, and problem details is reserved for the refusal of the request.
func TestRead_answers200WithTheTokenOfARejectedTransaction(t *testing.T) {
	t.Parallel()
	rejected := view(t, wager.Rejected, "")
	rejected.FailureCode = wager.InsufficientFunds
	recorder := get(t, &reader{view: rejected}, "provider-a", transactionID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status of a rejected transaction = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got == problem.MediaType {
		t.Fatalf("content type = %s, want the read not to answer problem details", got)
	}
	answered := answerOf(t, recorder)
	if answered.Status != "REJECTED" || answered.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Fatalf("answered = %+v, want REJECTED with its token", answered)
	}
	if strings.Contains(recorder.Body.String(), "observedBalance") {
		t.Fatalf("body = %s, want no balance for a transaction that carries none", recorder.Body.String())
	}
}

// The handler is given the same absence for a transaction of another provider and
// for one that does not exist, so the two answers are the same bytes.
func TestRead_answersTheSameAbsenceForAnotherProviderAndForNothing(t *testing.T) {
	t.Parallel()
	alien := get(t, &reader{err: storage.ErrTransactionNotFound}, "provider-b", transactionID)
	absent := get(t, &reader{err: storage.ErrTransactionNotFound}, "provider-b", transactionID)
	if alien.Code != http.StatusNotFound || absent.Code != http.StatusNotFound {
		t.Fatalf("statuses = %d and %d, want both 404", alien.Code, absent.Code)
	}
	if alien.Body.String() != absent.Body.String() {
		t.Fatalf("bodies = %s and %s, want them the same", alien.Body, absent.Body)
	}
	for _, banned := range []string{"PROCESSED", "REJECTED", "amount", "observedBalance"} {
		if strings.Contains(alien.Body.String(), banned) {
			t.Fatalf("body = %s, want it without %q", alien.Body, banned)
		}
	}
}

func TestRead_asksTheReaderForTheProviderOfTheToken(t *testing.T) {
	t.Parallel()
	rows := &reader{view: view(t, wager.Processed, "975.00")}
	get(t, rows, "provider-b", transactionID)
	if rows.provider.String() != "provider-b" {
		t.Fatalf("asked for the provider %s, want provider-b", rows.provider)
	}
	if rows.id.String() != transactionID {
		t.Fatalf("asked for the transaction %s, want %s", rows.id, transactionID)
	}
}

func TestRead_refusesAnIdentityOutOfFormatWithoutReachingTheReadModel(t *testing.T) {
	t.Parallel()
	rows := &reader{view: view(t, wager.Processed, "975.00")}
	recorder := get(t, rows, "provider-a", "not-a-uuid")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status of an identity out of format = %d, want 400", recorder.Code)
	}
	if rows.calls != 0 {
		t.Fatalf("read model calls for an identity out of format = %d, want 0", rows.calls)
	}
}

func TestRead_refusesARequestWithNoResolvedClient(t *testing.T) {
	t.Parallel()
	rows := &reader{view: view(t, wager.Processed, "975.00")}
	recorder := get(t, rows, "", transactionID)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status of a read with no resolved client = %d, want 403", recorder.Code)
	}
	if rows.calls != 0 {
		t.Fatalf("read model calls with no resolved client = %d, want 0", rows.calls)
	}
}

// externalMoney is money as the client reads it: two strings, never a JSON number.
type externalMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type externalTransaction struct {
	ID                    string        `json:"id"`
	Kind                  string        `json:"kind"`
	Status                string        `json:"status"`
	ProviderID            string        `json:"providerId"`
	ExternalTransactionID string        `json:"externalTransactionId"`
	Money                 externalMoney `json:"money"`
	ObservedBalance       externalMoney `json:"observedBalance"`
	FailureCode           string        `json:"failureCode"`
	IdempotentReplay      bool          `json:"idempotentReplay"`
}

func answerOf(t *testing.T, recorder *httptest.ResponseRecorder) externalTransaction {
	t.Helper()
	var answered externalTransaction
	if err := json.Unmarshal(recorder.Body.Bytes(), &answered); err != nil {
		t.Fatalf("unmarshal of the transaction = %v, want nil: %s", err, recorder.Body)
	}
	return answered
}

func refusalOf(t *testing.T, recorder *httptest.ResponseRecorder) problem.Details {
	t.Helper()
	var refusal problem.Details
	if err := json.Unmarshal(recorder.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("unmarshal of the problem details = %v, want nil: %s", err, recorder.Body)
	}
	return refusal
}

func post(t *testing.T, use Submitter, client, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, Route, strings.NewReader(body))
	request.Header.Set(idempotencyHeader, "key-1")
	return serve(t, client, Submit(use, reporter()), request)
}

func get(t *testing.T, rows Reader, client, id string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, Route+"/"+id, nil)
	request.SetPathValue("transactionId", id)
	return serve(t, client, Read(rows, reporter()), request)
}

// serve runs the handler behind the real guard, because the provider a request
// speaks for is the client of the token and nothing else. An empty client serves
// the handler bare, which is the request that carries no resolved client.
func serve(t *testing.T, client string, handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	behind(t, client, handler).ServeHTTP(recorder, request)
	return recorder
}

func behind(t *testing.T, client string, handler http.Handler) http.Handler {
	t.Helper()
	if client == "" {
		return handler
	}
	clients, err := authz.LoadClients(versionedMap)
	if err != nil {
		t.Fatalf("LoadClients = %v, want nil", err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer good")
		authz.NewGuard(verifier{client: client}, clients).Only(authz.Provider, handler).ServeHTTP(w, r)
	})
}

func reporter() *Reporter {
	return NewReporter(slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

type verifier struct {
	client string
}

func (v verifier) Client(context.Context, string) (string, error) {
	return v.client, nil
}

type submitter struct {
	result submitwager.Result
	err    error
	calls  int
}

func (s *submitter) Submit(_ context.Context, _ submitwager.Command) (submitwager.Result, error) {
	s.calls++
	return s.result, s.err
}

type reader struct {
	view     storage.TransactionView
	err      error
	calls    int
	id       identity.TransactionID
	provider identity.ProviderID
}

func (r *reader) Transaction(_ context.Context, id identity.TransactionID, provider identity.ProviderID) (storage.TransactionView, error) {
	r.calls++
	r.id = id
	r.provider = provider
	if r.err != nil {
		return storage.TransactionView{}, r.err
	}
	return r.view, nil
}

// recordedRefusal is how a use case marks a refusal it had already recorded: it
// wraps the rejection, so the token is still reachable, and reports the replay.
type recordedRefusal struct {
	rejection error
}

func (r recordedRefusal) Error() string {
	return "recorded refusal replayed: " + r.rejection.Error()
}

func (r recordedRefusal) Unwrap() error {
	return r.rejection
}

func (r recordedRefusal) IdempotentReplay() bool {
	return true
}

// observedBalance is the balance of the commit that closed the operation, and the
// one a replay answers again.
const observedBalance = "975.00"

// settled is the outcome of a submission that reached a decision. It is always
// PROCESSED: a rejection comes back from the use case as an error and never as a
// result.
func settled(t *testing.T, replay bool) submitwager.Result {
	t.Helper()
	return submitwager.Result{
		TransactionID:    transactionOf(t),
		Kind:             wager.KindBet,
		Status:           wager.Processed,
		ExternalID:       externalOf(t),
		Amount:           amountOf(t, "25.00"),
		ObservedBalance:  amountOf(t, observedBalance),
		IdempotentReplay: replay,
	}
}

func view(t *testing.T, status wager.Status, observed string) storage.TransactionView {
	t.Helper()
	found := storage.TransactionView{
		ID:         transactionOf(t),
		Kind:       wager.KindBet,
		Status:     status,
		ProviderID: providerNamed(t, "provider-a"),
		ExternalID: externalOf(t),
		Amount:     amountOf(t, "25.00"),
	}
	if observed != "" {
		found.ObservedBalance = amountOf(t, observed)
	}
	return found
}

func amountOf(t *testing.T, amount string) money.Money {
	t.Helper()
	if amount == "" {
		return money.Money{}
	}
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse = %v, want nil", err)
	}
	return parsed
}

func transactionOf(t *testing.T) identity.TransactionID {
	t.Helper()
	id, err := identity.ParseTransactionID(transactionID)
	if err != nil {
		t.Fatalf("ParseTransactionID = %v, want nil", err)
	}
	return id
}

func externalOf(t *testing.T) identity.ExternalTransactionID {
	t.Helper()
	id, err := identity.ParseExternalTransactionID("external-1")
	if err != nil {
		t.Fatalf("ParseExternalTransactionID = %v, want nil", err)
	}
	return id
}

func providerNamed(t *testing.T, text string) identity.ProviderID {
	t.Helper()
	id, err := identity.ParseProviderID(text)
	if err != nil {
		t.Fatalf("ParseProviderID = %v, want nil", err)
	}
	return id
}

// The provider a request speaks for is the client of the token. A request with no
// resolved client speaks for nobody, and that is refused without revealing
// whether anything exists.
func TestProviderOf_answersOnlyForARequestWithAResolvedClient(t *testing.T) {
	t.Parallel()
	bare := httptest.NewRequestWithContext(context.Background(), http.MethodGet, Route, nil)
	if _, err := providerOf(bare); !errors.Is(err, errNoClient) {
		t.Fatalf("providerOf a request with no client = %v, want %v", err, errNoClient)
	}
	resolved := resolvedRequest(t, "provider-a")
	provider, err := providerOf(resolved)
	if err != nil {
		t.Fatalf("providerOf a resolved request = %v, want nil", err)
	}
	if provider != providerNamed(t, "provider-a") {
		t.Fatalf("provider = %s, want the one the token resolved to", provider)
	}
}

// The identifier in the payload authorizes nothing: a body declaring another
// provider is refused before the use case, and therefore before any movement.
func TestSpeaksFor_refusesABodyThatDeclaresAnotherProvider(t *testing.T) {
	t.Parallel()
	bare := httptest.NewRequestWithContext(context.Background(), http.MethodPost, Route, nil)
	resolved := resolvedRequest(t, "provider-a")
	if err := speaksFor(resolved, providerNamed(t, "provider-a")); err != nil {
		t.Fatalf("speaksFor its own provider = %v, want nil", err)
	}
	if err := speaksFor(resolved, providerNamed(t, "provider-b")); !errors.Is(err, errOtherProvider) {
		t.Fatalf("speaksFor another provider = %v, want %v", err, errOtherProvider)
	}
	if err := speaksFor(bare, providerNamed(t, "provider-a")); !errors.Is(err, errNoClient) {
		t.Fatalf("speaksFor with no resolved client = %v, want %v", err, errNoClient)
	}
}

// The status describes the request and not the row: the first completion created
// the resource and points at it, and a replay found it already there.
func TestAnswer_pointsAtTheResourceOnlyOnTheFirstCompletion(t *testing.T) {
	t.Parallel()
	first := httptest.NewRecorder()
	answer(first, settled(t, false))
	if first.Code != http.StatusCreated {
		t.Fatalf("status of the first completion = %d, want 201", first.Code)
	}
	if got := first.Header().Get("Location"); got != Route+"/"+transactionOf(t).String() {
		t.Fatalf("Location of the first completion = %q, want the created resource", got)
	}
	replay := httptest.NewRecorder()
	answer(replay, settled(t, true))
	if replay.Code != http.StatusOK {
		t.Fatalf("status of a replay = %d, want 200", replay.Code)
	}
	if got := replay.Header().Get("Location"); got != "" {
		t.Fatalf("Location of a replay = %q, want none: the replay created nothing", got)
	}
}

// The zero value of Money carries no currency, which is what tells a balance that
// was never observed from a balance of zero.
func TestBalanceOf_answersNothingForABalanceThatWasNeverObserved(t *testing.T) {
	t.Parallel()
	if got := balanceOf(money.Money{}); got != nil {
		t.Fatalf("balanceOf the zero value = %v, want nil", got)
	}
	zero := amountOf(t, "0.00")
	got := balanceOf(zero)
	if got == nil {
		t.Fatalf("balanceOf %s = %v, want the balance itself", zero.Amount(), got)
	}
	if got.Amount() != "0.00" {
		t.Fatalf("balance = %s, want 0.00", got.Amount())
	}
}

// resolvedRequest answers the request as the guard handed it to the handler, which
// is the only way a client reaches the context: the constructor of that value is
// unexported on purpose, so no package outside authz can put one there.
func resolvedRequest(t *testing.T, client string) *http.Request {
	t.Helper()
	var passed *http.Request
	capture := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { passed = r })
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, Route, nil)
	behind(t, client, capture).ServeHTTP(httptest.NewRecorder(), request)
	if passed == nil {
		t.Fatalf("the guard refused %s before the handler, want it resolved", client)
	}
	return passed
}
