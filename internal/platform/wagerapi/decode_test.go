package wagerapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

func TestDecodeSubmit_readsTheCommandFromTheBodyAndTheHeader(t *testing.T) {
	t.Parallel()
	cmd, err := decode(t, submission(nil), "key-1")
	if err != nil {
		t.Fatalf("decodeSubmit = %v, want nil", err)
	}
	assertKeyAndKind(t, cmd)
	assertMoney(t, cmd)
	assertProviderOperation(t, cmd)
	assertOwnerAndRound(t, cmd)
}

func assertKeyAndKind(t *testing.T, cmd submitwager.Command) {
	t.Helper()
	if cmd.IdempotencyKey.String() != "key-1" {
		t.Fatalf("key = %s, want the key-1 of the header", cmd.IdempotencyKey)
	}
	if cmd.Kind != wager.KindBet {
		t.Fatalf("kind = %s, want BET", cmd.Kind)
	}
}

func assertMoney(t *testing.T, cmd submitwager.Command) {
	t.Helper()
	if cmd.Amount.Amount() != "25.00" || cmd.Amount.Currency().Code() != "BRL" {
		t.Fatalf("amount = %s, want 25.00 BRL", cmd.Amount)
	}
}

func assertProviderOperation(t *testing.T, cmd submitwager.Command) {
	t.Helper()
	if cmd.ProviderID.String() != "provider-a" || cmd.ExternalID.String() != "external-1" {
		t.Fatalf("provider and external = %s and %s, want provider-a and external-1", cmd.ProviderID, cmd.ExternalID)
	}
}

func assertOwnerAndRound(t *testing.T, cmd submitwager.Command) {
	t.Helper()
	if cmd.PlayerID.String() != playerID || cmd.WalletID.String() != walletID {
		t.Fatalf("player and wallet = %s and %s, want %s and %s", cmd.PlayerID, cmd.WalletID, playerID, walletID)
	}
	if cmd.RoundID.String() != "round-1" || cmd.GameID.String() != "game-1" {
		t.Fatalf("round and game = %s and %s, want round-1 and game-1", cmd.RoundID, cmd.GameID)
	}
}

// The key is what tells two arrivals apart, so a request without it is not a
// request this route can serve. Nothing is written for it.
func TestDecodeSubmit_refusesAnAbsentKey(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"", " "} {
		t.Run("the key "+key+" is refused", func(t *testing.T) {
			_, err := decode(t, submission(nil), key)
			assertInvalidInput(t, err)
		})
	}
}

func TestDecodeSubmit_refusesEveryFieldOutsideTheContract(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		field string
		value any
	}{
		{name: "a provider that is empty is refused", field: "providerId", value: ""},
		{name: "an external transaction that is empty is refused", field: "externalTransactionId", value: ""},
		{name: "a player out of format is refused", field: "playerId", value: "not-a-uuid"},
		{name: "a wallet out of format is refused", field: "walletId", value: "not-a-uuid"},
		{name: "a player that is the nil UUID is refused", field: "playerId", value: nilUUID},
		{name: "a wallet that is the nil UUID is refused", field: "walletId", value: nilUUID},
		{name: "a round that is empty is refused", field: "roundId", value: ""},
		{name: "a game that is empty is refused", field: "gameId", value: ""},
		{name: "a kind outside the vocabulary is refused", field: "kind", value: "DOUBLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decode(t, submission(map[string]any{tc.field: tc.value}), "key-1")
			assertInvalidInput(t, err)
			if got := detailOf(err); !strings.HasPrefix(got, tc.field) {
				t.Fatalf("detail = %q, want it naming %s", got, tc.field)
			}
		})
	}
}

// The amount is refused and never rounded: a scale past two places, the empty
// string, a negative value and scientific notation all stay refused.
func TestDecodeSubmit_refusesEveryAmountOutsideTheContract(t *testing.T) {
	t.Parallel()
	for _, amount := range []string{"", "NaN", "Infinity", "2.5e1", "-25.00", "25.005"} {
		t.Run("the amount "+amount+" is refused", func(t *testing.T) {
			body := submission(map[string]any{"money": map[string]string{"amount": amount, "currency": "BRL"}})
			_, err := decode(t, body, "key-1")
			assertInvalidInput(t, err)
		})
	}
}

func TestDecodeSubmit_refusesAnUnknownCurrency(t *testing.T) {
	t.Parallel()
	body := submission(map[string]any{"money": map[string]string{"amount": "25.00", "currency": "BRLL"}})
	_, err := decode(t, body, "key-1")
	assertInvalidInput(t, err)
}

func TestDecodeSubmit_refusesABodyItCannotRead(t *testing.T) {
	t.Parallel()
	_, err := decode(t, "{", "key-1")
	assertInvalidInput(t, err)
	if got := detailOf(err); !strings.HasPrefix(got, "body") {
		t.Fatalf("detail = %q, want it naming the body", got)
	}
}

// What cites another operation needs the wait, and the wait belongs to the next
// delivery. The refusal is not a business rejection: no rule refused anything, so
// the answer carries no token.
func TestDecodeSubmit_refusesWhatThisDeliveryDoesNotAccept(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		body  map[string]any
		field string
	}{
		{name: "a refund is refused", body: map[string]any{"kind": "REFUND"}, field: "kind"},
		{name: "a rollback is refused", body: map[string]any{"kind": "ROLLBACK"}, field: "kind"},
		{
			name:  "a win citing a bet is refused",
			body:  map[string]any{"kind": "WIN", "referenceExternalTransactionId": "bet-1"},
			field: "referenceExternalTransactionId",
		},
		{
			name:  "a win citing null is refused",
			body:  map[string]any{"kind": "WIN", "referenceExternalTransactionId": nil},
			field: "referenceExternalTransactionId",
		},
		{
			name:  "a win citing the empty string is refused",
			body:  map[string]any{"kind": "WIN", "referenceExternalTransactionId": ""},
			field: "referenceExternalTransactionId",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decode(t, submission(tc.body), "key-1")
			assertInvalidInput(t, err)
			detail := detailOf(err)
			if !strings.HasPrefix(detail, tc.field) || !strings.Contains(detail, "not accepted") {
				t.Fatalf("detail = %q, want %s named as not accepted", detail, tc.field)
			}
		})
	}
}

// bodyCeiling is the ceiling the rule fixes, spelled out here instead of read from
// maxBody: a case built from the constant moves with it and would pass at any
// value, which pins the shape of the boundary and not the boundary itself.
const bodyCeiling = 8 << 10

// The route bounds the payload, so the case sits on the ceiling and not near it: a
// body of exactly 8 KiB is read, and one byte past it is refused before any field
// is decoded.
func TestDecodeSubmit_takesABodyUpToTheCeilingAndRefusesItPastThat(t *testing.T) {
	t.Parallel()
	if maxBody != bodyCeiling {
		t.Fatalf("maxBody = %d, want %d: the ceiling of the payload comes from the rule", maxBody, bodyCeiling)
	}
	atCeiling := submissionOfSize(bodyCeiling)
	if _, err := decode(t, atCeiling, "key-1"); err != nil {
		t.Fatalf("decodeSubmit of a body of %d bytes = %v, want nil", len(atCeiling), err)
	}
	_, err := decode(t, submissionOfSize(bodyCeiling+1), "key-1")
	assertInvalidInput(t, err)
	if got := detailOf(err); !strings.HasPrefix(got, "body") {
		t.Fatalf("detail = %q, want the body named past the ceiling", got)
	}
}

func TestDecodeTransactionID_refusesAnIdentityOutOfFormat(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/wagering/transactions/not-a-uuid", nil)
	request.SetPathValue("transactionId", "not-a-uuid")
	_, err := decodeTransactionID(request)
	assertInvalidInput(t, err)
}

func TestDecodeTransactionID_readsTheIdentityTheURLNames(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/wagering/transactions/"+transactionID, nil)
	request.SetPathValue("transactionId", transactionID)
	id, err := decodeTransactionID(request)
	if err != nil {
		t.Fatalf("decodeTransactionID = %v, want nil", err)
	}
	if id.String() != transactionID {
		t.Fatalf("transaction = %s, want %s", id, transactionID)
	}
}

// The detail names the field and never its value: the amount and the key of a
// refused request must not travel back in the error body.
func TestDetailOf_namesTheFieldAndNotItsValue(t *testing.T) {
	t.Parallel()
	_, err := decode(t, submission(map[string]any{"money": map[string]string{"amount": "25.005", "currency": "BRL"}}), "key-1")
	detail := detailOf(err)
	if !strings.Contains(detail, "money") {
		t.Fatalf("detail = %q, want it naming money", detail)
	}
	if strings.Contains(detail, "25.005") {
		t.Fatalf("detail = %q, want it without the refused value", detail)
	}
}

func TestDetailOf_answersNothingForAnotherClass(t *testing.T) {
	t.Parallel()
	if got := detailOf(errors.New("acquire connection: context deadline exceeded")); got != "" {
		t.Fatalf("detail = %q, want empty outside a refused field", got)
	}
}

func assertInvalidInput(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, problem.ErrInvalidInput) {
		t.Fatalf("error = %v, want it classified as invalid input", err)
	}
	details := problem.From(err)
	if details.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", details.Status)
	}
	if details.FailureCode != "" {
		t.Fatalf("failureCode = %s, want empty: no rule refused the request", details.FailureCode)
	}
	var rejection wager.Rejection
	if errors.As(err, &rejection) {
		t.Fatalf("error = %v, want no business rejection", err)
	}
}

func decode(t *testing.T, body, key string) (submitwager.Command, error) {
	t.Helper()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/wagering/transactions", strings.NewReader(body))
	if key != "" {
		request.Header.Set(idempotencyHeader, key)
	}
	return decodeSubmit(httptest.NewRecorder(), request)
}

// submission is the body of one accepted bet, with the given fields replaced. A
// case names only what it is about, and everything else stays valid.
func submission(changes map[string]any) string {
	body := map[string]any{
		"providerId":            "provider-a",
		"externalTransactionId": "external-1",
		"playerId":              playerID,
		"walletId":              walletID,
		"roundId":               "round-1",
		"gameId":                "game-1",
		"kind":                  "BET",
		"money":                 map[string]string{"amount": "25.00", "currency": "BRL"},
	}
	for field, value := range changes {
		body[field] = value
	}
	payload, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return string(payload)
}

// submissionOfSize pads the round of an accepted bet until the body measures
// exactly the size asked. The round takes any token, so the padding changes the
// length of the payload and nothing else about it.
func submissionOfSize(size int) string {
	padding := size - len(submission(nil))
	if padding < 0 {
		panic("the accepted submission is already past the size asked")
	}
	return submission(map[string]any{"roundId": "round-1" + strings.Repeat("x", padding)})
}

const (
	nilUUID       = "00000000-0000-0000-0000-000000000000"
	playerID      = "22222222-2222-4222-8222-222222222222"
	walletID      = "11111111-1111-4111-8111-111111111111"
	transactionID = "33333333-3333-4333-8333-333333333333"
)

// accepted is the gate of this delivery, and the bet is what passes it.
func TestAccepted_takesTheOperationThisDeliverySettles(t *testing.T) {
	t.Parallel()
	if err := acceptedBody().accepted(); err != nil {
		t.Fatalf("accepted of a bet = %v, want nil", err)
	}
}

// The two arms name different fields: a cited operation needs a wait nothing here
// can close, and the two reversals never exist without that wait.
func TestAccepted_refusesWhatThisDeliveryCannotSettle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		body  submitRequest
		field string
	}{
		{name: "a citing operation is refused", body: citing(acceptedBody(), `"bet-1"`), field: "referenceExternalTransactionId"},
		{name: "a refund is refused", body: kindOf(acceptedBody(), wager.KindRefund.String()), field: "kind"},
		{name: "a rollback is refused", body: kindOf(acceptedBody(), wager.KindRollback.String()), field: "kind"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.body.accepted()
			if err == nil {
				t.Fatalf("accepted of %s = nil, want the field refused", tc.name)
			}
			if !strings.HasPrefix(err.Error(), "wagerapi: "+tc.field+" ") {
				t.Fatalf("refusal of %s = %q, want %s named first", tc.name, err.Error(), tc.field)
			}
		})
	}
}

// The reference is refused for being present at all, so an empty JSON string and
// an explicit null are refused beside a real identifier.
func TestAccepted_refusesACitedOperationHoweverItIsSpelled(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{`"bet-1"`, `""`, `null`} {
		if err := citing(acceptedBody(), spelling).accepted(); err == nil {
			t.Fatalf("accepted of a reference spelled %s = nil, want it refused", spelling)
		}
	}
}

// command builds the whole command before it looks at the refusal, so a body with
// several bad fields still answers one field and never a partial command.
func TestCommand_answersTheFirstRefusedFieldAndNoCommand(t *testing.T) {
	t.Parallel()
	body := acceptedBody()
	body.PlayerID = "not-a-uuid"
	body.WalletID = "not-a-uuid"
	cmd, err := body.command(keyOf(t))
	if err == nil {
		t.Fatalf("command of a body with two bad fields = %+v with no error, want a refusal", cmd)
	}
	if !strings.HasPrefix(err.Error(), "wagerapi: playerId ") {
		t.Fatalf("refusal = %q, want playerId, the first field the parse read", err.Error())
	}
	if cmd != (submitwager.Command{}) {
		t.Fatalf("command beside the refusal = %+v, want the zero value", cmd)
	}
}

func TestCommand_carriesEveryFieldOfAnAcceptedBody(t *testing.T) {
	t.Parallel()
	cmd, err := acceptedBody().command(keyOf(t))
	if err != nil {
		t.Fatalf("command of an accepted body = %v, want nil", err)
	}
	if cmd.Amount.Amount() != "25.00" || cmd.Kind != wager.KindBet {
		t.Fatalf("command = %+v, want the bet of 25.00 the body carried", cmd)
	}
}

// keep holds the first refusal and drops the rest, which is what lets ten fields
// be parsed without a branch per field.
func TestKeep_holdsTheFirstRefusalAndDropsTheRest(t *testing.T) {
	t.Parallel()
	var parse fields
	parse.keep(nil, "providerId")
	if parse.err != nil {
		t.Fatalf("err after a field that parsed = %v, want nil", parse.err)
	}
	parse.keep(errors.New("out of format"), "playerId")
	parse.keep(errors.New("out of format"), "walletId")
	if !strings.HasPrefix(parse.err.Error(), "wagerapi: playerId ") {
		t.Fatalf("err = %q, want the first field that was refused", parse.err.Error())
	}
}

// The nil UUID is well formed and identity reports it as absent on purpose, so a
// field the operation cannot do without is refused here rather than reaching the
// aggregate, where an incomplete transaction is a defect and not invalid input.
func TestRequired_refusesAnIdentifierTheParseReadAsAbsent(t *testing.T) {
	t.Parallel()
	var absent fields
	absent.required(true, "playerId")
	if absent.err == nil || !strings.HasPrefix(absent.err.Error(), "wagerapi: playerId ") {
		t.Fatalf("err after an absent identifier = %v, want playerId refused", absent.err)
	}
	var present fields
	present.required(false, "playerId")
	if present.err != nil {
		t.Fatalf("err after an identifier that is there = %v, want nil", present.err)
	}
	held := fields{err: invalid("providerId")}
	held.required(true, "playerId")
	if !strings.HasPrefix(held.err.Error(), "wagerapi: providerId ") {
		t.Fatalf("err = %q, want the refusal already held", held.err.Error())
	}
}

// acceptedBody is the body of one accepted bet, as the decoder hands it over.
func acceptedBody() submitRequest {
	var body submitRequest
	if err := json.Unmarshal([]byte(submission(nil)), &body); err != nil {
		panic(err)
	}
	return body
}

func citing(body submitRequest, spelling string) submitRequest {
	body.ReferenceExternalTransactionID = json.RawMessage(spelling)
	return body
}

func kindOf(body submitRequest, kind string) submitRequest {
	body.Kind = kind
	return body
}

func keyOf(t *testing.T) identity.IdempotencyKey {
	t.Helper()
	parsed, err := identity.ParseIdempotencyKey("key-1")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey = %v, want nil", err)
	}
	return parsed
}
