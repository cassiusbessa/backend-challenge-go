package walletapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

func TestDecodeOpen_readsThePlayerAndTheInitialBalance(t *testing.T) {
	t.Parallel()
	cmd, err := decodeOpen(httptest.NewRecorder(), openRequestOf(`{"playerId":"22222222-2222-4222-8222-222222222222","initialBalance":{"amount":"1000.00","currency":"BRL"}}`))
	if err != nil {
		t.Fatalf("decodeOpen = %v, want nil", err)
	}
	if cmd.InitialBalance.Amount() != "1000.00" {
		t.Fatalf("amount = %s, want 1000.00", cmd.InitialBalance.Amount())
	}
	if cmd.InitialBalance.Currency().Code() != "BRL" {
		t.Fatalf("currency = %s, want BRL", cmd.InitialBalance.Currency().Code())
	}
	if cmd.PlayerID.String() != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("playerId = %s, want the one in the body", cmd.PlayerID)
	}
}

func TestDecodeOpen_refusesEveryAmountOutsideTheContract(t *testing.T) {
	t.Parallel()
	for _, amount := range []string{"", "NaN", "Infinity", "2.5e1", "-25.00", "25.005"} {
		t.Run("amount "+amount+" is refused", func(t *testing.T) {
			body := `{"playerId":"22222222-2222-4222-8222-222222222222","initialBalance":{"amount":"` + amount + `","currency":"BRL"}}`
			_, err := decodeOpen(httptest.NewRecorder(), openRequestOf(body))
			if !errors.Is(err, problem.ErrInvalidInput) {
				t.Fatalf("decodeOpen = %v, want %v", err, problem.ErrInvalidInput)
			}
			if detailOf(err) != "initialBalance is not valid" {
				t.Fatalf("detail = %q, want the field name", detailOf(err))
			}
		})
	}
}

func TestDecodeOpen_refusesWhatIsNotTheContract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		body  string
		field string
	}{
		{name: "a malformed body is refused", body: `{`, field: "body is not valid"},
		{name: "a float amount is refused", body: `{"playerId":"22222222-2222-4222-8222-222222222222","initialBalance":{"amount":25.00,"currency":"BRL"}}`, field: "body is not valid"},
		{name: "an absent player is refused", body: `{"initialBalance":{"amount":"1.00","currency":"BRL"}}`, field: "playerId is not valid"},
		{name: "a player out of format is refused", body: `{"playerId":"not-a-uuid","initialBalance":{"amount":"1.00","currency":"BRL"}}`, field: "playerId is not valid"},
		{name: "an unknown currency is refused", body: `{"playerId":"22222222-2222-4222-8222-222222222222","initialBalance":{"amount":"1.00","currency":"BRLL"}}`, field: "initialBalance is not valid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeOpen(httptest.NewRecorder(), openRequestOf(tc.body))
			if !errors.Is(err, problem.ErrInvalidInput) {
				t.Fatalf("decodeOpen = %v, want %v", err, problem.ErrInvalidInput)
			}
			if detailOf(err) != tc.field {
				t.Fatalf("detail = %q, want %q", detailOf(err), tc.field)
			}
		})
	}
}

func TestDecodeWalletID_readsTheIdentityInTheURL(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/wallets/11111111-1111-4111-8111-111111111111", nil)
	request.SetPathValue("walletId", "11111111-1111-4111-8111-111111111111")
	id, err := decodeWalletID(request)
	if err != nil {
		t.Fatalf("decodeWalletID = %v, want nil", err)
	}
	if id.String() != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("walletId = %s, want the one in the URL", id)
	}
}

func TestDecodeWalletID_refusesAnIdentityOutOfFormat(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/wallets/not-a-uuid", nil)
	request.SetPathValue("walletId", "not-a-uuid")
	_, err := decodeWalletID(request)
	if !errors.Is(err, problem.ErrInvalidInput) {
		t.Fatalf("decodeWalletID = %v, want %v", err, problem.ErrInvalidInput)
	}
}

func TestDetailOf_saysNothingAboutAnotherClass(t *testing.T) {
	t.Parallel()
	if detail := detailOf(errors.New("acquire connection: refused")); detail != "" {
		t.Fatalf("detail = %q, want empty outside invalid input", detail)
	}
}

func openRequestOf(body string) *http.Request {
	return httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/wallets", strings.NewReader(body))
}
