package broker

import (
	"errors"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"
	awshttp "github.com/aws/smithy-go/transport/http"
)

// Permanent reports whether the broker refused the send in a way the same bytes
// will never complete: the topic is not there, the parameters are refused, or
// the identity is not allowed to publish. Everything else — the broker being
// out, a deadline, throttling, a dropped connection — is worth trying again.
//
// It is a method of the client, and the single place that knows the shape of
// these errors, for the same reason the map of the PostgreSQL error codes is in
// one place: spread out, two callers end up reading the same refusal in two
// different ways, and one of them retries a row forever over a topic that does
// not exist.
//
// An error whose shape says nothing is not permanent. Giving up on a row is what
// cannot be undone, so the unknown takes the side that can be.
func (t *Topic) Permanent(err error) bool {
	if err == nil {
		return false
	}
	if worthRepeating(err) {
		return false
	}
	return permanentType(err) || permanentStatus(err)
}

// worthRepeating asks the SDK the question it already answers to decide its own
// retries: throttling by error code, the server side of the status range, a
// connection that broke and a deadline that ran out. A copy of those lists here
// would be a copy that ages apart from the API.
//
// Only a yes counts. The SDK answers about one call and this decides about a
// row, so "no opinion" — which is what it has about most modelled errors — is
// left to the two rules below.
//
// KMS throttling is the one it does not name: SNS answers it with a 400, and
// without it the status rule would read a broker asking to slow down as a
// refusal of the bytes.
func worthRepeating(err error) bool {
	if retry.IsErrorRetryables(retry.DefaultRetryables).IsErrorRetryable(err) == aws.TrueTernary {
		return true
	}
	var throttled *types.KMSThrottlingException
	return errors.As(err, &throttled)
}

// permanentType reads the modelled errors of the API: a topic that is not there,
// a parameter the API refuses, and a caller that is not authorized. None of the
// three is answered differently on a second attempt with the same bytes.
func permanentType(err error) bool {
	var notFound *types.NotFoundException
	var invalid *types.InvalidParameterException
	var value *types.InvalidParameterValueException
	var authorization *types.AuthorizationErrorException
	return errors.As(err, &notFound) ||
		errors.As(err, &invalid) ||
		errors.As(err, &value) ||
		errors.As(err, &authorization)
}

// permanentStatus reads the response the API sent when the error carries no
// modelled type and the SDK had no opinion on it: the client side of the status
// range is a refusal of what was sent, except for the throttling the broker asks
// to have repeated.
func permanentStatus(err error) bool {
	var response *awshttp.ResponseError
	if !errors.As(err, &response) {
		return false
	}
	status := response.HTTPStatusCode()
	if status == http.StatusTooManyRequests {
		return false
	}
	return status >= http.StatusBadRequest && status < http.StatusInternalServerError
}
