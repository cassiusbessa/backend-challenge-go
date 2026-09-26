package walletapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/listledger"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
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

func TestDecodeLimit_readsAnAbsentLimitAsZeroAndAPresentOneAsItself(t *testing.T) {
	t.Parallel()
	for text, want := range map[string]int{"": 0, "1": 1, "25": 25, "200": 200} {
		t.Run("the limit "+text+" is read", func(t *testing.T) {
			got, err := decodeLimit(text)
			if err != nil {
				t.Fatalf("decodeLimit(%q) = %v, want nil", text, err)
			}
			if got != want {
				t.Fatalf("limit of %q = %d, want %d", text, got, want)
			}
		})
	}
}

// An explicit zero is refused here and not read as the default: zero is the only
// way to tell the use case nothing was asked, so a zero the client wrote cannot
// be let through without being mistaken for that.
func TestDecodeLimit_refusesWhatIsNotAPositiveInteger(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"0", "-1", "abc", "2.5", "1e2"} {
		t.Run("the limit "+text+" is refused", func(t *testing.T) {
			_, err := decodeLimit(text)
			if !errors.Is(err, problem.ErrInvalidInput) {
				t.Fatalf("decodeLimit(%q) = %v, want %v", text, err, problem.ErrInvalidInput)
			}
			if detailOf(err) != "limit is not valid" {
				t.Fatalf("detail = %q, want the field name", detailOf(err))
			}
		})
	}
}

func TestRefusalOf_namesTheFieldTheUseCaseRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]error{
		"cursor is not valid": fmt.Errorf("page ledger: %w", listledger.ErrInvalidCursor),
		"limit is not valid":  listledger.ErrInvalidLimit,
	}
	for detail, refused := range cases {
		t.Run(detail, func(t *testing.T) {
			translated := refusalOf(refused)
			if !errors.Is(translated, problem.ErrInvalidInput) {
				t.Fatalf("refusalOf = %v, want %v", translated, problem.ErrInvalidInput)
			}
			if detailOf(translated) != detail {
				t.Fatalf("detail = %q, want %q", detailOf(translated), detail)
			}
		})
	}
}

func TestRefusalOf_leavesAnyOtherFailureAsItIs(t *testing.T) {
	t.Parallel()
	absent := fmt.Errorf("read ledger: %w", storage.ErrWalletNotFound)
	if translated := refusalOf(absent); translated != absent {
		t.Fatalf("refusalOf of an absence = %v, want the same error back", translated)
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
