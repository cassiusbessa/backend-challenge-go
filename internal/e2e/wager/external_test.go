//go:build integration

// The journey of the read a provider makes by the identifier it chose, against a
// real PostgreSQL and a real IdP: the same representation as the read by
// identity, and the same isolation, now with a provider in the URL that
// authorizes nothing.
//
// Every case opens its own wallet, so two runs of the suite never collide on the
// unique index of one wallet per player and currency.
package wager

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/platform/problem"
	"github.com/junglegaming/backend-challenge-go/internal/suiteenv"
)

func TestReadByExternal_answersWhatTheReadByIdentityAnswers(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	external := "external-" + suiteenv.NewID()
	placed := submitBody(ctx, t, at, wallet.bet("25.00", named(external, "round-"+suiteenv.NewID())))
	if placed.status != http.StatusCreated {
		t.Fatalf("the bet to find by its external identifier = %d, want 201: %s", placed.status, placed.body)
	}
	found := readExternal(ctx, t, at, at.provider, providerClient, external)
	if found.status != http.StatusOK {
		t.Fatalf("read by the external identifier = %d, want 200: %s", found.status, found.body)
	}
	byIdentity := read(ctx, t, at, at.provider, placed.transaction(t).TransactionID)
	if got, want := found.transaction(t), byIdentity.transaction(t); got != want {
		t.Fatalf("read by the external identifier = %+v, want the read by identity %+v", got, want)
	}
	if got := found.transaction(t).Balance.Amount; got != "975.00" {
		t.Fatalf("balance read by the external identifier = %s, want the 975.00 the bet left", got)
	}
}

// Another provider in the URL answers what an identifier nobody sent answers:
// the same status and the same body, except the instance, which is the path
// that was asked.
func TestReadByExternal_answersAnotherProviderInTheURLAsAbsence(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	external := "external-" + suiteenv.NewID()
	placed := submitBody(ctx, t, at, wallet.bet("25.00", named(external, "round-"+suiteenv.NewID())))
	if placed.status != http.StatusCreated {
		t.Fatalf("the bet another provider asks about = %d, want 201: %s", placed.status, placed.body)
	}
	alien := readExternal(ctx, t, at, at.other, providerClient, external)
	absent := readExternal(ctx, t, at, at.other, otherClient, "external-"+suiteenv.NewID())
	if alien.status != http.StatusNotFound || absent.status != http.StatusNotFound {
		t.Fatalf("another provider = %d and nobody sent = %d, want both 404: %s", alien.status, absent.status, alien.body)
	}
	assertRevealsNothing(t, alien)
	if got, want := withoutInstance(t, alien), withoutInstance(t, absent); got != want {
		t.Fatalf("another provider = %+v, want the absence %+v", got, want)
	}
}

func TestReadByExternal_answers404ForAnIdentifierNobodySent(t *testing.T) {
	ctx, at := start(t)
	absent := readExternal(ctx, t, at, at.provider, providerClient, "external-"+suiteenv.NewID())
	if absent.status != http.StatusNotFound {
		t.Fatalf("read of an identifier nobody sent = %d, want 404: %s", absent.status, absent.body)
	}
	if got := absent.header.Get("Content-Type"); got != problem.MediaType {
		t.Fatalf("content type of the absence = %s, want %s", got, problem.MediaType)
	}
}

// The read finds a wait as it stands: 200 with that status and no balance, and
// the schedule of the row where it was. The worker is kept off, so the only
// thing that could have moved the schedule is the read.
func TestReadByExternal_findsAWaitWithoutTouchingIt(t *testing.T) {
	ctx, at := startWith(t, map[string]string{"REFERENCE_INTERVAL": "1h"})
	wallet := openWallet(ctx, t, at)
	external := "external-" + suiteenv.NewID()
	round := "round-" + suiteenv.NewID()
	changes := citing("external-"+suiteenv.NewID(), round)
	changes["externalTransactionId"] = external
	accepted := submitBody(ctx, t, at, wallet.win("50.00", changes))
	if accepted.status != http.StatusAccepted {
		t.Fatalf("the win that waits = %d, want 202: %s", accepted.status, accepted.body)
	}
	before := schedule(ctx, t, accepted.transaction(t).TransactionID)
	found := readExternal(ctx, t, at, at.provider, providerClient, external)
	if found.status != http.StatusOK {
		t.Fatalf("read of the wait by its external identifier = %d, want 200: %s", found.status, found.body)
	}
	waiting := found.transaction(t)
	if waiting.Status != "PENDING_REFERENCE" || waiting.Balance.Amount != "" {
		t.Fatalf("wait read = %s with balance %q, want PENDING_REFERENCE with none", waiting.Status, waiting.Balance.Amount)
	}
	if after := schedule(ctx, t, waiting.TransactionID); after != before {
		t.Fatalf("schedule after the read = %v, want the %v it had", after, before)
	}
}

// The internal client does not read a wager, by either read, and the answer
// carries nothing of the row.
func TestReadByExternal_refusesTheInternalClientWithNoState(t *testing.T) {
	ctx, at := start(t)
	wallet := openWallet(ctx, t, at)
	external := "external-" + suiteenv.NewID()
	placed := submitBody(ctx, t, at, wallet.bet("25.00", named(external, "round-"+suiteenv.NewID())))
	if placed.status != http.StatusCreated {
		t.Fatalf("the bet the internal client asks about = %d, want 201: %s", placed.status, placed.body)
	}
	refused := readExternal(ctx, t, at, at.internal, providerClient, external)
	if refused.status != http.StatusForbidden {
		t.Fatalf("internal client on the read by external identifier = %d, want 403: %s", refused.status, refused.body)
	}
	assertRevealsNothing(t, refused)
}

func TestReadByExternal_refusesARequestWithNoCredential(t *testing.T) {
	ctx, at := start(t)
	refused := readExternal(ctx, t, at, "", providerClient, "external-"+suiteenv.NewID())
	if refused.status != http.StatusUnauthorized {
		t.Fatalf("read by external identifier with no token = %d, want 401: %s", refused.status, refused.body)
	}
	if bytes.Contains(refused.body, []byte("transactionId")) {
		t.Fatalf("body with no token = %s, want no transaction in it", refused.body)
	}
}

func readExternal(ctx context.Context, t *testing.T, at suite, bearer, provider, external string) answer {
	t.Helper()
	path := "/providers/" + url.PathEscape(provider) + wagerRoute + "/" + url.PathEscape(external)
	return call(ctx, t, request{method: http.MethodGet, url: at.base + path, bearer: bearer})
}

// withoutInstance is the refusal with its instance cleared: the instance is the
// path that was asked, so two absences asked at two paths differ there and only
// there.
func withoutInstance(t *testing.T, refused answer) problem.Details {
	t.Helper()
	details := refused.refusal(t)
	details.Instance = ""
	return details
}
