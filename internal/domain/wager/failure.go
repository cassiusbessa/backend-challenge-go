package wager

import "errors"

var ErrUnknownFailureCode = errors.New("wager: failure code is not in the catalog")

// FailureCode is the stable token of a business rejection.
//
// The catalog is closed on purpose: the border's status map and the metric
// series per code both need an enumerable list to be exhaustive, and a token
// invented at a call site is one that reaches the external contract with no
// status and no series.
type FailureCode uint8

const (
	noFailureCode FailureCode = iota
	InsufficientFunds
	ReversalInsufficientFunds
	ReferenceNotFound
	ReferenceNotProcessed
	ReferenceUnsuccessful
	AlreadyReversed
	PlayerWalletMismatch
	CurrencyMismatch
	ReversalAmountMismatch
	ReferenceMismatch
	IdempotencyConflict
	DuplicateExternalTransaction
	WalletNotFound
	OpeningNotAllowed
	AmountNotAllowedForKind
	ReferenceRequired
	failureCodeCount
)

var failureTokens = [failureCodeCount]string{
	InsufficientFunds:            "INSUFFICIENT_FUNDS",
	ReversalInsufficientFunds:    "REVERSAL_INSUFFICIENT_FUNDS",
	ReferenceNotFound:            "REFERENCE_NOT_FOUND",
	ReferenceNotProcessed:        "REFERENCE_NOT_PROCESSED",
	ReferenceUnsuccessful:        "REFERENCE_UNSUCCESSFUL",
	AlreadyReversed:              "ALREADY_REVERSED",
	PlayerWalletMismatch:         "PLAYER_WALLET_MISMATCH",
	CurrencyMismatch:             "CURRENCY_MISMATCH",
	ReversalAmountMismatch:       "REVERSAL_AMOUNT_MISMATCH",
	ReferenceMismatch:            "REFERENCE_MISMATCH",
	IdempotencyConflict:          "IDEMPOTENCY_CONFLICT",
	DuplicateExternalTransaction: "DUPLICATE_EXTERNAL_TRANSACTION",
	WalletNotFound:               "WALLET_NOT_FOUND",
	OpeningNotAllowed:            "OPENING_NOT_ALLOWED",
	AmountNotAllowedForKind:      "AMOUNT_NOT_ALLOWED_FOR_KIND",
	ReferenceRequired:            "REFERENCE_REQUIRED",
}

// Catalog lists every token, in declaration order. The border builds its
// status map from this list, and the exhaustiveness test walks it.
func Catalog() []FailureCode {
	// Mutants of this capacity hint are immortal: append sizes the slice, so
	// the arithmetic here reaches no observable of the returned list.
	out := make([]FailureCode, 0, failureCodeCount-1)
	for code := InsufficientFunds; code < failureCodeCount; code++ {
		out = append(out, code)
	}
	return out
}

// String answers the stable token, or the empty string for a code outside the
// catalog.
func (c FailureCode) String() string {
	if c >= failureCodeCount {
		return ""
	}
	return failureTokens[c]
}

func (c FailureCode) IsZero() bool {
	return c == noFailureCode
}

// ParseFailureCode reads the token back from a persisted row.
func ParseFailureCode(text string) (FailureCode, error) {
	for _, code := range Catalog() {
		if code.String() == text {
			return code, nil
		}
	}
	return noFailureCode, ErrUnknownFailureCode
}

// Rejection is a business refusal carrying a catalog token. It is a value, not
// a panic, and the border reaches it with errors.As.
//
// When the refusal started as a condition in another package — the wallet
// answering that the balance does not cover a debit — that condition travels
// as the cause, so errors.Is still finds it.
type Rejection struct {
	code  FailureCode
	cause error
}

// NewRejection refuses a token outside the catalog rather than letting an
// unknown one reach the external contract: it answers ErrUnknownFailureCode,
// which is not a Rejection, so the caller cannot mistake one for the other.
// The cause may be nil.
func NewRejection(code FailureCode, cause error) error {
	if code.String() == "" {
		return ErrUnknownFailureCode
	}
	return Rejection{code: code, cause: cause}
}

func (r Rejection) Code() FailureCode {
	return r.code
}

func (r Rejection) Error() string {
	if r.cause == nil {
		return "wager: rejected with " + r.code.String()
	}
	return "wager: rejected with " + r.code.String() + ": " + r.cause.Error()
}

func (r Rejection) Unwrap() error {
	return r.cause
}
