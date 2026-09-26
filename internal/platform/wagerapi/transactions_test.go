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
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/authz"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
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
	if answered.TransactionID != transactionID || answered.Status != "PROCESSED" || answered.Kind != "BET" {
		t.Fatalf("answered = %+v, want the created transaction", answered)
	}
	assertFirstCompletion(t, answered)
}

func assertFirstCompletion(t *testing.T, answered externalTransaction) {
	t.Helper()
	if answered.Balance.Amount != "975.00" {
		t.Fatalf("balance = %s, want 975.00", answered.Balance.Amount)
	}
	if answered.IdempotentReplay {
		t.Fatalf("idempotentReplay = %t, want false on the first completion", answered.IdempotentReplay)
	}
}

// The operation that was accepted and is waiting answers a code of its own: the
// row is durable and nothing moved, so it is neither the 201 of a completion nor
// problem details, and there is no observed balance to answer.
func TestSubmit_answers202PointingAtTheResourceOfAWaitingOperation(t *testing.T) {
	t.Parallel()
	use := &submitter{result: waiting(t)}
	recorder := post(t, use, "provider-a", submission(nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status of an operation that waits = %d, want 202", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); got != Route+"/"+transactionID {
		t.Fatalf("location of the wait = %s, want %s", got, Route+"/"+transactionID)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %s, want application/json and not problem details", got)
	}
	assertWaitAnswered(t, recorder)
}

// The body of a wait names the status and carries nothing a decision would have
// put there: no token, because no rule refused it, and no balance, because no
// commit closed it.
func assertWaitAnswered(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	answered := answerOf(t, recorder)
	if answered.Status != "PENDING_REFERENCE" {
		t.Fatalf("status = %s, want PENDING_REFERENCE", answered.Status)
	}
	if answered.FailureCode != "" {
		t.Fatalf("failureCode = %s, want none: no rule refused the operation", answered.FailureCode)
	}
	assertNoBalance(t, recorder)
}

// assertNoBalance reads the body itself: an absent balance leaves the field out
// altogether, which a decoded zero value cannot tell from a balance of zero.
func assertNoBalance(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if _, ok := fieldsOf(t, recorder)["balance"]; ok {
		t.Fatalf("body = %s, want no balance for an outcome that carries none", recorder.Body.String())
	}
}

// The replay of a wait answers 200 like any other replay, marked, and still with
// no balance behind it.
func TestSubmit_answers200OnTheReplayOfAWait(t *testing.T) {
	t.Parallel()
	replayed := waiting(t)
	replayed.IdempotentReplay = true
	recorder := post(t, &submitter{result: replayed}, "provider-a", submission(nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status of the replayed wait = %d, want 200", recorder.Code)
	}
	answered := answerOf(t, recorder)
	if answered.Status != "PENDING_REFERENCE" || !answered.IdempotentReplay {
		t.Fatalf("answered = %+v, want the wait marked as a replay", answered)
	}
	assertNoBalance(t, recorder)
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
	if answered.Balance.Amount != "975.00" {
		t.Fatalf("balance = %s, want the one of the original completion", answered.Balance.Amount)
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
	if answered.Status != "PROCESSED" || answered.Balance.Amount != "975.00" {
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
	assertNoBalance(t, recorder)
}

// A transaction still waiting reads back as itself: the read concluded, so it is
// 200 with that status, it carries no balance, and it asks the read model for
// nothing but the row.
func TestRead_answers200WithTheWaitAndNoBalance(t *testing.T) {
	t.Parallel()
	rows := &reader{view: view(t, wager.PendingReference, "")}
	recorder := get(t, rows, "provider-a", transactionID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status of a waiting transaction = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got == problem.MediaType {
		t.Fatalf("content type of a waiting transaction = %s, want it not to be problem details", got)
	}
	answered := answerOf(t, recorder)
	if answered.Status != "PENDING_REFERENCE" || answered.FailureCode != "" {
		t.Fatalf("answered = %+v, want PENDING_REFERENCE with no token", answered)
	}
	assertNoBalance(t, recorder)
	if rows.calls != 1 {
		t.Fatalf("read model calls = %d, want the 1 read and nothing else", rows.calls)
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
	for _, banned := range []string{"PROCESSED", "REJECTED", "amount", "balance"} {
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

// A segment out of format is invalid input naming the field, and it is refused
// before any query, so the refusal reveals nothing.
func TestReadByExternal_refusesEachSegmentOutOfFormatWithoutReachingTheReadModel(t *testing.T) {
	t.Parallel()
	for field, segments := range map[string][2]string{
		"providerId":            {" ", "external-1"},
		"externalTransactionId": {"provider-a", " "},
	} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			rows := &reader{view: view(t, wager.Processed, "975.00")}
			recorder := getExternal(t, rows, "provider-a", segments[0], segments[1])
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status of a blank %s = %d, want 400", field, recorder.Code)
			}
			if got := refusalOf(t, recorder).Detail; !strings.HasPrefix(got, field) {
				t.Fatalf("detail = %q, want it naming %s", got, field)
			}
			if rows.calls != 0 {
				t.Fatalf("read model calls for a blank %s = %d, want 0", field, rows.calls)
			}
		})
	}
}

// The provider in the URL authorizes nothing. Another one answers what an
// identifier nobody sent answers, and the read model is never asked, so no branch
// could learn that the record exists.
func TestReadByExternal_answersTheAbsenceForAnotherProviderInTheURLWithoutReachingTheReadModel(t *testing.T) {
	t.Parallel()
	rows := &reader{view: view(t, wager.Processed, "975.00")}
	alien := getExternal(t, rows, "provider-b", "provider-a", "external-1")
	if rows.calls != 0 {
		t.Fatalf("read model calls for another provider in the URL = %d, want 0", rows.calls)
	}
	nobody := &reader{err: storage.ErrTransactionNotFound}
	absent := getExternal(t, nobody, "provider-a", "provider-a", "external-1")
	if nobody.calls != 1 {
		t.Fatalf("read model calls for the own provider = %d, want the 1 read that found nothing", nobody.calls)
	}
	if alien.Code != http.StatusNotFound || absent.Code != http.StatusNotFound {
		t.Fatalf("another provider = %d and nobody sent = %d, want both 404", alien.Code, absent.Code)
	}
	if alien.Body.String() != absent.Body.String() {
		t.Fatalf("another provider = %s and nobody sent = %s, want the same bytes", alien.Body, absent.Body)
	}
}

func TestReadByExternal_answersTheTransactionOfTheProviderOfTheToken(t *testing.T) {
	t.Parallel()
	rows := &reader{view: view(t, wager.Processed, "975.00")}
	recorder := getExternal(t, rows, "provider-a", "provider-a", "external-1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status of the own transaction = %d, want 200", recorder.Code)
	}
	if rows.provider.String() != "provider-a" || rows.external.String() != "external-1" {
		t.Fatalf("asked for %s of %s, want external-1 of provider-a", rows.external, rows.provider)
	}
	answered := answerOf(t, recorder)
	if answered.TransactionID != transactionID || answered.Balance.Amount != "975.00" {
		t.Fatalf("answered by external identifier = %+v, want the recorded outcome", answered)
	}
	assertFields(t, fieldsOf(t, recorder), []string{"providerId", "externalTransactionId"}, []string{"idempotentReplay"})
}

func TestOwnedExternal_keepsThePairOnlyForTheProviderOfTheToken(t *testing.T) {
	t.Parallel()
	own := externalRequest("provider-a", "external-1")
	resolved := resolvedRequest(t, "provider-a")
	own = own.WithContext(resolved.Context())
	provider, external, err := ownedExternal(own)
	if err != nil {
		t.Fatalf("ownedExternal of the own provider = %v, want nil", err)
	}
	if provider.String() != "provider-a" || external.String() != "external-1" {
		t.Fatalf("owned pair = %s and %s, want provider-a and external-1", provider, external)
	}
	alien := externalRequest("provider-b", "external-1").WithContext(resolved.Context())
	if _, _, err := ownedExternal(alien); !errors.Is(err, storage.ErrTransactionNotFound) {
		t.Fatalf("ownedExternal of another provider = %v, want %v", err, storage.ErrTransactionNotFound)
	}
	if _, _, err := ownedExternal(externalRequest("provider-a", "external-1")); !errors.Is(err, errNoClient) {
		t.Fatalf("ownedExternal with no resolved client = %v, want %v", err, errNoClient)
	}
}

// externalMoney is money as the client reads it: two strings, never a JSON number.
type externalMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type externalTransaction struct {
	TransactionID         string        `json:"transactionId"`
	Kind                  string        `json:"kind"`
	Status                string        `json:"status"`
	ProviderID            string        `json:"providerId"`
	ExternalTransactionID string        `json:"externalTransactionId"`
	Money                 externalMoney `json:"money"`
	Balance               externalMoney `json:"balance"`
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

// fieldsOf answers the top-level fields of the body as they were written, which
// is what tells a field left out from one written with its zero value.
func fieldsOf(t *testing.T, recorder *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &fields); err != nil {
		t.Fatalf("unmarshal of the fields = %v, want nil: %s", err, recorder.Body)
	}
	return fields
}

// assertFields checks the names a body carries and the ones it must not.
func assertFields(t *testing.T, fields map[string]json.RawMessage, present, absent []string) {
	t.Helper()
	for _, name := range present {
		if _, ok := fields[name]; !ok {
			t.Fatalf("fields = %v, want %q among them", keysOf(fields), name)
		}
	}
	for _, name := range absent {
		if _, ok := fields[name]; ok {
			t.Fatalf("fields = %v, want no %q", keysOf(fields), name)
		}
	}
}

func keysOf(fields map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(fields))
	for name := range fields {
		keys = append(keys, name)
	}
	slices.Sort(keys)
	return keys
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

func getExternal(t *testing.T, rows ExternalReader, client, provider, external string) *httptest.ResponseRecorder {
	t.Helper()
	return serve(t, client, ReadByExternal(rows, reporter()), externalRequest(provider, external))
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
	return NewReporter(slog.New(slog.NewJSONHandler(io.Discard, nil)), metrics.New(prometheus.NewRegistry()))
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
	external identity.ExternalTransactionID
}

func (r *reader) ByExternal(_ context.Context, provider identity.ProviderID, external identity.ExternalTransactionID) (storage.TransactionView, error) {
	r.calls++
	r.provider = provider
	r.external = external
	if r.err != nil {
		return storage.TransactionView{}, r.err
	}
	return r.view, nil
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

// waiting is the outcome of an operation that was accepted and is waiting for
// the one it cites: the row is durable and no commit closed it, so it carries no
// balance.
func waiting(t *testing.T) submitwager.Result {
	t.Helper()
	return submitwager.Result{
		TransactionID: transactionOf(t),
		Kind:          wager.KindWin,
		Status:        wager.PendingReference,
		ExternalID:    externalOf(t),
		Amount:        amountOf(t, "50.00"),
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

// statusOf is what tells the two first outcomes apart: the completion created the
// resource and decided it, and the wait created it without deciding it.
func TestStatusOf_tellsTheWaitApartFromTheCompletion(t *testing.T) {
	t.Parallel()
	if got := statusOf(settled(t, false)); got != http.StatusCreated {
		t.Fatalf("statusOf a completion = %d, want 201", got)
	}
	if got := statusOf(waiting(t)); got != http.StatusAccepted {
		t.Fatalf("statusOf an operation that waits = %d, want 202", got)
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

// The submission is read by name, as a client following the challenge statement
// reads it: the names are that document's, the former ones are gone, and the
// replay marker is written even when it is false.
func TestSettledResponse_answersTheNamesOfTheStatementWithTheReplayWrittenOnTheFirstCompletion(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	write(recorder, http.StatusCreated, settledResponse(settled(t, false)))
	fields := fieldsOf(t, recorder)
	assertFields(t, fields,
		[]string{"transactionId", "status", "balance", "idempotentReplay"},
		[]string{"id", "observedBalance"})
	if got := string(fields["idempotentReplay"]); got != "false" {
		t.Fatalf("idempotentReplay on the first completion = %s, want false written out", got)
	}
}

// A read is not an arrival of the operation, so it says nothing about a replay:
// the marker is left out rather than answered false, and the provider is there.
func TestViewResponse_answersTheNamesOfTheStatementWithNoReplayMarker(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	write(recorder, http.StatusOK, viewResponse(view(t, wager.Processed, "975.00")))
	assertFields(t, fieldsOf(t, recorder),
		[]string{"transactionId", "providerId", "balance"},
		[]string{"id", "observedBalance", "idempotentReplay"})
}

// Every answer that is not a refusal is plain JSON under the status it was given:
// problem details is reserved for the refusal of the request.
func TestWrite_answersJSONUnderTheStatusItWasGiven(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	write(recorder, http.StatusAccepted, viewResponse(view(t, wager.PendingReference, "")))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status written = %d, want the 202 it was given", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type written = %s, want application/json", got)
	}
	if got := answerOf(t, recorder).Status; got != "PENDING_REFERENCE" {
		t.Fatalf("status of the body written = %s, want the PENDING_REFERENCE it was given", got)
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
