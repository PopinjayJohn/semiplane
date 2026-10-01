package wiki_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
)

// The marker is a string that appears in exactly one page and nowhere else. Its
// only job is to be greppable, and its job is to fail loudly if it turns up in a
// response a reader was not entitled to.
const privateMarker = "UNIQUEMARKER-9f3a2b7c"

// TestAPrivatePagesBodyNeverAppearsForANonMember is §14.3, the
// cache-partitioning row, asserted as an absence rather than a status.
//
// The risk is real rather than formal: a page body is cached, a campaign's
// visibility is the only thing standing between that body and a reader who is not
// a member, and the failure mode is not a wrong status code. It is a perfectly
// correct 200 carrying the wrong body — a cache that served a private page's
// render because the *entry* was found and the *gate* was not consulted on the
// way out.
//
// So the test asks one question: does the marker appear anywhere, for any
// non-member, on any spelling of the path, in any part of the response? It is
// asserted positively for a member first, so the test cannot pass by serving
// nothing to anybody.
func TestAPrivatePagesBodyNeverAppearsForANonMember(t *testing.T) {
	t.Parallel()

	h := newHarness(t).private()
	h.write("Marked.md", pageBody("# Marked\n\n"+privateMarker+"\n"))

	// The positive, first and explicitly. A member must receive the marker, or
	// every negative below proves nothing.
	member := h.get("/c/greyhaven/wiki/Marked", playerRequestor())
	if member.Code != http.StatusOK {
		t.Fatalf("a member received %d, want 200 — the rest of this test would be vacuous",
			member.Code)
	}

	if !strings.Contains(member.Body.String(), privateMarker) {
		t.Fatalf("a member did not receive the marker: the test would pass vacuously.\n%s",
			truncateBody(member.Body.String()))
	}

	// The negative, over every spelling a reader might try and every role that is
	// not a member. Listing them is the point: a test that checks only the obvious
	// route checks a fraction of the surface.
	paths := []string{
		"/c/greyhaven/wiki/Marked",
		"/c/greyhaven/wiki/Marked.md",
		"/c/greyhaven/wiki/marked",
		"/c/greyhaven/wiki/Marked/",
		"/c/greyhaven/",
		"/c/greyhaven",
	}

	nonMembers := map[string]domain.Requestor{
		"anonymous":    anonymousRequestor(),
		"other-member": {Authenticated: true, UserID: 99, Username: "someone-else"},
	}

	for _, path := range paths {
		for role, requestor := range nonMembers {
			recorder := h.get(path, requestor)

			if strings.Contains(recorder.Body.String(), privateMarker) {
				t.Errorf("a private page's body reached a %s on %s (status %d):\n%s",
					role, path, recorder.Code, truncateBody(recorder.Body.String()))
			}

			// A header is a place the marker can travel too — a reflected path, a
			// validator, a filename — and a body-only assertion misses all three.
			for name, values := range recorder.Header() {
				for _, value := range values {
					if strings.Contains(value, privateMarker) {
						t.Errorf("a private page's body travelled in header %s to a %s on %s: %q",
							name, role, path, value)
					}
				}
			}
		}
	}
}

// TestTheTwoRolesGetTwoCacheEntries is the cache-partitioning property at the
// level it actually operates: one page, two roles, two entries.
//
// The ETag test proves the validators differ. This proves the *entries* are
// separate, which is what makes differing validators load-bearing rather than
// decorative — two roles reading one page must leave two keys behind, or the
// second reader is being served the first one's stored body.
//
// The two bodies are then asserted to be **byte-identical**, and that is the
// stronger claim. S-5.1 says rendered output is permission-neutral by
// construction, and S-5.12 draws the consequence: the page body cannot vary by
// viewer. (The surrounding *shell* does — each reader's own name in the header,
// which is why `Vary: Cookie` is set and why the comparison is on the page
// fragment rather than the whole document.)
func TestTheTwoRolesGetTwoCacheEntries(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.write("Shared.md", pageBody("# Shared\n\nIdentical body for both roles.\n"))

	gm := h.get("/c/greyhaven/wiki/Shared", gmRequestor())
	player := h.get("/c/greyhaven/wiki/Shared", playerRequestor())

	if gm.Code != http.StatusOK || player.Code != http.StatusOK {
		t.Fatalf("both roles must be served: gm=%d player=%d", gm.Code, player.Code)
	}

	if got := h.cache.Len(); got != 2 {
		t.Errorf("the cache holds %d entries after two roles read one page, want 2: "+
			"one entry means the variant is not part of the key, so the second reader "+
			"is served the first one's stored body", got)
	}

	// Byte-identity of the *page*, which is the property S-5.12 protects.
	gmPage := pageFragment(gm.Body.String())
	playerPage := pageFragment(player.Body.String())

	if gmPage != playerPage {
		t.Errorf("the page body differs by viewer with a pass-through redactor, which "+
			"S-5.1 and S-5.12 forbid by construction:\ngm:\n%s\n---\nplayer:\n%s",
			truncateBody(gmPage), truncateBody(playerPage))
	}

	// And the responses must differ where they are required to.
	if gm.Header().Get("ETag") == player.Header().Get("ETag") {
		t.Errorf("both roles received the validator %s — S-5.3 violated, and a cache "+
			"holding both bodies would serve whichever it stored first",
			gm.Header().Get("ETag"))
	}

	if got := gm.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("the GM's Cache-Control is %q, want %q", got, "private, no-store")
	}

	if got := player.Header().Get("Cache-Control"); strings.Contains(got, "no-store") {
		t.Errorf("the player's Cache-Control is %q: a body that is permission-neutral "+
			"within its tier should be cacheable by a private cache (S-5.4)", got)
	}
}

// TestAStaleCacheDoesNotServeAnEditedPage is what a content-hash-keyed cache
// buys, and S-5.2 exists for it.
//
// Validity is the content hash, never the modification time, because a vault
// arrives by sync and a sync path either preserves mtime from the writing device
// or coarsens it. A page rewritten within the same second — which a sync client
// does routinely — collides on mtime, and an mtime-keyed cache would serve the
// first version until a third write moved the timestamp.
func TestAStaleCacheDoesNotServeAnEditedPage(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.write("Changing.md", pageBody("# Changing\n\nThe first version.\n"))

	first := h.get("/c/greyhaven/wiki/Changing", gmRequestor())
	if first.Code != http.StatusOK {
		t.Fatalf("first read = %d, want 200", first.Code)
	}

	// A second read of an unchanged page must hit the cache and agree.
	second := h.get("/c/greyhaven/wiki/Changing", gmRequestor())
	if second.Header().Get("ETag") != first.Header().Get("ETag") {
		t.Error("two reads of an unchanged page produced different validators, so the " +
			"cache is missing or the hash is not a function of the content")
	}

	// Change the content. The write is immediate, so the two versions are
	// microseconds apart — well inside the one-second granularity a timestamp
	// column can represent, which is the collision S-5.2 names.
	h.write("Changing.md", pageBody("# Changing\n\nThe second version.\n"))

	rewritten := h.get("/c/greyhaven/wiki/Changing", gmRequestor())

	if rewritten.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Error("an edited page kept its validator: the cache key is not a function of " +
			"the content (S-5.2)")
	}

	body := rewritten.Body.String()
	if !strings.Contains(body, "second version") {
		t.Errorf("the edited page was not re-rendered:\n%s", truncateBody(body))
	}

	if strings.Contains(body, "first version") {
		t.Error("a stale body was served for an edited page")
	}
}

// truncateBody shortens a response for a failure message.
func truncateBody(body string) string {
	const limit = 300
	if len(body) > limit {
		return body[:limit] + "..."
	}

	return body
}
