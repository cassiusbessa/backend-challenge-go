package event

import (
	"errors"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/ledger"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
)

// ErrMissingReference is a wait recorded over an operation that cites none. The
// payload of WagerTransactionPendingReference names the cited operation and its
// deadline, and neither has an empty form that a consumer could read.
var ErrMissingReference = errors.New("event: pending reference cites no operation")

// The four payloads. Each names the event it belongs to through eventType, so
// the envelope constructor never takes a type from a caller.
//
// Money crosses as the pair of the external contract — a decimal string of two
// places and the currency beside it — because money.Money marshals that way and
// the outbound contract reads like the inbound one.
//
// Nothing here carries an idempotency key, a header or a body: what the
// consumer needs is the operation and its outcome.

// ProcessedData is what WagerTransactionProcessed carries: the operation that
// concluded and the balance observed by the commit that concluded it.
//
// The provider fields are absent on the opening of a wallet, which is internal,
// and omitted rather than sent empty.
type ProcessedData struct {
	TransactionID         string      `json:"transactionId"`
	WalletID              string      `json:"walletId"`
	PlayerID              string      `json:"playerId"`
	ProviderID            string      `json:"providerId,omitempty"`
	ExternalTransactionID string      `json:"externalTransactionId,omitempty"`
	RoundID               string      `json:"roundId,omitempty"`
	GameID                string      `json:"gameId,omitempty"`
	Kind                  string      `json:"kind"`
	Status                string      `json:"status"`
	Money                 money.Money `json:"money"`
	ObservedBalance       money.Money `json:"observedBalance"`
}

func (ProcessedData) eventType() Type { return TypeProcessed }

// RejectedData is what WagerTransactionRejected carries: the operation a rule
// refused and the stable token of that rule.
//
// There is no balance: a rejection moves none.
type RejectedData struct {
	TransactionID         string      `json:"transactionId"`
	WalletID              string      `json:"walletId"`
	PlayerID              string      `json:"playerId"`
	ProviderID            string      `json:"providerId,omitempty"`
	ExternalTransactionID string      `json:"externalTransactionId,omitempty"`
	RoundID               string      `json:"roundId,omitempty"`
	GameID                string      `json:"gameId,omitempty"`
	Kind                  string      `json:"kind"`
	Status                string      `json:"status"`
	Money                 money.Money `json:"money"`
	FailureCode           string      `json:"failureCode"`
}

func (RejectedData) eventType() Type { return TypeRejected }

// PendingReferenceData is what WagerTransactionPendingReference carries: the
// operation that is waiting, the one it cites, and the instant the clock closes
// the wait at.
type PendingReferenceData struct {
	TransactionID                  string      `json:"transactionId"`
	WalletID                       string      `json:"walletId"`
	PlayerID                       string      `json:"playerId"`
	ProviderID                     string      `json:"providerId,omitempty"`
	ExternalTransactionID          string      `json:"externalTransactionId,omitempty"`
	RoundID                        string      `json:"roundId,omitempty"`
	GameID                         string      `json:"gameId,omitempty"`
	Kind                           string      `json:"kind"`
	Status                         string      `json:"status"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId"`
	ReferenceDeadlineAt            string      `json:"referenceDeadlineAt"`
}

func (PendingReferenceData) eventType() Type { return TypePendingReference }

// BalanceChangedData is what WalletBalanceChanged carries: the movement that
// produced it, with both balances and the version the wallet reached.
//
// The fields are the ones go-outbox fixes, and the two balances close with the
// direction because the ledger entry they come from already does.
type BalanceChangedData struct {
	WalletID      string      `json:"walletId"`
	TransactionID string      `json:"transactionId"`
	Direction     string      `json:"direction"`
	Money         money.Money `json:"money"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
}

func (BalanceChangedData) eventType() Type { return TypeBalanceChanged }

// NewProcessed builds the event of an operation that concluded, which includes
// a LOSS and the opening of a wallet with a positive balance.
func NewProcessed(spec Spec, op *wager.Transaction) (Envelope, error) {
	return New(spec, ProcessedData{
		TransactionID:         op.ID().String(),
		WalletID:              op.WalletID().String(),
		PlayerID:              op.PlayerID().String(),
		ProviderID:            op.ProviderID().String(),
		ExternalTransactionID: op.ExternalID().String(),
		RoundID:               op.RoundID().String(),
		GameID:                op.GameID().String(),
		Kind:                  op.Kind().String(),
		Status:                op.Status().String(),
		Money:                 op.Amount(),
		ObservedBalance:       op.ObservedBalance(),
	})
}

// NewRejected builds the event of an operation a rule refused, carrying the
// token recorded on the row.
func NewRejected(spec Spec, op *wager.Transaction) (Envelope, error) {
	return New(spec, RejectedData{
		TransactionID:         op.ID().String(),
		WalletID:              op.WalletID().String(),
		PlayerID:              op.PlayerID().String(),
		ProviderID:            op.ProviderID().String(),
		ExternalTransactionID: op.ExternalID().String(),
		RoundID:               op.RoundID().String(),
		GameID:                op.GameID().String(),
		Kind:                  op.Kind().String(),
		Status:                op.Status().String(),
		Money:                 op.Amount(),
		FailureCode:           op.FailureCode().String(),
	})
}

// NewPendingReference builds the event of a wait that was recorded.
//
// It answers [ErrMissingReference] for an operation that cites none: a wait
// with nothing to wait for is not a wait, and the two fields of the payload
// that name the cited operation have no empty form.
func NewPendingReference(spec Spec, op *wager.Transaction) (Envelope, error) {
	cited, ok := op.ReferenceExternalID()
	if !ok {
		return Envelope{}, ErrMissingReference
	}
	return New(spec, PendingReferenceData{
		TransactionID:                  op.ID().String(),
		WalletID:                       op.WalletID().String(),
		PlayerID:                       op.PlayerID().String(),
		ProviderID:                     op.ProviderID().String(),
		ExternalTransactionID:          op.ExternalID().String(),
		RoundID:                        op.RoundID().String(),
		GameID:                         op.GameID().String(),
		Kind:                           op.Kind().String(),
		Status:                         op.Status().String(),
		Money:                          op.Amount(),
		ReferenceExternalTransactionID: cited.String(),
		ReferenceDeadlineAt:            op.ReferenceDeadlineAt().UTC().Format(time.RFC3339Nano),
	})
}

// NewBalanceChanged builds the event of a movement, from the ledger entry that
// recorded it and the version the wallet reached.
//
// The version is handed in rather than read off the entry: the entry numbers
// its own sequence, and a sequence that happens to match the version today is
// not the same fact.
func NewBalanceChanged(spec Spec, entry ledger.Entry, walletVersion int64) (Envelope, error) {
	return New(spec, BalanceChangedData{
		WalletID:      entry.WalletID().String(),
		TransactionID: entry.TransactionID().String(),
		Direction:     entry.Direction().String(),
		Money:         entry.Amount(),
		BalanceBefore: entry.BalanceBefore(),
		BalanceAfter:  entry.BalanceAfter(),
		WalletVersion: walletVersion,
	})
}
