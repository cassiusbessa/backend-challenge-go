package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

func TestState_rebuildsTheAggregateStateFromTheRow(t *testing.T) {
	t.Parallel()
	state, err := validRow().state()
	if err != nil {
		t.Fatalf("state = %v, want nil", err)
	}
	assertStateIdentities(t, state)
	assertStateOutcome(t, state)
	assertStateMoney(t, state)
}

func assertStateIdentities(t *testing.T, state wager.State) {
	t.Helper()
	if state.ID.String() != rowTransaction || state.WalletID.String() != rowWallet {
		t.Fatalf("identities = %s and %s, want %s and %s", state.ID, state.WalletID, rowTransaction, rowWallet)
	}
	if state.BodyHash != rowHash {
		t.Fatalf("body hash = %s, want the one stored on the row", state.BodyHash)
	}
}

func assertStateOutcome(t *testing.T, state wager.State) {
	t.Helper()
	if state.Kind != wager.KindBet || state.Status != wager.Processed {
		t.Fatalf("kind and status = %s and %s, want BET and PROCESSED", state.Kind, state.Status)
	}
}

func assertStateMoney(t *testing.T, state wager.State) {
	t.Helper()
	if state.Amount.Amount() != "25.00" || state.ObservedBalance.Amount() != "75.00" {
		t.Fatalf("amount and observed = %s and %s, want 25.00 and 75.00", state.Amount.Amount(), state.ObservedBalance.Amount())
	}
}

func TestView_answersTheRecordedOutcomeOfTheRow(t *testing.T) {
	t.Parallel()
	view, err := validRow().view()
	if err != nil {
		t.Fatalf("view = %v, want nil", err)
	}
	if view.ID.String() != rowTransaction || view.ProviderID.String() != rowProvider {
		t.Fatalf("view identities = %s and %s, want %s and %s", view.ID, view.ProviderID, rowTransaction, rowProvider)
	}
	if view.Status != wager.Processed || view.FailureCode != wager.FailureCode(0) {
		t.Fatalf("view outcome = %s with the code %v, want PROCESSED with no code", view.Status, view.FailureCode)
	}
}

// A column the domain cannot parse is a row this context did not write. The
// refusal names the operation and the state comes back zeroed.
func TestState_refusesARowWithAColumnOutsideItsVocabulary(t *testing.T) {
	t.Parallel()
	row := validRow()
	row.kind = "DOUBLE"
	state, err := row.state()
	if err == nil {
		t.Fatalf("state of an unknown kind = %+v with no error, want a refusal", state)
	}
	if !strings.Contains(err.Error(), "read transaction row") {
		t.Fatalf("refusal = %q, want the operation named in the chain", err.Error())
	}
	if state.ID.String() != (wager.State{}).ID.String() {
		t.Fatalf("state = %+v, want the zero value beside the refusal", state)
	}
}

// Each column goes to the parser of its own vocabulary, so a value that is valid
// somewhere else is still refused here. This is what pins the mapping of the
// twenty columns to their types.
func TestKeep_keepsTheFirstRefusalOfTheRow(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.keep(errors.New("the first refusal"))
	parse.keep(errors.New("the second refusal"))
	if parse.err == nil || parse.err.Error() != "the first refusal" {
		t.Fatalf("kept refusal = %v, want the first one", parse.err)
	}
}

func TestTransactionID_refusesAValueThatIsNotACanonicalUUID(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.transactionID("not-a-uuid")
	assertRefused(t, parse.err, "a transaction out of format")
}

func TestPlayerID_refusesAValueThatIsNotACanonicalUUID(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.playerID("not-a-uuid")
	assertRefused(t, parse.err, "a player out of format")
}

func TestWalletID_refusesAValueThatIsNotACanonicalUUID(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.walletID("not-a-uuid")
	assertRefused(t, parse.err, "a wallet out of format")
}

func TestProviderID_refusesAColumnThatIsBlank(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.providerID(" ")
	assertRefused(t, parse.err, "a blank provider")
}

func TestExternalID_refusesAColumnThatIsBlank(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.externalID(" ")
	assertRefused(t, parse.err, "a blank external identifier")
}

func TestIdempotencyKey_refusesAColumnThatIsBlank(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.idempotencyKey(" ")
	assertRefused(t, parse.err, "a blank key")
}

func TestRoundID_refusesAColumnThatIsBlank(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.roundID(" ")
	assertRefused(t, parse.err, "a blank round")
}

func TestGameID_refusesAColumnThatIsBlank(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.gameID(" ")
	assertRefused(t, parse.err, "a blank game")
}

func TestKind_refusesAWordOutsideTheVocabularyOfKinds(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.kind("PROCESSED")
	assertRefused(t, parse.err, "a status where the kind belongs")
}

func TestStatus_refusesAWordOutsideTheStateMachine(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.status("BET")
	assertRefused(t, parse.err, "a kind where the status belongs")
}

// PROCESSING is the state the lifecycle does not have, and a row carrying it is
// not a row this context wrote.
func TestStatus_refusesTheStateTheLifecycleDoesNotHave(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.status("PROCESSING")
	assertRefused(t, parse.err, "a state outside the machine")
}

func TestMoney_refusesACodeOutsideTheVocabulary(t *testing.T) {
	t.Parallel()
	var parse rowParser
	parse.money(2500, "BRLL")
	assertRefused(t, parse.err, "an unknown currency")
}

// A NULL reference is what a transaction citing no other operation carries, and
// it is an absence rather than a refusal.
func TestOptionalExternalID_answersTheZeroIdentifierForANullColumn(t *testing.T) {
	t.Parallel()
	var parse rowParser
	if got := parse.optionalExternalID(nil); !got.IsZero() {
		t.Fatalf("reference of a NULL column = %s, want the zero identifier", got)
	}
	if parse.err != nil {
		t.Fatalf("parser refusal = %v, want nil: a NULL column is an absence", parse.err)
	}
}

func TestFailureCode_answersTheZeroCodeForANullColumn(t *testing.T) {
	t.Parallel()
	var parse rowParser
	if got := parse.failureCode(nil); got != wager.FailureCode(0) {
		t.Fatalf("code of a NULL column = %v, want the zero code", got)
	}
	if parse.err != nil {
		t.Fatalf("parser refusal on a NULL code = %v, want nil", parse.err)
	}
}

// The zero Money carries no currency, which is what tells an absent balance from
// a balance of zero.
func TestOptionalMoney_tellsAnAbsentBalanceFromABalanceOfZero(t *testing.T) {
	t.Parallel()
	var parse rowParser
	absent := parse.optionalMoney(nil, "BRL")
	if !absent.Currency().IsZero() {
		t.Fatalf("currency of a NULL balance = %s, want none", absent.Currency().Code())
	}
	zero := parse.optionalMoney(pointerTo(int64(0)), "BRL")
	if zero.Currency().Code() != "BRL" || !zero.IsZero() {
		t.Fatalf("balance of zero cents = %s, want 0.00 BRL", zero)
	}
}

func TestInstantOf_answersTheZeroInstantForANullColumn(t *testing.T) {
	t.Parallel()
	if got := instantOf(nil); !got.IsZero() {
		t.Fatalf("instant of a NULL column = %s, want the zero time", got)
	}
	at := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	if got := instantOf(&at); !got.Equal(at) {
		t.Fatalf("instant = %s, want %s", got, at)
	}
}

// The refusal of the querier crosses unchanged: pgx.ErrNoRows is what the caller
// above turns into the absence of the catalog.
func TestScanTransaction_answersTheRefusalOfTheQuerier(t *testing.T) {
	t.Parallel()
	_, err := scanTransaction(context.Background(), refusingQuerier{}, selectTransactionByKey, "provider-a", "key-1")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("scanTransaction = %v, want %v", err, pgx.ErrNoRows)
	}
}

func assertRefused(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the parser took %s, want it refused", what)
	}
}

// validRow is one PROCESSED bet as PostgreSQL hands it over, with every nullable
// column absent except the observed balance a PROCESSED row must carry.
func validRow() transactionRow {
	at := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	return transactionRow{
		id:         rowTransaction,
		kind:       "BET",
		playerID:   rowPlayer,
		walletID:   rowWallet,
		cents:      2500,
		currency:   "BRL",
		providerID: rowProvider,
		externalID: "external-1",
		key:        "key-1",
		bodyHash:   rowHash,
		roundID:    "round-1",
		gameID:     "game-1",
		status:     "PROCESSED",
		observed:   pointerTo(int64(7500)),
		createdAt:  at,
		updatedAt:  at,
	}
}

func pointerTo[T any](value T) *T {
	return &value
}

type refusingQuerier struct{}

func (refusingQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	return refusingRow{}
}

type refusingRow struct{}

func (refusingRow) Scan(...any) error {
	return pgx.ErrNoRows
}

const (
	rowTransaction = "33333333-3333-4333-8333-333333333333"
	rowPlayer      = "22222222-2222-4222-8222-222222222222"
	rowWallet      = "11111111-1111-4111-8111-111111111111"
	rowProvider    = "provider-a"
	rowHash        = "7f83b1657ff1fc53b92dc18148a1d65dfc2d4b1fa3d677284addd200126d9069"
)
