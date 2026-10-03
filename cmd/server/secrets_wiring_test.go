package main

// The reveal route, on the router the product actually serves.
//
// # Why this file exists
//
// S6 wrote `internal/httpapi/secrets` and its own tests, and those tests mount the
// handler on a mux of their own. That is the right way to test a handler and it is
// **not** the same claim: a package can be entirely correct and never be reachable,
// and the failure that produces is a 404 on a GM's reveal button with no error
// anywhere. This file is the assertion that the route is on the router the binary
// builds — the wiring S6 explicitly left to the integrator.
//
// # The shape, copied from the play route's wiring test
//
// One status distinguishes every failure shape:
//
//   - **404** would mean the route is not on the router — the bug this file exists
//     to catch. The campaign subtree is mounted and gated, nothing inside it
//     matched, so the subtree's not-found handler answered.
//   - **401** is the success case for an anonymous reader: no identity, so the edit
//     gate refuses before any handler runs.
//   - **204** would mean the route answered *without* a gate, which is far worse —
//     a disclosure anybody who can reach the port can make.
//   - **403** would mean the inner `RequireEdit` did not run under the outer
//     `RequireRead`.
//
// Asserting the route *exists* by looking for a pattern would assert nothing, and
// opening a socket would pass with the gate applied by hand somewhere other than the
// mount. So the assertion is on the status the product produces.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
)

const (
	// secretPagePath is the page the reveal route is aimed at. It has to exist, or
	// the request would fail on the path rather than on the gate — and a test that
	// cannot tell those two apart is not testing the gate.
	secretPagePath = "/c/" + fixtureSlug + "/secrets/lore/vault.md"

	// vaultWithSecret is a page carrying one collapsed `[!secret]` callout with a
	// block id, so the anchor a request names is one the resolver can find rather
	// than one the route has to guess.
	vaultWithSecret = "---\ntitle: The Vault\n---\n\n" +
		"The vault door is iron.\n\n" +
		"> [!secret]- The combination is hunter2.  ^vault\n" +
		"> It is written on the back of the wyvern's scale.\n"

	// revealBody is what a GM's reveal sends. The anchor is the block id above.
	revealBody = `{"anchor":"vault","revealed":true}`
)

// TestTheSecretRouteIsMountedOnTheRouterTheProductServes is the smallest form of the
// claim, and the one that catches "correct package, never reachable".
func TestTheSecretRouteIsMountedOnTheRouterTheProductServes(t *testing.T) {
	// **No `t.Parallel()`, and that is ADR 0004 rather than an oversight.** A
	// `*store.Store` is a process-wide single-instance slot, so two parallel
	// instances collide on it — and the collision surfaces as a uniqueness error on
	// `campaigns.slug`, which reads like a fixture bug and is really the
	// single-instance rule doing its job. The play route's wiring test is written
	// the same way for the same reason.
	// `newInstance` already registers `fixtureSlug` and keeps it as `inst.campaign`;
	// registering it again here would collide on `campaigns.slug`, which reads like
	// a fixture bug and is really this file's first mistake.
	inst := newInstance(t)
	inst.writePage(inst.campaign, "lore/vault.md", vaultWithSecret)

	handler := inst.serve(inst.registered)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, inst.put(secretPagePath, nil, revealBody))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("PUT %s as an anonymous reader = %d, want 401. 404 means the "+
			"route is not mounted on the router the binary serves; 204 means it is "+
			"mounted without RequireEdit; 403 means the inner gate did not run "+
			"under the outer one", secretPagePath, recorder.Code)
	}
}

// TestTheSecretRouteRefusesAPlayerOnTheRouterTheProductServes is the second row, and
// the one that distinguishes the edit gate from the read gate.
//
// A `player` **is** a member, so `RequireRead` lets them through and only
// `RequireEdit` stops them. So the refusal here is the claim: if this row ever
// answers 204 the endpoint is not GM-only, and a player who can disclose a secret
// to the party through it is the disclosure this phase exists to prevent.
//
// The assertion is deliberately **not** a specific status. ADR 0024 settles the
// matrix and S-6's own tests hold it against the handler; this file's claim is
// narrower and is the one only the composition root can make — that the route is
// behind the edit gate at all, and that a player does not get a success.
func TestTheSecretRouteRefusesAPlayerOnTheRouterTheProductServes(t *testing.T) {
	// **No `t.Parallel()`, and that is ADR 0004 rather than an oversight.** A
	// `*store.Store` is a process-wide single-instance slot, so two parallel
	// instances collide on it — and the collision surfaces as a uniqueness error on
	// `campaigns.slug`, which reads like a fixture bug and is really the
	// single-instance rule doing its job. The play route's wiring test is written
	// the same way for the same reason.
	// `newInstance` already registers `fixtureSlug` and keeps it as `inst.campaign`;
	// registering it again here would collide on `campaigns.slug`, which reads like
	// a fixture bug and is really this file's first mistake.
	inst := newInstance(t)
	inst.writePage(inst.campaign, "lore/vault.md", vaultWithSecret)

	player := inst.createUser("brian", "correct horse battery")
	inst.addMember(inst.campaign.ID, player.ID, domain.RolePlayer)

	handler := inst.serve(inst.registered)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, inst.put(
		secretPagePath, inst.sessionFor(player.ID), revealBody))

	if recorder.Code == http.StatusNoContent {
		t.Fatalf("PUT %s as a player = 204. The reveal endpoint is GM-only "+
			"(S-5.12), and a player who can disclose a secret to the party through "+
			"it is the disclosure this phase exists to prevent", secretPagePath)
	}
}

// TestTheSecretRouteChangedNothingOnThePage is the assertion that the *refusals*
// above were refusals and not near-misses.
//
// A gate that answered correctly and still wrote the file would pass every status
// assertion in this file, and the page it wrote to would be a GM's vault. So this
// reads the file back through the content root after a player's refused reveal and
// requires the marker byte to be exactly where it was.
func TestTheSecretRouteChangedNothingOnThePage(t *testing.T) {
	// **No `t.Parallel()`, and that is ADR 0004 rather than an oversight.** A
	// `*store.Store` is a process-wide single-instance slot, so two parallel
	// instances collide on it — and the collision surfaces as a uniqueness error on
	// `campaigns.slug`, which reads like a fixture bug and is really the
	// single-instance rule doing its job. The play route's wiring test is written
	// the same way for the same reason.
	// `newInstance` already registers `fixtureSlug` and keeps it as `inst.campaign`;
	// registering it again here would collide on `campaigns.slug`, which reads like
	// a fixture bug and is really this file's first mistake.
	inst := newInstance(t)
	inst.writePage(inst.campaign, "lore/vault.md", vaultWithSecret)

	player := inst.createUser("brian", "correct horse battery")
	inst.addMember(inst.campaign.ID, player.ID, domain.RolePlayer)

	handler := inst.serve(inst.registered)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, inst.put(
		secretPagePath, inst.sessionFor(player.ID), revealBody))

	if got := inst.readPage(inst.campaign, "lore/vault.md"); got != vaultWithSecret {
		t.Fatalf("the page changed after a refused reveal:\n got %q\nwant %q",
			got, vaultWithSecret)
	}

	// And the secret is still secret: the byte is still `-`.
	if !strings.Contains(inst.readPage(inst.campaign, "lore/vault.md"), "[!secret]-") {
		t.Error("the marker is no longer `-` after a refused reveal")
	}
}

// readPage reads a page back off disk, so a test can assert the file itself rather
// than what a route said about it.
//
// **Off disk rather than through the index or a route**, because the claim is about
// the author's file: the reveal is a one-byte edit to that file and nothing else
// records it. Reading through a route would assert that some layer agrees with
// itself.
func (i *instance) readPage(campaign domain.Campaign, rel string) string {
	i.t.Helper()

	body, err := os.ReadFile(filepath.Join(
		campaign.ContentRoot, filepath.FromSlash(rel)))
	if err != nil {
		i.t.Fatalf("read %s: %v", rel, err)
	}

	return string(body)
}

// put builds a `PUT` carrying an optional cookie and a JSON body.
//
// **No `If-Match`, deliberately.** This file is about whether the route is mounted
// and gated; a missing precondition answers 428, which is a fourth status and would
// make every row here ambiguous about which gate produced the refusal. The
// precondition is S6's own test's subject and is not this file's.
func (i *instance) put(path string, cookie *http.Cookie, body string) *http.Request {
	i.t.Helper()

	request := httptest.NewRequestWithContext(
		i.t.Context(), http.MethodPut, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	if cookie != nil {
		request.AddCookie(cookie)
	}

	return request
}
