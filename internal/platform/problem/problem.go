// Package problem writes the error body of the HTTP border.
//
// Every refusal leaves in the single form of RFC 9457, and the numeric status
// map of the border lives here and nowhere else.
package problem

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
)

// retryAfter is how long a Retryable answer asks the caller to wait. One second is
// the base of the backoff the rest of the system already uses.
const retryAfter = "1"

// MediaType is what every error response of the border carries.
const MediaType = "application/problem+json"

var (
	// ErrInvalidInput is what the decoder wraps: a malformed body, a missing
	// field, an amount that is not a two-place decimal, an unknown currency, an
	// identifier out of format. It never reaches a write.
	ErrInvalidInput = errors.New("problem: request is not valid")

	// ErrNotPermitted is a valid identity without permission for what the request
	// asks. The guard refuses the route by role; this is the refusal a handler
	// decides afterwards, such as a body speaking for another provider.
	ErrNotPermitted = errors.New("problem: client has no permission")
)

const namespace = "urn:junglegaming:problem:"

// Class is the kind of refusal, which decides the type, the title and the
// number. The zero value is not a class.
type Class uint8

const (
	// InvalidInput is the body or the URL the border could not accept.
	InvalidInput Class = iota + 1
	// Unauthenticated is a credential absent, malformed, expired or from
	// another issuer.
	Unauthenticated
	// Unauthorized is a valid identity with no permission for the route.
	Unauthorized
	// NotFound is the resource the URL names not existing.
	NotFound
	// WalletExists is the second wallet of a player in the same currency. It
	// carries no failureCode: the closed catalog names wager operations, and an
	// opening is not one.
	WalletExists
	// BusinessRejection is a rule refusing the operation. It is the only class
	// that carries a token.
	BusinessRejection
	// Unavailable is infrastructure: the database out, a deadline exceeded, a
	// version conflict. It is a real outage, and it carries a stack captured at
	// the boundary where it was first seen.
	Unavailable
	// Retryable is a condition that resolves by itself shortly: an operation
	// recorded under the key that has not reached its outcome yet, or a race
	// whose winner rolled back. Nothing is broken, so the span stays ok and no
	// stack is recorded.
	Retryable
	// Internal is a defect of this service: a failure it let through where it
	// should have refused earlier, or one no border could name. Sending the same
	// request again cannot fix it.
	Internal
)

// Details is the body of a refusal. Detail names the field that was refused and
// never its value: an amount or a credential echoed back is what the refusal
// exists to keep out. FailureCode is an extension member and is present only on
// a business rejection.
//
// IdempotentReplay is another extension member: it says the refusal is one that
// was already recorded and is being answered again. It carries no amount, no
// balance, no body and no key — only the fact that the outcome is not new.
//
// TransactionID is the last one: the transaction a rule recorded REJECTED for
// this refusal, on the first answer and on its replay, so the caller can read
// the row back. It is empty for a refusal that wrote no row.
type Details struct {
	Type             string `json:"type"`
	Title            string `json:"title"`
	Status           int    `json:"status"`
	Detail           string `json:"detail,omitempty"`
	Instance         string `json:"instance,omitempty"`
	FailureCode      string `json:"failureCode,omitempty"`
	IdempotentReplay bool   `json:"idempotentReplay,omitempty"`
	TransactionID    string `json:"transactionId,omitempty"`

	// Class is what this body was built from. It stays off the wire, which
	// carries the type and the number: the class is what the reporter asks to
	// decide the span and the stack, and the number alone cannot answer that.
	Class Class `json:"-"`
}

// Broken reports whether the outcome is one that marks the span and records a
// stack. A rule that answered, a refusal of the contract and a request that can
// be retried shortly all leave the span ok: nothing about them is broken.
func (d Details) Broken() bool {
	return d.Class == Unavailable || d.Class == Internal
}

// The numeric map of the border, in one place. The type names the class of the
// problem; the token of a business rejection names the rule, and the two are
// never the same field.
var classes = map[Class]Details{
	InvalidInput:      {Type: namespace + "invalid-input", Title: "Request is not valid", Status: http.StatusBadRequest},
	Unauthenticated:   {Type: namespace + "unauthenticated", Title: "Credential is absent or not valid", Status: http.StatusUnauthorized},
	Unauthorized:      {Type: namespace + "unauthorized", Title: "Client has no permission", Status: http.StatusForbidden},
	NotFound:          {Type: namespace + "not-found", Title: "Resource does not exist", Status: http.StatusNotFound},
	WalletExists:      {Type: namespace + "wallet-already-exists", Title: "Wallet already exists", Status: http.StatusConflict},
	BusinessRejection: {Type: namespace + "business-rejection", Title: "Wager rejected", Status: http.StatusUnprocessableEntity},
	Unavailable:       {Type: namespace + "unavailable", Title: "Service is unavailable", Status: http.StatusServiceUnavailable},
	Retryable:         {Type: namespace + "retryable", Title: "Request can be retried shortly", Status: http.StatusServiceUnavailable},
	Internal:          {Type: namespace + "internal", Title: "Service failed to handle the request", Status: http.StatusInternalServerError},
}

// Of answers the body of the class. An unknown class answers as internal: a
// border that cannot name the problem has a defect of its own, and it is not in a
// position to blame the caller or to invite a retry.
func Of(class Class) Details {
	details, ok := classes[class]
	if !ok {
		class = Internal
		details = classes[Internal]
	}
	details.Class = class
	return details
}

// From classifies the error the application answered.
//
// The two classes never mix: a business rejection is reached with errors.As and
// carries its token, and everything left over is infrastructure with no token
// at all.
func From(err error) Details {
	var rejection wager.Rejection
	if errors.As(err, &rejection) {
		details := Of(BusinessRejection)
		details.FailureCode = rejection.Code().String()
		details.IdempotentReplay = isReplay(err)
		return details
	}
	return Of(classOf(err))
}

// replayed is any refusal that says its outcome was already recorded. It is
// reached as a behaviour and not as a type, so the marker belongs to whoever
// answers a recorded outcome and this package does not import a use case.
type replayed interface {
	IdempotentReplay() bool
}

// isReplay is asked only about a business rejection, which is the single class
// with a recorded outcome to answer again: invalid input, a credential, a
// permission and a failure have nothing stored to replay.
func isReplay(err error) bool {
	var marked replayed
	return errors.As(err, &marked) && marked.IdempotentReplay()
}

// retryable is any failure that says the same request can be sent again shortly:
// nothing is broken and nothing was decided yet, so the caller repeats it instead
// of reading it as an outage.
//
// It is reached as a behaviour and not as a type, for the same reason as replayed:
// this package does not import a use case.
type retryable interface {
	RetryShortly() bool
}

// defective is any failure that says this service let through what it should have
// refused earlier. Sending the same request again cannot fix it.
type defective interface {
	Defect() bool
}

func asksToRetry(err error) bool {
	var marked retryable
	return errors.As(err, &marked) && marked.RetryShortly()
}

func isDefect(err error) bool {
	var marked defective
	return errors.As(err, &marked) && marked.Defect()
}

// carriesStack reports whether the failure was captured at an I/O boundary, which
// is what tells a real outage from an error nobody classified.
func carriesStack(err error) bool {
	var carried *fault.Error
	return errors.As(err, &carried)
}

// classOf names the class of a failure that is not a business rejection.
//
// The order is the point: a sentinel this border names wins over a marker the
// failure carries, so a condition with a known answer is never read as an outage.
func classOf(err error) Class {
	if named, ok := namedClassOf(err); ok {
		return named
	}
	return markedClassOf(err)
}

// namedClassOf answers the class of a condition named by its sentinel, and reports
// whether there is one.
func namedClassOf(err error) (Class, bool) {
	switch {
	case errors.Is(err, ErrInvalidInput):
		return InvalidInput, true
	case errors.Is(err, storage.ErrWalletExists):
		return WalletExists, true
	case errors.Is(err, storage.ErrNotFound):
		// The family, not each entity: this case is what keeps the classification
		// from growing an expression every time another row can be absent.
		return NotFound, true
	case errors.Is(err, ErrNotPermitted):
		return Unauthorized, true
	case errors.Is(err, storage.ErrLostWrite):
		// go-errors and go-observability both name the version conflict as
		// infrastructure, with the span marked and the stack recorded. It is named
		// here rather than left to the stack check, so the class does not depend
		// on which boundary happened to wrap it.
		return Unavailable, true
	}
	return 0, false
}

// markedClassOf answers the class of a failure that carries its own marker: the
// behaviour a use case set, or the stack a boundary captured.
//
// A failure carrying none of them is one nobody classified, and that is a defect
// of ours rather than an invitation to retry: answering 503 there would tell the
// provider to repeat a request that can never succeed.
func markedClassOf(err error) Class {
	switch {
	case asksToRetry(err):
		return Retryable
	case isDefect(err):
		return Internal
	case carriesStack(err):
		return Unavailable
	}
	return Internal
}

// Write answers the refusal. The instance defaults to the path of the request,
// which locates the call without repeating anything it carried.
func Write(w http.ResponseWriter, r *http.Request, details Details) {
	if details.Instance == "" {
		details.Instance = r.URL.Path
	}
	w.Header().Set("Content-Type", MediaType)
	if details.Class == Retryable {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.WriteHeader(details.Status)
	// A client that hung up mid-write leaves nothing to answer with, and the
	// status line already left.
	_ = json.NewEncoder(w).Encode(details)
}
