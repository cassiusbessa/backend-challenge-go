package broker

import (
	"errors"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sns/types"
	awshttp "github.com/aws/smithy-go/transport/http"
)

func TestPermanent_readsTheRefusalOfTheBrokerAsPermanent(t *testing.T) {
	t.Parallel()
	cases := map[string]error{
		"a topic that is not there":       &types.NotFoundException{},
		"a parameter the API refuses":     &types.InvalidParameterException{},
		"a caller not allowed to publish": &types.AuthorizationErrorException{},
		"a status the client owns":        responseError(http.StatusBadRequest),
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if got := (&Topic{}).Permanent(chained(err)); !got {
				t.Fatalf("Permanent of %s = %t, want true", name, got)
			}
		})
	}
}

func TestPermanent_readsWhatMayCompleteLaterAsWorthRepeating(t *testing.T) {
	t.Parallel()
	cases := map[string]error{
		"the broker being out":                responseError(http.StatusServiceUnavailable),
		"the first status of the server side": responseError(http.StatusInternalServerError),
		"a throttled request":                 responseError(http.StatusTooManyRequests),
		"an error of no known shape":          errors.New("connection reset by peer"),
		"no error at all":                     nil,
	}
	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if got := (&Topic{}).Permanent(chained(err)); got {
				t.Fatalf("Permanent of %s = %t, want false", name, got)
			}
		})
	}
}

// chained wraps the failure the way the publisher hands it over, so the
// classification is read off the chain and not off a bare value.
func chained(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(err)
}

// responseError is the shape the SDK hands over when the API answered a status
// and no modelled error came with it.
func responseError(status int) error {
	return &awshttp.ResponseError{
		Response: &awshttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      errors.New("the broker answered"),
	}
}
