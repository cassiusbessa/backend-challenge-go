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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			details := Of(tc.class)
			if details.Status != tc.status {
				t.Fatalf("status = %d, want %d", details.Status, tc.status)
			}
			if details.FailureCode != "" {
				t.Fatalf("failureCode = %s, want empty for a class", details.FailureCode)
			}
		})
	}
}

func TestOf_answersUnavailableForAClassItDoesNotKnow(t *testing.T) {
	t.Parallel()
	if details := Of(Class(200)); details.Status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", details.Status)
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
		{name: "unknown failure answers 503", err: errors.New("acquire connection: context deadline exceeded"), status: http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			details := From(tc.err)
			if details.Status != tc.status {
				t.Fatalf("status = %d, want %d", details.Status, tc.status)
			}
			if details.FailureCode != "" {
				t.Fatalf("failureCode = %s, want empty outside a business rejection", details.FailureCode)
			}
		})
	}
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
