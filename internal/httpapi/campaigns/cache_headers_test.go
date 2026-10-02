package campaigns_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
)

// TestTheGateTellsCachesNotToStoreItsRefusals is the test for a response that was
// safe to build and unsafe to keep.
//
// Every status `writeError` produces is **reader-dependent**: the same URL answers
// 404 for an anonymous requestor and 200 for a member, because S-8 answers "no
// access" with a status that does not say why. Nothing in the body distinguishes
// them — deliberately, so the status cannot become an existence oracle — and that
// is precisely what makes the response unsafe for an intermediary.
//
// A reverse proxy in front of a self-hosted instance is the ordinary deployment,
// not an exotic one. One that stored the anonymous 404 for
// `/c/greyhaven/wiki/index` would serve it to the GM entitled to that page for as
// long as the entry lived, and the symptom would be a GM whose wiki has stopped
// working behind the one thing they put in front of it to make it work.
//
// `Cache-Control: no-store` is what stops the write. Asserted on every status the
// gate can produce, because a partial fix here is worse than none: `no-store` on
// the 404 and not on the 403 leaves one door open.
func TestTheGateTellsCachesNotToStoreItsRefusals(t *testing.T) {
	t.Parallel()

	st := newFakeStore().withCampaign(privateCampaign)

	// The terminal handler, reached only when the gate admits.
	handler := campaigns.RequireRead(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	mux := campaigns.Resolve(st)(handler)

	serve := func(method, slug string) *httptest.ResponseRecorder {
		r := requestFor(t, method, "/c/"+slug+"/wiki/Page", nil)
		r.SetPathValue("slug", slug)

		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, r)

		return recorder
	}

	for _, tc := range []struct {
		name   string
		method string
		slug   string
		status int
	}{
		// Absent campaign: the not-found the S-14.3 oracle test covers.
		{"absent, GET", http.MethodGet, "no-such-campaign", http.StatusNotFound},
		// Invisible campaign: byte-identical to the above by design, and so must
		// carry the same cache directives — a header that differed would be a
		// cheaper existence oracle than the body ever was.
		{"invisible, GET", http.MethodGet, privateCampaign.Slug, http.StatusNotFound},
		// Anonymous on a write. S-14.4 accepts 401 **or** 404 — the gate answers
		// with whatever does not become an existence oracle — and this build
		// answers 404. Which one is asserted rather than assumed, because the
		// cache directives are what is under test and they must be the same
		// either way.
		{"anonymous, PUT", http.MethodPut, privateCampaign.Slug, http.StatusNotFound},
		{"anonymous, POST", http.MethodPost, privateCampaign.Slug, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			recorder := serve(tc.method, tc.slug)

			if recorder.Code != tc.status {
				t.Fatalf("status is %d, want %d: %s",
					recorder.Code, tc.status, recorder.Body.String())
			}

			got := recorder.Header().Get("Cache-Control")
			if !strings.Contains(got, "no-store") {
				t.Errorf("Cache-Control is %q; a reader-dependent refusal that a "+
					"cache may store will be replayed to an entitled member, and a "+
					"reverse proxy in front of this instance is the ordinary "+
					"deployment rather than an exotic one", got)
			}

			// `private` as well: it states the reason to a cache that honours only
			// one of the two directives, and it is what ADR 0016 already requires
			// for a GM's unredacted page.
			if !strings.Contains(got, "private") {
				t.Errorf("Cache-Control is %q; want it to name `private` as well as "+
					"`no-store`, for a cache that honours only one", got)
			}
		})
	}
}

// TestTheGateSaysNoStoreAndTheAdmittedResponseDoesNot is the negative half.
//
// If the admitted 200 were also `no-store`, the assertion above would pass for
// the wrong reason — a gate that stores nothing proves nothing about a gate that
// stores the wrong thing. The admitted response is the one that *may* be cached,
// so this asserts it is not blanket-refused.
func TestTheGateSaysNoStoreAndTheAdmittedResponseDoesNot(t *testing.T) {
	t.Parallel()

	st := newFakeStore().
		withCampaign(privateCampaign).
		withMember(privateCampaign.ID, gmUser, domain.RoleGM)

	handler := campaigns.RequireRead(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	mux := campaigns.Resolve(st)(handler)

	// The identity goes on the request, not into the store alone: `RequireRead`
	// reads the requestor from the context, and a store membership without a
	// requestor is an anonymous request whose campaign happens to have a member.
	r := requestFor(t, http.MethodGet, "/c/"+privateCampaign.Slug+"/wiki/Page", asUser(gmUser))
	r.SetPathValue("slug", privateCampaign.Slug)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, r)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("an entitled member got %d, want 204: the gate refused a request "+
			"it should admit", recorder.Code)
	}

	if got := recorder.Header().Get("Cache-Control"); strings.Contains(got, "no-store") {
		t.Errorf("the admitted response carries Cache-Control %q; the no-store "+
			"assertion above would then pass because this gate stores nothing, "+
			"rather than because it stores the right thing", got)
	}
}
