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
)

// MediaType is what every error response of the border carries.
const MediaType = "application/problem+json"

// ErrInvalidInput is what the decoder wraps: a malformed body, a missing field,
// an amount that is not a two-place decimal, an unknown currency, an identifier
// out of format. It never reaches a write.
var ErrInvalidInput = errors.New("problem: request is not valid")

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
	// version conflict.
	Unavailable
)

// Details is the body of a refusal. Detail names the field that was refused and
// never its value: an amount or a credential echoed back is what the refusal
// exists to keep out. FailureCode is an extension member and is present only on
// a business rejection.
type Details struct {
	Type        string `json:"type"`
	Title       string `json:"title"`
	Status      int    `json:"status"`
	Detail      string `json:"detail,omitempty"`
	Instance    string `json:"instance,omitempty"`
	FailureCode string `json:"failureCode,omitempty"`
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
}

// Of answers the body of the class. An unknown class answers as unavailable,
// because a border that cannot name the problem is not in a position to blame
// the caller.
func Of(class Class) Details {
	details, ok := classes[class]
	if !ok {
		return classes[Unavailable]
	}
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
		return details
	}
	return Of(classOf(err))
}

func classOf(err error) Class {
	switch {
	case errors.Is(err, ErrInvalidInput):
		return InvalidInput
	case errors.Is(err, storage.ErrWalletExists):
		return WalletExists
	case errors.Is(err, storage.ErrWalletNotFound):
		return NotFound
	}
	return Unavailable
}

// Write answers the refusal. The instance defaults to the path of the request,
// which locates the call without repeating anything it carried.
func Write(w http.ResponseWriter, r *http.Request, details Details) {
	if details.Instance == "" {
		details.Instance = r.URL.Path
	}
	w.Header().Set("Content-Type", MediaType)
	w.WriteHeader(details.Status)
	// A client that hung up mid-write leaves nothing to answer with, and the
	// status line already left.
	_ = json.NewEncoder(w).Encode(details)
}
