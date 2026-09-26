package scenarios

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
)

// The two operations the proxy recognizes, as the SDK of this module writes them
// on the wire: the queue speaks the JSON protocol and names the operation in a
// header, and the topic speaks the query protocol and names it in the form.
const (
	targetHeader  = "X-Amz-Target"
	deleteTarget  = "AmazonSQS.DeleteMessage"
	publishAction = "Publish"
)

// invocationHeader names one call of the SDK. Every attempt of that call carries
// the same value, the retries of transport included.
const invocationHeader = "Amz-Sdk-Invocation-Id"

// refusal is the answer to a removal the proxy refuses, shaped as an error of the
// JSON protocol so the SDK reads it as a refusal of the broker. It is a client
// error, which the SDK does not retry: the removal the consumer attempts is the
// removal refused, and nothing else reaches the broker in its place.
const refusal = `{"__type":"com.amazonaws.sqs#RequestRefused","message":"the proxy of the scenario refused the removal"}`

// Faculties is what a proxy does besides forwarding. The zero value forwards
// every request untouched.
type Faculties struct {
	// RefuseDeletes answers every removal of a message with a client error and
	// never forwards it. The commit that preceded it stands, and the message
	// stays on the queue.
	RefuseDeletes bool

	// RecordPublishes notes the deduplication of every publication, which is the
	// eventId, before forwarding it, once per call of the SDK. That counts what
	// left the process before the FIFO topic deduplicates it, which would make a
	// second send reach a subscriber once.
	RecordPublishes bool
}

// Proxy stands between one instance and the broker, on the network, where no
// code of the process can tell it from the broker itself. The zero value is not
// a proxy: NewProxy is the only constructor.
type Proxy struct {
	forward   *httputil.ReverseProxy
	faculties Faculties

	mu          sync.Mutex
	refused     int
	published   map[string]int
	invocations map[string]struct{}
}

func NewProxy(broker *url.URL, faculties Faculties) *Proxy {
	return &Proxy{
		forward: &httputil.ReverseProxy{Rewrite: func(out *httputil.ProxyRequest) {
			out.SetURL(broker)
			// The host the SDK signed the request for is kept, so what reaches the
			// broker is the request the process sent and not one rewritten by the
			// test.
			out.Out.Host = out.In.Host
		}},
		faculties:   faculties,
		published:   map[string]int{},
		invocations: map[string]struct{}{},
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.faculties.RefuseDeletes && r.Header.Get(targetHeader) == deleteTarget {
		p.refuse(w)
		return
	}
	if p.faculties.RecordPublishes {
		if err := p.record(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	p.forward.ServeHTTP(w, r)
}

// Refused answers how many removals the proxy refused.
func (p *Proxy) Refused() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refused
}

// Published answers how many calls of the SDK sent each eventId out through the
// proxy. The map is a copy, so a case reads it while the instances keep
// publishing.
func (p *Proxy) Published() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.published)
}

func (p *Proxy) refuse(w http.ResponseWriter) {
	p.mu.Lock()
	p.refused++
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(w, refusal)
}

// record notes the deduplication of a publication and puts the body back as it
// came, so the broker receives the very bytes the process signed. A body that is
// not a form is no publication of the topic, and it is forwarded as it is.
func (p *Proxy) record(r *http.Request) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("proxy: read the request body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	form, malformed := url.ParseQuery(string(body))
	if malformed == nil && form.Get("Action") == publishAction {
		p.note(form.Get("MessageDeduplicationId"), r.Header.Get(invocationHeader))
	}
	return nil
}

// note counts one publication of the event, once per call of the SDK. A retry
// repeats the invocation of the attempt before it: that is the transport sending
// one publication again, not a relay publishing the event twice. A request with
// no invocation is counted every time it comes.
func (p *Proxy) note(eventID, invocation string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, repeated := p.invocations[invocation]; repeated {
		return
	}
	if invocation != "" {
		p.invocations[invocation] = struct{}{}
	}
	p.published[eventID]++
}
