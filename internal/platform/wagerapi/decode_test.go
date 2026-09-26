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
	"github.com/junglegaming/backend-challenge-go/internal/platform/wagerqueue"
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

// The three cases an earlier delivery refused as input it did not take now reach
// the use case: the reversals and the operation that cites another are what the
// wait was brought in for.
func TestDecodeSubmit_takesTheReversalsAndTheOperationThatCitesAnother(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body map[string]any
		kind wager.Kind
	}{
		{
			name: "a refund citing an operation is taken",
			body: map[string]any{"kind": "REFUND", "referenceExternalTransactionId": "bet-1"},
			kind: wager.KindRefund,
		},
		{
			name: "a rollback citing an operation is taken",
			body: map[string]any{"kind": "ROLLBACK", "referenceExternalTransactionId": "bet-1"},
			kind: wager.KindRollback,
		},
		{
			name: "a win citing a bet is taken",
			body: map[string]any{"kind": "WIN", "referenceExternalTransactionId": "bet-1"},
			kind: wager.KindWin,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := decode(t, submission(tc.body), "key-1")
			if err != nil {
				t.Fatalf("decodeSubmit of %s = %v, want nil", tc.name, err)
			}
			if cmd.Kind != tc.kind {
				t.Fatalf("kind = %s, want %s", cmd.Kind, tc.kind)
			}
			if cmd.ReferenceExternalID.String() != "bet-1" {
				t.Fatalf("cited operation = %s, want bet-1", cmd.ReferenceExternalID)
			}
		})
	}
}

// An operation citing none carries the zero identifier, which is what the use
// case reads as having nothing to wait for.
func TestDecodeSubmit_leavesTheCitedOperationEmptyForABodyThatNamesNone(t *testing.T) {
	t.Parallel()
	cmd, err := decode(t, submission(map[string]any{"kind": "WIN"}), "key-1")
	if err != nil {
		t.Fatalf("decodeSubmit of a body citing none = %v, want nil", err)
	}
	if !cmd.ReferenceExternalID.IsZero() {
		t.Fatalf("cited operation = %s, want none", cmd.ReferenceExternalID)
	}
}

// The cited operation spelled wrong is invalid input: the answer carries no
// token, because no rule refused anything and no row was written.
func TestDecodeSubmit_refusesACitedOperationOutOfFormat(t *testing.T) {
	t.Parallel()
	for _, spelling := range []any{"", "   ", 42} {
		body := submission(map[string]any{"kind": "WIN", "referenceExternalTransactionId": spelling})
		_, err := decode(t, body, "key-1")
		assertInvalidInput(t, err)
		if got := detailOf(err); !strings.HasPrefix(got, "referenceExternalTransactionId") {
			t.Fatalf("detail = %q, want the cited operation named", got)
		}
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

// Each segment is refused by its own name, and a blank one is out of format: an
// identifier of the provider is kept as it arrived, so absence is its only fault.
func TestDecodeExternal_refusesEachSegmentOutOfFormatByItsName(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		field    string
		segments [2]string
	}{
		"blank provider":                {field: "providerId", segments: [2]string{" ", "external-1"}},
		"blank external identifier":     {field: "externalTransactionId", segments: [2]string{"provider-a", " "}},
		"external identifier off UTF-8": {field: "externalTransactionId", segments: [2]string{"provider-a", "\xff"}},
		"external identifier with NUL":  {field: "externalTransactionId", segments: [2]string{"provider-a", "external-\x00"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, _, err := decodeExternal(externalRequest(tc.segments[0], tc.segments[1]))
			assertInvalidInput(t, err)
			if got := detailOf(err); !strings.HasPrefix(got, tc.field) {
				t.Fatalf("detail of a refused segment = %q, want it naming %s", got, tc.field)
			}
		})
	}
}

func TestDecodeExternal_readsThePairTheURLNames(t *testing.T) {
	t.Parallel()
	provider, external, err := decodeExternal(externalRequest("provider-a", "external-1"))
	if err != nil {
		t.Fatalf("decodeExternal = %v, want nil", err)
	}
	if provider.String() != "provider-a" || external.String() != "external-1" {
		t.Fatalf("pair = %s and %s, want provider-a and external-1", provider, external)
	}
}

// externalRequest is the request of the read by external identifier with its two
// segments already matched, the way the mux hands it to the handler.
func externalRequest(provider, external string) *http.Request {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/providers/wagering/transactions", nil)
	request.SetPathValue("providerId", provider)
	request.SetPathValue("externalTransactionId", external)
	return request
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

// The reference names an operation the body cites. An absent field and an
// explicit null both mean citing none, which is what go-idempotency says of a
// null: it does not enter the business at all.
func TestReference_readsAnAbsentFieldAndANullAsCitingNothing(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{"", "null"} {
		var parse fields
		cited := parse.reference(rawOf(spelling))
		if parse.err != nil {
			t.Fatalf("reference spelled %q = %v, want nil", spelling, parse.err)
		}
		if !cited.IsZero() {
			t.Fatalf("reference spelled %q = %s, want no cited operation", spelling, cited)
		}
	}
}

func TestReference_readsTheIdentifierOfTheOperationTheBodyCites(t *testing.T) {
	t.Parallel()
	var parse fields
	cited := parse.reference(rawOf(`"bet-1"`))
	if parse.err != nil {
		t.Fatalf("reference = %v, want nil", parse.err)
	}
	if cited.String() != "bet-1" {
		t.Fatalf("reference = %s, want bet-1", cited)
	}
}

// A body that meant to cite an operation and spelled it wrong is refused, and
// never settled as though it cited none.
func TestReference_refusesAFieldThatIsThereAndIsNotAnIdentifier(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{`""`, `"   "`, `42`, `{}`, `[]`, `true`} {
		var parse fields
		cited := parse.reference(rawOf(spelling))
		if parse.err == nil {
			t.Fatalf("reference spelled %s = nil, want it refused", spelling)
		}
		if !strings.HasPrefix(parse.err.Error(), "wagerapi: referenceExternalTransactionId ") {
			t.Fatalf("refusal of %s = %q, want the field named", spelling, parse.err.Error())
		}
		if !cited.IsZero() {
			t.Fatalf("reference beside the refusal = %s, want the zero identifier", cited)
		}
	}
}

func rawOf(spelling string) json.RawMessage {
	if spelling == "" {
		return nil
	}
	return json.RawMessage(spelling)
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

func keyOf(t *testing.T) identity.IdempotencyKey {
	t.Helper()
	parsed, err := identity.ParseIdempotencyKey("key-1")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey = %v, want nil", err)
	}
	return parsed
}

// The same business over the two channels has to reach the use case as the same
// command, because the hash of the body is taken from that command by a single
// function: two commands alike in every business field hash alike, and the
// idempotency of the provider cannot tell the two channels apart.
//
// The two envelopes differ where go-idempotency says they may: the key arrives in
// a header over HTTP and in the body over the queue, and the identity of the
// message and the correlation belong to the queue alone.
func TestDecodeSubmit_answersTheSameCommandAsTheQueueForTheSameBusiness(t *testing.T) {
	t.Parallel()
	overHTTP, err := decode(t, submission(nil), "key-1")
	if err != nil {
		t.Fatalf("decodeSubmit of the shared business = %v, want nil", err)
	}
	overQueue, err := wagerqueue.Decode(queueMessage(t))
	if err != nil {
		t.Fatalf("wagerqueue.Decode = %v, want nil", err)
	}
	if overHTTP != overQueue.Command {
		t.Fatalf("command over the queue = %+v, want the one over HTTP %+v", overQueue.Command, overHTTP)
	}
}

// queueMessage is the same bet as submission, in the envelope of the queue.
func queueMessage(t *testing.T) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(submission(nil)), &body); err != nil {
		t.Fatalf("Unmarshal of the submission = %v, want nil", err)
	}
	body["idempotencyKey"] = "key-1"
	raw, err := json.Marshal(map[string]any{
		"messageId":     "44444444-4444-4444-8444-444444444444",
		"correlationId": "corr-1",
		"data":          body,
	})
	if err != nil {
		t.Fatalf("Marshal of the message = %v, want nil", err)
	}
	return raw
}
