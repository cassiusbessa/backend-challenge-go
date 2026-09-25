package problem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

func TestWrite_answersTheProblemMediaTypeAndTheRequiredMembers(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	Write(recorder, requestTo("/wallets"), Of(InvalidInput))
	if got := recorder.Header().Get("Content-Type"); got != MediaType {
		t.Fatalf("content type = %s, want %s", got, MediaType)
	}
	body := decode(t, recorder)
	if body.Type == "" || body.Title == "" || body.Status == 0 {
		t.Fatalf("body = %+v, want type, title and status", body)
	}
	if body.Instance != "/wallets" {
		t.Fatalf("instance = %s, want /wallets", body.Instance)
	}
}

func TestWrite_keepsAnInstanceTheCallerAlreadyChose(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	chosen := Of(NotFound)
	chosen.Instance = "/wallets/11111111-1111-4111-8111-111111111111"
	Write(recorder, requestTo("/wallets/other"), chosen)
	if body := decode(t, recorder); body.Instance != chosen.Instance {
		t.Fatalf("instance = %s, want %s", body.Instance, chosen.Instance)
	}
}

func TestOf_mapsEachClassToItsNumber(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		class  Class
		status int
	}{
		{name: "invalid input answers 400", class: InvalidInput, status: http.StatusBadRequest},
		{name: "absent credential answers 401", class: Unauthenticated, status: http.StatusUnauthorized},
		{name: "identity without permission answers 403", class: Unauthorized, status: http.StatusForbidden},
		{name: "absent wallet answers 404", class: NotFound, status: http.StatusNotFound},
		{name: "duplicate wallet answers 409", class: WalletExists, status: http.StatusConflict},
		{name: "business rejection answers 422", class: BusinessRejection, status: http.StatusUnprocessableEntity},
		{name: "infrastructure answers 503", class: Unavailable, status: http.StatusServiceUnavailable},
		{name: "a request that can be retried answers 503", class: Retryable, status: http.StatusServiceUnavailable},
		{name: "a defect of ours answers 500", class: Internal, status: http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			details := Of(tc.class)
			if details.Status != tc.status {
				t.Fatalf("status of the class = %d, want %d", details.Status, tc.status)
			}
			if details.FailureCode != "" {
				t.Fatalf("failureCode = %s, want empty for a class", details.FailureCode)
			}
		})
	}
}

// A border that cannot name the class has a defect of its own, so it answers 500
// rather than inviting the caller to repeat the request.
func TestOf_answersInternalForAClassItDoesNotKnow(t *testing.T) {
	t.Parallel()
	details := Of(Class(200))
	if details.Status != http.StatusInternalServerError {
		t.Fatalf("status of an unknown class = %d, want 500", details.Status)
	}
	if details.Class != Internal {
		t.Fatalf("class of an unknown class = %d, want Internal", details.Class)
	}
}

// Retryable and Unavailable share the number, so the class is the only thing that
// tells them apart: one marks the span and records a stack, the other does not.
func TestBroken_separatesTheTwoAnswersThatShareTheNumber(t *testing.T) {
	t.Parallel()
	retry, outage := Of(Retryable), Of(Unavailable)
	if retry.Status != outage.Status {
		t.Fatalf("statuses = %d and %d, want both of them the same", retry.Status, outage.Status)
	}
	if retry.Broken() {
		t.Fatalf("Broken() of a retryable answer = %t, want false: nothing is broken", retry.Broken())
	}
	if !outage.Broken() {
		t.Fatalf("Broken() of an outage = %t, want true", outage.Broken())
	}
}

// The retryable answer says how long to wait; the outage that shares its number
// does not, because repeating it is not what fixes anything.
func TestWrite_asksTheCallerToWaitOnlyOnARetryableAnswer(t *testing.T) {
	t.Parallel()
	retry := httptest.NewRecorder()
	Write(retry, requestTo("/wagering/transactions"), Of(Retryable))
	if got := retry.Header().Get("Retry-After"); got != retryAfter {
		t.Fatalf("Retry-After of a retryable answer = %q, want %q", got, retryAfter)
	}
	for _, tc := range []struct {
		name  string
		class Class
	}{
		{name: "an outage", class: Unavailable},
		{name: "a defect of ours", class: Internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			silent := httptest.NewRecorder()
			Write(silent, requestTo("/wagering/transactions"), Of(tc.class))
			if got := silent.Header().Get("Retry-After"); got != "" {
				t.Fatalf("Retry-After of %s = %q, want none", tc.name, got)
			}
		})
	}
}

// Retryable, Unavailable and Internal all answer without a token, so the type is
// the only member that tells the caller which of them it got.
func TestOf_givesEachFailureClassATypeAndNoToken(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		details Details
	}{
		{name: "the retryable answer", details: Of(Retryable)},
		{name: "the defect", details: Of(Internal)},
		{name: "the outage", details: Of(Unavailable)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.HasPrefix(tc.details.Type, namespace) {
				t.Fatalf("type of %s = %q, want it under %q", tc.name, tc.details.Type, namespace)
			}
			if tc.details.FailureCode != "" {
				t.Fatalf("token of %s = %q, want none outside a business rejection", tc.name, tc.details.FailureCode)
			}
		})
	}
}

// Two of the three share the number, so a type answering for more than one class
// would leave the caller unable to tell them apart at all.
func TestOf_keepsTheThreeFailureTypesApart(t *testing.T) {
	t.Parallel()
	taken := map[string]Class{}
	for _, class := range []Class{Retryable, Internal, Unavailable} {
		details := Of(class)
		if held, seen := taken[details.Type]; seen {
			t.Fatalf("type %q answers class %d and class %d, want one type each", details.Type, held, class)
		}
		taken[details.Type] = class
	}
}

func TestFrom_namesTheCatalogTokenOfABusinessRejection(t *testing.T) {
	t.Parallel()
	rejected := fmt.Errorf("submit wager: %w", wager.NewRejection(wager.InsufficientFunds, nil))
	details := From(rejected)
	if details.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Fatalf("failureCode = %s, want INSUFFICIENT_FUNDS", details.FailureCode)
	}
	if details.Status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", details.Status)
	}
}

func TestFrom_leavesEveryOtherRefusalWithoutAToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{name: "invalid input answers 400", err: fmt.Errorf("read playerId: %w", ErrInvalidInput), status: http.StatusBadRequest},
		{name: "duplicate wallet answers 409", err: fmt.Errorf("insert wallet: %w", storage.ErrWalletExists), status: http.StatusConflict},
		{name: "absent wallet answers 404", err: fmt.Errorf("read wallet: %w", storage.ErrWalletNotFound), status: http.StatusNotFound},
		{
			// A real outage arrives carrying the stack fault captured at the
			// boundary, and that is what tells it from an error nobody named.
			name:   "an outage answers 503",
			err:    fault.Wrap("acquire connection", errors.New("context deadline exceeded")),
			status: http.StatusServiceUnavailable,
		},
		{
			// Nobody classified this one and it carries no stack, so it is a
			// defect of ours. Answering 503 would invite the provider to repeat a
			// request that can never succeed.
			name:   "a failure nobody named answers 500",
			err:    errors.New("submit wager: kind is not settled by this use case"),
			status: http.StatusInternalServerError,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			details := From(tc.err)
			if details.Status != tc.status {
				t.Fatalf("status of the refusal = %d, want %d", details.Status, tc.status)
			}
			if details.FailureCode != "" {
				t.Fatalf("failureCode of the refusal = %s, want empty outside a business rejection", details.FailureCode)
			}
		})
	}
}

// A refusal that was already recorded answers the token of the first refusal and
// says it is a replay. The marker rides beside the token and carries nothing else.
func TestFrom_marksAReplayedRejectionBesideItsToken(t *testing.T) {
	t.Parallel()
	recorded := replayedRefusal{rejection: wager.NewRejection(wager.InsufficientFunds, nil)}
	details := From(fmt.Errorf("submit wager: %w", recorded))
	if details.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Fatalf("failureCode = %s, want the token of the first refusal", details.FailureCode)
	}
	if !details.IdempotentReplay {
		t.Fatalf("idempotentReplay = %t, want true for a recorded refusal", details.IdempotentReplay)
	}
	if details.Status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want the 422 of the first refusal", details.Status)
	}
}

func TestFrom_leavesTheFirstRefusalWithoutTheMarker(t *testing.T) {
	t.Parallel()
	details := From(fmt.Errorf("submit wager: %w", wager.NewRejection(wager.InsufficientFunds, nil)))
	if details.IdempotentReplay {
		t.Fatalf("idempotentReplay = %t, want false on the first refusal", details.IdempotentReplay)
	}
}

// Invalid input, a credential, a permission and a failure have no recorded outcome
// to answer again, so none of them ever carries the marker.
func TestFrom_neverMarksARefusalWithNothingRecorded(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
	}{
		{name: "invalid input is never a replay", err: fmt.Errorf("read body: %w", ErrInvalidInput)},
		{name: "a permission refusal is never a replay", err: fmt.Errorf("check provider: %w", ErrNotPermitted)},
		{name: "a failure is never a replay", err: errors.New("acquire connection: context deadline exceeded")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			details := From(tc.err)
			if details.IdempotentReplay || details.FailureCode != "" {
				t.Fatalf("details = %+v, want neither a token nor the marker", details)
			}
		})
	}
}

func TestFrom_classifiesTheRefusalsOfTheWagerRoutes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{name: "absent transaction answers 404", err: fmt.Errorf("read transaction: %w", storage.ErrTransactionNotFound), status: http.StatusNotFound},
		{name: "identity without permission answers 403", err: fmt.Errorf("check provider: %w", ErrNotPermitted), status: http.StatusForbidden},
		{name: "a lost write answers 503", err: fmt.Errorf("update wallet balance: %w", storage.ErrLostWrite), status: http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			details := From(tc.err)
			if details.Status != tc.status {
				t.Fatalf("status of the refusal of a wager route = %d, want %d", details.Status, tc.status)
			}
			if details.FailureCode != "" {
				t.Fatalf("failureCode of the refusal of a wager route = %s, want empty outside a business rejection", details.FailureCode)
			}
		})
	}
}

func TestWrite_leavesOutTheMarkerOfARefusalThatIsNotAReplay(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	Write(recorder, requestTo("/wagering/transactions"), From(wager.NewRejection(wager.InsufficientFunds, nil)))
	if body := recorder.Body.String(); strings.Contains(body, "idempotentReplay") {
		t.Fatalf("body = %s, want it without the marker", body)
	}
}

// replayedRefusal is how a use case marks an outcome it had already recorded: it
// wraps the rejection, so the token is still reachable, and reports the replay.
type replayedRefusal struct {
	rejection error
}

func (r replayedRefusal) Error() string {
	return "recorded refusal replayed: " + r.rejection.Error()
}

func (r replayedRefusal) Unwrap() error {
	return r.rejection
}

func (r replayedRefusal) IdempotentReplay() bool {
	return true
}

func TestWrite_leavesOutTheMembersTheRefusalDoesNotCarry(t *testing.T) {
	t.Parallel()
	recorder := httptest.NewRecorder()
	Write(recorder, requestTo("/wallets"), Of(Unavailable))
	body := recorder.Body.String()
	for _, absent := range []string{"failureCode", "detail"} {
		if strings.Contains(body, absent) {
			t.Fatalf("body = %s, want it without %q", body, absent)
		}
	}
}

func requestTo(path string) *http.Request {
	return httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
}

func decode(t *testing.T, recorder *httptest.ResponseRecorder) Details {
	t.Helper()
	var body Details
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal = %v, want nil", err)
	}
	return body
}

// retryableError and defectiveError stand in for whatever a use case marks. The
// border reads the behaviour and never the type, so a fake carries it exactly the
// way the real one does.
type retryableError struct{ error }

func (retryableError) RetryShortly() bool { return true }

type defectiveError struct{ error }

func (defectiveError) Defect() bool { return true }

func TestAsksToRetry_readsTheBehaviourThroughTheChain(t *testing.T) {
	t.Parallel()
	marked := fmt.Errorf("submit wager: %w", retryableError{errors.New("not terminal yet")})
	if got := asksToRetry(marked); !got {
		t.Fatalf("asksToRetry of a marked chain = %t, want true", got)
	}
	if got := asksToRetry(errors.New("submit wager: something else")); got {
		t.Fatalf("asksToRetry of a plain error = %t, want false", got)
	}
}

func TestIsDefect_readsTheBehaviourThroughTheChain(t *testing.T) {
	t.Parallel()
	marked := fmt.Errorf("submit wager: %w", defectiveError{errors.New("kind not settled")})
	if got := isDefect(marked); !got {
		t.Fatalf("isDefect of a marked chain = %t, want true", got)
	}
	if got := isDefect(errors.New("submit wager: something else")); got {
		t.Fatalf("isDefect of a plain error = %t, want false", got)
	}
}

// The stack is what tells a real outage from an error nobody named, so it is read
// through the chain and never from the text.
func TestCarriesStack_findsTheCaptureOfAnIOBoundary(t *testing.T) {
	t.Parallel()
	captured := fmt.Errorf("submit wager: %w", fault.Wrap("acquire connection", errors.New("refused")))
	if got := carriesStack(captured); !got {
		t.Fatalf("carriesStack of a captured failure = %t, want true", got)
	}
	if got := carriesStack(errors.New("acquire connection: refused")); got {
		t.Fatalf("carriesStack of a bare error naming a connection = %t, want false", got)
	}
}

func TestNamedClassOf_answersOnlyForTheConditionsItNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		err   error
		class Class
	}{
		{name: "invalid input", err: ErrInvalidInput, class: InvalidInput},
		{name: "a duplicate wallet", err: storage.ErrWalletExists, class: WalletExists},
		{name: "an absent wallet", err: storage.ErrWalletNotFound, class: NotFound},
		{name: "an absent transaction", err: storage.ErrTransactionNotFound, class: NotFound},
		{name: "no permission", err: ErrNotPermitted, class: Unauthorized},
		{name: "a lost write", err: storage.ErrLostWrite, class: Unavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := namedClassOf(fmt.Errorf("submit wager: %w", tc.err))
			if !ok {
				t.Fatalf("namedClassOf named %s = %t, want true", tc.name, ok)
			}
			if got != tc.class {
				t.Fatalf("named class of %s = %d, want %d", tc.name, got, tc.class)
			}
		})
	}
	if _, ok := namedClassOf(errors.New("submit wager: something nobody named")); ok {
		t.Fatalf("namedClassOf of an unnamed condition = %t, want false", ok)
	}
}

// The order is the contract. A marker a use case set on purpose beats the stack a
// boundary captured on the way up, so a transient condition is never answered as
// an outage even when it crossed I/O.
func TestMarkedClassOf_prefersTheMarkerOverTheStack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		err   error
		class Class
	}{
		{name: "a marked retryable", err: retryableError{errors.New("not terminal yet")}, class: Retryable},
		{name: "a marked defect", err: defectiveError{errors.New("kind not settled")}, class: Internal},
		{name: "a captured outage", err: fault.Wrap("acquire connection", errors.New("refused")), class: Unavailable},
		{name: "a failure nobody named", err: errors.New("something else"), class: Internal},
		{
			name:  "a retryable that also crossed I/O",
			err:   fault.Wrap("acquire connection", retryableError{errors.New("not terminal yet")}),
			class: Retryable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := markedClassOf(fmt.Errorf("submit wager: %w", tc.err)); got != tc.class {
				t.Fatalf("marked class of %s = %d, want %d", tc.name, got, tc.class)
			}
		})
	}
}

// The replay marker is read as a behaviour too, and only a recorded outcome sets
// it: nothing else has anything to answer again.
func TestIsReplay_answersOnlyForAMarkedOutcome(t *testing.T) {
	t.Parallel()
	if got := isReplay(errors.New("submit wager: first arrival")); got {
		t.Fatalf("isReplay of an unmarked failure = %t, want false", got)
	}
}
