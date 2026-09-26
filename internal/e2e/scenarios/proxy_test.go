package scenarios

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go"
)

// publishAnswer is what the topic answers a publication, in the query protocol,
// so the SDK on the other side of the proxy reads a send that went through.
const publishAnswer = `<PublishResponse xmlns="http://sns.amazonaws.com/doc/2010-03-31/">` +
	`<PublishResult><MessageId>message-1</MessageId></PublishResult>` +
	`<ResponseMetadata><RequestId>request-1</RequestId></ResponseMetadata></PublishResponse>`

// arrival is one request as the stand-in of the broker received it.
type arrival struct {
	target string
	body   []byte
}

// standIn takes the place of the broker behind the proxy and keeps every request
// that reached it, so a case tells what was forwarded from what was not. It
// answers the first failing requests with a failure of the broker the SDK
// retries, and every one after them as the broker would.
type standIn struct {
	mu       sync.Mutex
	arrivals []arrival
	failing  int
}

func (s *standIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.arrivals = append(s.arrivals, arrival{target: r.Header.Get(targetHeader), body: body})
	failed := len(s.arrivals) <= s.failing
	s.mu.Unlock()
	if failed {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get(targetHeader) != "" {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		_, _ = io.WriteString(w, "{}")
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = io.WriteString(w, publishAnswer)
}

func (s *standIn) received() []arrival {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]arrival(nil), s.arrivals...)
}

// proxied puts a proxy with those faculties in front of a stand-in of the
// broker, and answers the proxy, the address the SDK reaches it at, and the
// stand-in.
func proxied(t *testing.T, faculties Faculties) (*Proxy, string, *standIn) {
	t.Helper()
	return proxiedFailing(t, faculties, 0)
}

// proxiedFailing is proxied with a stand-in that fails the first requests it
// receives.
func proxiedFailing(t *testing.T, faculties Faculties, failing int) (*Proxy, string, *standIn) {
	t.Helper()
	broker := &standIn{failing: failing}
	behind := httptest.NewServer(broker)
	t.Cleanup(behind.Close)
	target, err := url.Parse(behind.URL)
	if err != nil {
		t.Fatalf("url.Parse of the stand-in = %v, want nil", err)
	}
	proxy := NewProxy(target, faculties)
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)
	return proxy, front.URL, broker
}

var local = credentials.NewStaticCredentialsProvider("test", "test", "")

func queueAt(endpoint string) *sqs.Client {
	return sqs.New(sqs.Options{Region: "us-east-1", Credentials: local, BaseEndpoint: aws.String(endpoint)})
}

// topicAt is a client of the topic with the retryer of the SDK, the one the relay
// publishes with, less its waits between attempts.
func topicAt(endpoint string) *sns.Client {
	return sns.New(sns.Options{Region: "us-east-1", Credentials: local, BaseEndpoint: aws.String(endpoint), Retryer: promptly})
}

var promptly = retry.NewStandard(func(o *retry.StandardOptions) {
	o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
})

func deleteThrough(ctx context.Context, endpoint string) error {
	_, err := queueAt(endpoint).DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String("http://localhost:4566/000000000000/scenario.fifo"),
		ReceiptHandle: aws.String("receipt-1"),
	})
	return err
}

func publishThrough(ctx context.Context, endpoint, eventID string) error {
	_, err := topicAt(endpoint).Publish(ctx, &sns.PublishInput{
		TopicArn:               aws.String("arn:aws:sns:us-east-1:000000000000:wallet-events.fifo"),
		Message:                aws.String(`{"eventId":"` + eventID + `"}`),
		MessageGroupId:         aws.String("wallet-1"),
		MessageDeduplicationId: aws.String(eventID),
	})
	return err
}

// The removal the SDK sends is answered by the proxy and never reaches the
// broker, and the SDK reads the answer as a refusal it does not retry: one
// attempt, one refusal counted.
func TestProxy_refusesTheRemovalTheSDKSendsWithoutForwardingIt(t *testing.T) {
	t.Parallel()
	proxy, endpoint, broker := proxied(t, Faculties{RefuseDeletes: true})
	err := deleteThrough(context.Background(), endpoint)
	var refused smithy.APIError
	if !errors.As(err, &refused) || refused.ErrorCode() != "RequestRefused" {
		t.Fatalf("DeleteMessage through the proxy = %v, want the RequestRefused of the broker", err)
	}
	if got := len(broker.received()); got != 0 {
		t.Errorf("requests that reached the broker = %d, want 0", got)
	}
	if got := proxy.Refused(); got != 1 {
		t.Errorf("Refused = %d, want the 1 attempt the SDK made", got)
	}
}

// Refusing the removal refuses nothing else: the fetch of the consumer goes
// through, and is not counted.
func TestProxy_forwardsAnyOtherOperationOfTheQueue(t *testing.T) {
	t.Parallel()
	proxy, endpoint, broker := proxied(t, Faculties{RefuseDeletes: true})
	_, err := queueAt(endpoint).ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
		QueueUrl: aws.String("http://localhost:4566/000000000000/scenario.fifo"),
	})
	if err != nil {
		t.Fatalf("ReceiveMessage through the proxy = %v, want nil", err)
	}
	received := broker.received()
	if len(received) != 1 || received[0].target != "AmazonSQS.ReceiveMessage" {
		t.Fatalf("requests that reached the broker = %+v, want the one ReceiveMessage", received)
	}
	if got := proxy.Refused(); got != 0 {
		t.Errorf("Refused after a fetch = %d, want 0", got)
	}
}

// The publication the SDK sends is recorded under its deduplication, which is
// the eventId, and still reaches the broker.
func TestProxy_recordsThePublicationTheSDKSendsByItsEventID(t *testing.T) {
	t.Parallel()
	proxy, endpoint, broker := proxied(t, Faculties{RecordPublishes: true})
	if err := publishThrough(context.Background(), endpoint, "event-1"); err != nil {
		t.Fatalf("Publish through the proxy = %v, want nil", err)
	}
	if got := proxy.Published(); len(got) != 1 || got["event-1"] != 1 {
		t.Errorf("Published = %v, want event-1 once", got)
	}
	if received := broker.received(); len(received) != 1 || !bytes.Contains(received[0].body, []byte("event-1")) {
		t.Errorf("requests that reached the broker = %d, want the one publication carrying event-1", len(received))
	}
}

// A publication the SDK sends again after a failure of the broker is one call:
// every attempt reaches the broker, and the event is counted once, because the
// retry is the transport and not a relay publishing it twice.
func TestProxy_countsOnceAPublicationTheSDKRetried(t *testing.T) {
	t.Parallel()
	proxy, endpoint, broker := proxiedFailing(t, Faculties{RecordPublishes: true}, 1)
	if err := publishThrough(context.Background(), endpoint, "event-5"); err != nil {
		t.Fatalf("Publish retried through the proxy = %v, want nil", err)
	}
	if got := len(broker.received()); got != 2 {
		t.Fatalf("attempts that reached the broker = %d, want the failed one and its retry", got)
	}
	if got := proxy.Published(); len(got) != 1 || got["event-5"] != 1 {
		t.Errorf("Published after a retry = %v, want event-5 once", got)
	}
}

// Two calls that publish the same event are two sends, which is the defect the
// scenario of the publishers looks for, and both are counted.
func TestProxy_countsTwiceAnEventTwoCallsPublish(t *testing.T) {
	t.Parallel()
	proxy, endpoint, _ := proxied(t, Faculties{RecordPublishes: true})
	for range 2 {
		if err := publishThrough(context.Background(), endpoint, "event-6"); err != nil {
			t.Fatalf("Publish of event-6 through the proxy = %v, want nil", err)
		}
	}
	if got := proxy.Published()["event-6"]; got != 2 {
		t.Errorf("Published of event-6 after two calls = %d, want 2", got)
	}
}

// Reading the body to record it does not change it: the broker receives the
// bytes the process sent, byte for byte.
func TestProxy_forwardsTheBodyOfAPublicationIntact(t *testing.T) {
	t.Parallel()
	proxy, _, broker := proxied(t, Faculties{RecordPublishes: true})
	body := "Action=Publish&MessageDeduplicationId=event-2&Message=%7B%22a%22%3A1%7D&MessageGroupId=wallet-2"
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	answered := httptest.NewRecorder()
	proxy.ServeHTTP(answered, req)
	if answered.Code != http.StatusOK {
		t.Fatalf("status of the forwarded publication = %d, want 200", answered.Code)
	}
	received := broker.received()
	if len(received) != 1 || string(received[0].body) != body {
		t.Fatalf("body that reached the broker = %+v, want exactly %q", received, body)
	}
	if got := proxy.Published()["event-2"]; got != 1 {
		t.Errorf("Published of event-2 = %d, want 1", got)
	}
}

// With both faculties off the proxy is a wire: the removal and the publication
// both reach the broker, and nothing is counted.
func TestProxy_forwardsEverythingUntouchedWithTheFacultiesOff(t *testing.T) {
	t.Parallel()
	proxy, endpoint, broker := proxied(t, Faculties{})
	if err := deleteThrough(context.Background(), endpoint); err != nil {
		t.Fatalf("DeleteMessage through a plain proxy = %v, want nil", err)
	}
	if err := publishThrough(context.Background(), endpoint, "event-3"); err != nil {
		t.Fatalf("Publish through a plain proxy = %v, want nil", err)
	}
	if got := len(broker.received()); got != 2 {
		t.Errorf("requests that reached the broker = %d, want the removal and the publication", got)
	}
	if proxy.Refused() != 0 || len(proxy.Published()) != 0 {
		t.Errorf("Refused = %d and Published = %v, want nothing counted", proxy.Refused(), proxy.Published())
	}
}

// A body the proxy cannot read is not guessed at: the instance is answered with
// a failure of the gateway, and nothing reaches the broker half read.
func TestProxy_answersABadGatewayForABodyItCannotRead(t *testing.T) {
	t.Parallel()
	proxy, _, broker := proxied(t, Faculties{RecordPublishes: true})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", iotest.ErrReader(errors.New("connection reset")))
	answered := httptest.NewRecorder()
	proxy.ServeHTTP(answered, req)
	if answered.Code != http.StatusBadGateway {
		t.Errorf("status for an unreadable body = %d, want 502", answered.Code)
	}
	if got := len(broker.received()); got != 0 {
		t.Errorf("requests that reached the broker after an unreadable body = %d, want 0", got)
	}
}

// record notes a publication of the topic and nothing else, and every body it
// reads goes back as it came: a request of the queue and another action of the
// topic pass a recording proxy with nothing noted.
func TestRecord_notesOnlyAPublicationAndPutsEveryBodyBack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		body  string
		noted map[string]int
	}{
		{name: "a publication is noted by its deduplication", body: "Action=Publish&MessageDeduplicationId=event-4", noted: map[string]int{"event-4": 1}},
		{name: "another action of the topic is not", body: "Action=GetTopicAttributes&TopicArn=wallet-events", noted: map[string]int{}},
		{name: "a request of the queue is not", body: `{"QueueUrl":"http://localhost:4566/000000000000/scenario.fifo"}`, noted: map[string]int{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy := NewProxy(&url.URL{Scheme: "http", Host: "broker.invalid"}, Faculties{RecordPublishes: true})
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/", strings.NewReader(tc.body))
			if err := proxy.record(req); err != nil {
				t.Fatalf("record = %v, want nil", err)
			}
			if got := proxy.Published(); !maps.Equal(got, tc.noted) {
				t.Errorf("Published = %v, want %v", got, tc.noted)
			}
			if again, _ := io.ReadAll(req.Body); string(again) != tc.body {
				t.Errorf("body after record = %q, want %q as it came", again, tc.body)
			}
		})
	}
}

// note counts an event once per invocation, and a request that carries none each
// time it comes.
func TestNote_countsEachCallOfTheSDKOnce(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		invocations []string
		want        int
	}{
		{name: "the attempts of one call are counted once", invocations: []string{"call-1", "call-1", "call-1"}, want: 1},
		{name: "two calls are counted twice", invocations: []string{"call-1", "call-2"}, want: 2},
		{name: "requests without an invocation are each counted", invocations: []string{"", ""}, want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy := NewProxy(&url.URL{Scheme: "http", Host: "broker.invalid"}, Faculties{RecordPublishes: true})
			for _, invocation := range tc.invocations {
				proxy.note("event-7", invocation)
			}
			if got := proxy.Published()["event-7"]; got != tc.want {
				t.Errorf("Published of event-7 after %v = %d, want %d", tc.invocations, got, tc.want)
			}
		})
	}
}
