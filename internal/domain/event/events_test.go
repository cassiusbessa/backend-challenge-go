package event

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestNewProcessed_namesTheOperationAndTheBalanceItObserved(t *testing.T) {
	t.Parallel()
	built, err := NewProcessed(spec(t), processedBet(t))
	if err != nil {
		t.Fatalf("NewProcessed = %v, want nil", err)
	}
	if built.Type() != TypeProcessed {
		t.Fatalf("type = %s, want %s", built.Type(), TypeProcessed)
	}
	data := dataOf(t, built)
	if data["kind"] != "BET" || data["status"] != "PROCESSED" {
		t.Fatalf("kind and status = %v and %v, want BET and PROCESSED", data["kind"], data["status"])
	}
	if got := amountOf(t, data, "observedBalance"); got != "975.00" {
		t.Fatalf("observedBalance = %s, want 975.00", got)
	}
}

func TestNewRejected_carriesTheTokenOfTheRuleAndNoBalance(t *testing.T) {
	t.Parallel()
	built, err := NewRejected(spec(t), rejectedBet(t))
	if err != nil {
		t.Fatalf("NewRejected = %v, want nil", err)
	}
	if built.Type() != TypeRejected {
		t.Fatalf("type = %s, want %s", built.Type(), TypeRejected)
	}
	data := dataOf(t, built)
	if data["failureCode"] != "INSUFFICIENT_FUNDS" {
		t.Fatalf("failureCode = %v, want INSUFFICIENT_FUNDS", data["failureCode"])
	}
	if _, present := data["observedBalance"]; present {
		t.Fatalf("the rejection carries observedBalance, want the field absent")
	}
}

func TestNewPendingReference_namesTheCitedOperationAndTheDeadline(t *testing.T) {
	t.Parallel()
	built, err := NewPendingReference(spec(t), waitingWin(t))
	if err != nil {
		t.Fatalf("NewPendingReference = %v, want nil", err)
	}
	if built.Type() != TypePendingReference {
		t.Fatalf("type = %s, want %s", built.Type(), TypePendingReference)
	}
	data := dataOf(t, built)
	if data["referenceExternalTransactionId"] != "tx-000" {
		t.Fatalf("cited operation = %v, want tx-000", data["referenceExternalTransactionId"])
	}
	if data["referenceDeadlineAt"] != "2026-09-24T12:15:00Z" {
		t.Fatalf("deadline = %v, want 2026-09-24T12:15:00Z", data["referenceDeadlineAt"])
	}
}

func TestNewBalanceChanged_carriesTheMovementThatProducedIt(t *testing.T) {
	t.Parallel()
	built, err := NewBalanceChanged(spec(t), credit(t), 7)
	if err != nil {
		t.Fatalf("NewBalanceChanged = %v, want nil", err)
	}
	if built.Type() != TypeBalanceChanged {
		t.Fatalf("type = %s, want %s", built.Type(), TypeBalanceChanged)
	}
	data := dataOf(t, built)
	if data["direction"] != "CREDIT" {
		t.Fatalf("direction = %v, want CREDIT", data["direction"])
	}
	if data["walletVersion"] != float64(7) {
		t.Fatalf("walletVersion = %v, want 7", data["walletVersion"])
	}
	assertCloses(t, data)
}

// assertCloses checks the arithmetic the requirement fixes: on a credit, the
// balance after is the balance before plus the amount.
func assertCloses(t *testing.T, data map[string]any) {
	t.Helper()
	before, amount, after := amountOf(t, data, "balanceBefore"), amountOf(t, data, "money"), amountOf(t, data, "balanceAfter")
	if before != "100.00" || amount != "50.00" || after != "150.00" {
		t.Fatalf("movement = %s + %s -> %s, want 100.00 + 50.00 -> 150.00", before, amount, after)
	}
}

func spec(t *testing.T) Spec {
	t.Helper()
	return Spec{ID: eventID(t), AggregateID: walletID(t), At: at}
}

var at = time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)

const (
	eventUUID       = "019974a4-0000-7000-8000-00000000e001"
	walletUUID      = "019974a4-0000-7000-8000-00000000a11e"
	playerUUID      = "019974a4-0000-7000-8000-000000000b11"
	transactionUUID = "019974a4-0000-7000-8000-0000000000c1"
	entryUUID       = "019974a4-0000-7000-8000-0000000000d1"
)

func eventID(t *testing.T) identity.EventID {
	t.Helper()
	parsed, err := identity.ParseEventID(eventUUID)
	if err != nil {
		t.Fatalf("ParseEventID = %v, want nil", err)
	}
	return parsed
}

func walletID(t *testing.T) identity.WalletID {
	t.Helper()
	parsed, err := identity.ParseWalletID(walletUUID)
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	return parsed
}

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse of %s = %v, want nil", amount, err)
	}
	return parsed
}

// betSpec is one operation of a provider, complete but undecided. Each case
// takes it and moves it to the single status that case is about.
func betSpec(t *testing.T) wager.ExternalSpec {
	t.Helper()
	player, err := identity.ParsePlayerID(playerUUID)
	if err != nil {
		t.Fatalf("ParsePlayerID = %v, want nil", err)
	}
	transaction, err := identity.ParseTransactionID(transactionUUID)
	if err != nil {
		t.Fatalf("ParseTransactionID = %v, want nil", err)
	}
	return wager.ExternalSpec{
		ID:             transaction,
		ProviderID:     tokenOf(t, identity.ParseProviderID, "provider-a"),
		ExternalID:     tokenOf(t, identity.ParseExternalTransactionID, "tx-001"),
		IdempotencyKey: tokenOf(t, identity.ParseIdempotencyKey, "key-001"),
		BodyHash:       "hash",
		PlayerID:       player,
		WalletID:       walletID(t),
		RoundID:        tokenOf(t, identity.ParseRoundID, "round-1"),
		GameID:         tokenOf(t, identity.ParseGameID, "crash"),
		Kind:           wager.KindBet,
		Amount:         brl(t, "25.00"),
		At:             at,
	}
}

// tokenOf parses one provider identifier through the constructor it belongs to,
// so a test never builds an identity the border would have refused.
func tokenOf[T any](t *testing.T, parse func(string) (T, error), text string) T {
	t.Helper()
	parsed, err := parse(text)
	if err != nil {
		t.Fatalf("parse of %q = %v, want nil", text, err)
	}
	return parsed
}

func external(t *testing.T, spec wager.ExternalSpec) *wager.Transaction {
	t.Helper()
	op, err := wager.NewExternal(spec)
	if err != nil {
		t.Fatalf("wager.NewExternal = %v, want nil", err)
	}
	return op
}

func processedBet(t *testing.T) *wager.Transaction {
	t.Helper()
	op := external(t, betSpec(t))
	if err := op.Process(brl(t, "975.00"), at); err != nil {
		t.Fatalf("Process = %v, want nil", err)
	}
	return op
}

func rejectedBet(t *testing.T) *wager.Transaction {
	t.Helper()
	op := external(t, betSpec(t))
	if err := op.Reject(wager.InsufficientFunds, at); err != nil {
		t.Fatalf("Reject = %v, want nil", err)
	}
	return op
}

func waitingWin(t *testing.T) *wager.Transaction {
	t.Helper()
	spec := betSpec(t)
	spec.Kind = wager.KindWin
	spec.ReferenceExternalID = tokenOf(t, identity.ParseExternalTransactionID, "tx-000")
	op := external(t, spec)
	deadline := at.Add(15 * time.Minute)
	if err := op.WaitForReference(at.Add(time.Second), deadline, at); err != nil {
		t.Fatalf("WaitForReference = %v, want nil", err)
	}
	return op
}

func credit(t *testing.T) ledger.Entry {
	t.Helper()
	entryID, err := identity.ParseLedgerEntryID(entryUUID)
	if err != nil {
		t.Fatalf("ParseLedgerEntryID = %v, want nil", err)
	}
	transaction, err := identity.ParseTransactionID(transactionUUID)
	if err != nil {
		t.Fatalf("ParseTransactionID = %v, want nil", err)
	}
	entry, err := ledger.NewEntry(ledger.EntrySpec{
		ID:            entryID,
		WalletID:      walletID(t),
		TransactionID: transaction,
		Direction:     ledger.Credit,
		Amount:        brl(t, "50.00"),
		BalanceBefore: brl(t, "100.00"),
		BalanceAfter:  brl(t, "150.00"),
		Sequence:      7,
		CreatedAt:     at,
	})
	if err != nil {
		t.Fatalf("ledger.NewEntry = %v, want nil", err)
	}
	return entry
}

// dataOf reads the data of the marshalled envelope, which is the only form the
// payload is ever seen in outside this package.
func dataOf(t *testing.T, built Envelope) map[string]any {
	t.Helper()
	return wireOf(t, built)["data"].(map[string]any)
}

func wireOf(t *testing.T, built Envelope) map[string]any {
	t.Helper()
	return marshalWith(t, built, Origin{CorrelationID: "corr-1"})
}

func marshalWith(t *testing.T, built Envelope, origin Origin) map[string]any {
	t.Helper()
	raw, err := built.Marshal(origin)
	if err != nil {
		t.Fatalf("Marshal = %v, want nil", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal = %v, want nil", err)
	}
	return out
}

// amountOf reads one money field, which crosses as the decimal string and the
// currency beside it and never as a number.
func amountOf(t *testing.T, data map[string]any, field string) string {
	t.Helper()
	value, ok := data[field].(map[string]any)
	if !ok {
		t.Fatalf("%s = %v, want the money pair", field, data[field])
	}
	amount, ok := value["amount"].(string)
	if !ok {
		t.Fatalf("%s.amount = %v, want a decimal string", field, value["amount"])
	}
	return amount
}
