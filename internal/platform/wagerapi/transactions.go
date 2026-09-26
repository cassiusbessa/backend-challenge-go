package wagerapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/authz"
	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
)

// Route is the path of the wager transactions, which is also what the created
// resource is pointed at.
const Route = "/wagering/transactions"

// The two refusals of permission a handler decides. Neither names a provider, a
// transaction or an amount: what they say is that the client may not ask this.
var (
	errOtherProvider = fmt.Errorf("wagerapi: %w: the body names another provider", problem.ErrNotPermitted)
	errNoClient      = fmt.Errorf("wagerapi: %w: the request carries no resolved client", problem.ErrNotPermitted)
)

// Submitter is the use case of the submission, as the border needs it.
type Submitter interface {
	Submit(ctx context.Context, cmd submitwager.Command) (submitwager.Result, error)
}

// Reader is the query of one transaction, as the border needs it.
type Reader interface {
	Transaction(ctx context.Context, id identity.TransactionID, provider identity.ProviderID) (storage.TransactionView, error)
}

// submittedResponse is what the submission answers, under the names of the
// challenge statement.
//
// Money leaves as a decimal string of two places, which is what money.MarshalJSON
// writes. Balance is a pointer so that an outcome carrying none leaves the field
// out instead of answering a balance of zero. IdempotentReplay is always
// written: every arrival of an operation is either the first or a replay, and
// false says the first as plainly as true says the replay.
type submittedResponse struct {
	TransactionID         string       `json:"transactionId"`
	Kind                  string       `json:"kind"`
	Status                string       `json:"status"`
	ExternalTransactionID string       `json:"externalTransactionId"`
	Money                 money.Money  `json:"money"`
	Balance               *money.Money `json:"balance,omitempty"`
	IdempotentReplay      bool         `json:"idempotentReplay"`
}

// recordedResponse is what a read answers: the row as it was recorded. It
// carries no replay marker, because a read is not an arrival of the operation,
// and the provider, which the submission leaves out because the caller is it.
type recordedResponse struct {
	TransactionID         string       `json:"transactionId"`
	Kind                  string       `json:"kind"`
	Status                string       `json:"status"`
	ProviderID            string       `json:"providerId"`
	ExternalTransactionID string       `json:"externalTransactionId"`
	Money                 money.Money  `json:"money"`
	Balance               *money.Money `json:"balance,omitempty"`
	FailureCode           string       `json:"failureCode,omitempty"`
}

// Submit serves POST /wagering/transactions. The operation ends PROCESSED,
// REJECTED or PENDING_REFERENCE in one commit: the first completion answers 201
// pointing at the resource, one that is accepted and waits answers 202 pointing
// at it too, and a replay of either answers 200.
func Submit(submitter Submitter, reporter *Reporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cmd, err := decodeSubmit(w, r)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		if err := speaksFor(r, cmd.ProviderID); err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		settled, err := submitter.Submit(r.Context(), cmd)
		if err != nil {
			// A rule that refused wrote a row of its own, and the result names it.
			// Rejected takes it; the refusals above wrote nothing to name.
			reporter.Rejected(w, r, settled, err)
			return
		}
		reporter.Settled(r, settled)
		answer(w, settled)
	})
}

// Read serves GET /wagering/transactions/{transactionId}. A transaction of another
// provider answers the same 404 as one that does not exist, because the provider
// of the token is part of the query.
//
// A transaction closed by a rule answers 200 with its token: the read succeeded,
// and problem details is reserved for the refusal of the request itself.
func Read(reader Reader, reporter *Reporter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := decodeTransactionID(r)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		provider, err := providerOf(r)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		found, err := reader.Transaction(r.Context(), id, provider)
		if err != nil {
			reporter.Refuse(w, r, err)
			return
		}
		write(w, http.StatusOK, viewResponse(found))
	})
}

// providerOf answers the provider of the client the token resolved to. It is the
// only source of the provider a request speaks for: an identifier in the payload
// authorizes nothing.
func providerOf(r *http.Request) (identity.ProviderID, error) {
	client, ok := authz.ClientOf(r.Context())
	if !ok {
		return identity.ProviderID{}, errNoClient
	}
	return client.ProviderID(), nil
}

// speaksFor refuses a body that declares another provider, before the use case and
// therefore before any movement.
func speaksFor(r *http.Request, declared identity.ProviderID) error {
	provider, err := providerOf(r)
	if err != nil {
		return err
	}
	if provider != declared {
		return errOtherProvider
	}
	return nil
}

// answer writes the outcome. The status describes the request and not the row:
// the first completion created the resource, one that was accepted and is
// waiting created it too without deciding it, and a replay found it already
// there.
//
// The wait answers its own code rather than 201 with the status in the body:
// stacking the two under one number would make the provider read the body to
// learn whether money moved, and telling that apart is the whole point of a code
// of its own.
func answer(w http.ResponseWriter, settled submitwager.Result) {
	body := settledResponse(settled)
	if settled.IdempotentReplay {
		write(w, http.StatusOK, body)
		return
	}
	w.Header().Set("Location", Route+"/"+settled.TransactionID.String())
	write(w, statusOf(settled), body)
}

// statusOf answers the code of a first outcome: the wait was accepted and not
// completed, and no rule refused it, so it is neither 201 nor problem details.
func statusOf(settled submitwager.Result) int {
	if settled.Status == wager.PendingReference {
		return http.StatusAccepted
	}
	return http.StatusCreated
}

func settledResponse(settled submitwager.Result) submittedResponse {
	return submittedResponse{
		TransactionID:         settled.TransactionID.String(),
		Kind:                  settled.Kind.String(),
		Status:                settled.Status.String(),
		ExternalTransactionID: settled.ExternalID.String(),
		Money:                 settled.Amount,
		Balance:               balanceOf(settled.ObservedBalance),
		IdempotentReplay:      settled.IdempotentReplay,
	}
}

func viewResponse(found storage.TransactionView) recordedResponse {
	return recordedResponse{
		TransactionID:         found.ID.String(),
		Kind:                  found.Kind.String(),
		Status:                found.Status.String(),
		ProviderID:            found.ProviderID.String(),
		ExternalTransactionID: found.ExternalID.String(),
		Money:                 found.Amount,
		Balance:               balanceOf(found.ObservedBalance),
		FailureCode:           found.FailureCode.String(),
	}
}

// balanceOf answers nothing for money that was never set. The zero value of Money
// carries no currency, which is what tells an absent balance from a balance of
// zero.
func balanceOf(balance money.Money) *money.Money {
	if balance.Currency().IsZero() {
		return nil
	}
	return &balance
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// A client that hung up leaves nothing to answer with, and the status line
	// already left.
	_ = json.NewEncoder(w).Encode(body)
}
