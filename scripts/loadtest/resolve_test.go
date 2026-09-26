package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// An arrival that failed before the service decided it is settled by its key
// after the window, as the first conclusion of the operation.
func TestResolveAll_settlesAnArrivalTheServiceNeverSaw(t *testing.T) {
	t.Parallel()
	fake, server := newFakeService(t)
	fake.wager = func(w http.ResponseWriter, _ *http.Request, arrival int) bool {
		if arrival%5 != 0 {
			return false
		}
		http.Error(w, "no replica", http.StatusServiceUnavailable)
		return true
	}
	o := optionsFor(t, server)
	if err := run(context.Background(), o, &strings.Builder{}); err != nil {
		t.Fatalf("run with one arrival in five refused by the balancer = %v, want every one resolved and the verdict to pass", err)
	}
}

// An arrival the service committed and then failed to answer is settled by the
// replay of its key, and counted once.
func TestResolveAll_settlesAnArrivalCommittedWithoutAnAnswer(t *testing.T) {
	t.Parallel()
	fake, server := newFakeService(t)
	fake.wager = func(w http.ResponseWriter, r *http.Request, arrival int) bool {
		if arrival%4 != 0 {
			return false
		}
		fake.commitSilently(r)
		http.Error(w, "replica killed", http.StatusBadGateway)
		return true
	}
	o := optionsFor(t, server)
	o.clock = ticking(longAgo, time.Millisecond)
	l := newLoad(o)
	if err := l.open(context.Background()); err != nil {
		t.Fatalf("open = %v, want nil", err)
	}
	l.send(context.Background(), longAgo.Add(time.Second))
	if l.errors == 0 {
		t.Fatalf("errors of the window = %d, want the arrivals answered 502", l.errors)
	}
	if unresolved := l.resolveAll(context.Background()); len(unresolved) != 0 {
		t.Fatalf("unresolved = %v, want every committed arrival replayed", unresolved)
	}
	if findings := l.check(context.Background()); len(findings) != 0 {
		t.Fatalf("findings after the replays = %v, want each operation counted once", findings)
	}
}

// An arrival that gets no decided answer within the bound fails the run,
// naming its key.
func TestResolveAll_namesTheKeyThatNeverSettled(t *testing.T) {
	t.Parallel()
	fake, server := newFakeService(t)
	fake.wager = func(w http.ResponseWriter, _ *http.Request, _ int) bool {
		http.Error(w, "no replica", http.StatusServiceUnavailable)
		return true
	}
	o := optionsFor(t, server)
	o.resolveWait = 50 * time.Millisecond
	o.duration = 20 * time.Millisecond
	o.concurrency = 1
	err := run(context.Background(), o, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "has no decided answer after 50ms of resolution") || !strings.Contains(err.Error(), "key key-") {
		t.Fatalf("run whose arrivals never settle = %v, want the key named", err)
	}
}

// An unexpected answer during the resolution ends the attempts and is counted
// as one: the key is not sent again forever.
func TestResolveOne_stopsAtAnUnexpectedAnswer(t *testing.T) {
	t.Parallel()
	fake, server := newFakeService(t)
	fake.wager = func(w http.ResponseWriter, _ *http.Request, _ int) bool {
		writeJSON(w, http.StatusForbidden, map[string]string{"title": "no permission"})
		return true
	}
	l := newLoad(optionsFor(t, server))
	op := &operation{key: "key-x", body: []byte(`{}`)}
	if settled := l.resolveOne(context.Background(), op, farAhead); !settled || l.unexpected != 1 || fake.arrivals != 1 {
		t.Fatalf("resolution of a refused key = settled %t, %d unexpected after %d arrivals, want it ended after one", settled, l.unexpected, fake.arrivals)
	}
}
