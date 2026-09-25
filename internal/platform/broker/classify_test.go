package broker

import (
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/smithy-go"
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
		// The three the status rule alone would read as a refusal: SNS answers
		// each with a 400, and none of them is about the bytes that were sent.
		"throttling the SDK names by code": carried(http.StatusBadRequest, &smithy.GenericAPIError{Code: "ThrottledException"}),
		"throttling of the key":            carried(http.StatusBadRequest, &types.KMSThrottlingException{}),
		"a request that timed out":         carried(http.StatusBadRequest, &smithy.GenericAPIError{Code: "RequestTimeout"}),
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

// The upper bound of the status rule. The five statuses the SDK already calls
// repeatable never reach this far, but the rest of the server side does, and
// reading one of those as a refusal of the bytes would give up on a row over a
// broker having a bad minute.
func TestPermanentStatus_stopsAtTheServerSideOfTheRange(t *testing.T) {
	t.Parallel()
	cases := map[int]bool{
		http.StatusBadRequest:          true,
		http.StatusUnprocessableEntity: true,
		http.StatusInternalServerError: false,
		http.StatusNotImplemented:      false,
	}
	for status, want := range cases {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			if got := permanentStatus(responseError(status)); got != want {
				t.Fatalf("permanentStatus of %d = %t, want %t", status, got, want)
			}
		})
	}
}

// responseError is the shape the SDK hands over when the API answered a status
// and no modelled error came with it.
func responseError(status int) error {
	return carried(status, errors.New("the broker answered"))
}

// carried is that same shape with the refusal the API sent inside it, which is
// how every coded and modelled error arrives: the status and the code reach the
// classification together, and a case that handed over only one of the two
// would pass over a rule that reads the other.
func carried(status int, err error) error {
	return &awshttp.ResponseError{
		Response: &awshttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      err,
	}
}
