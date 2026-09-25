package broker

import (
	"errors"
	"net/http"

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
	return permanentType(err) || permanentStatus(err)
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
// modelled type: the client side of the status range is a refusal of what was
// sent, except for the throttling the broker asks to have repeated.
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
