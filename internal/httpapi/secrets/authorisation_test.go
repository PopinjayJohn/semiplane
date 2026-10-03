package secrets_test

// The access gates, and the 404 that must not be an oracle.
//
// Three separate claims live here and they are easy to conflate:
//
//   - **The route mounts `RequireEdit`** (ADR 0024, S-6.5, §5.6.4). A `player`'s
//     `PUT` is a 403 and an anonymous one is a 401 or a 404 — S-14.4 — and both answers
//     come from the gate rather than from the handler.
//   - **No access is 404, never 403** (ADR 0024). A private campaign is 404 to everybody
//     who is not a member, and the route's own "no such page" is the **same bytes**.
//   - **The handler never authorises.** It reads the campaign and the account the gate
//     resolved and consults nothing else, so there is no second copy of the S-8 matrix
//     to drift.
//
// The third is the one that is hardest to hold and easiest to lose: a handler that
// re-derived the tier from the slug would be a second implementation of the matrix, and
// the copy nobody reviews is the one that eventually answers 403 where the gate answered
// 404. `TestTheHandlersOwnGmCheckIsNeverTheDecision` asserts the *store's* re-check still
// refuses a demoted GM even when the gate has already admitted him — which is the only
// observable form of "the handler asked the gate rather than deciding".

import (
	"net/http"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
)

// The three strings `assertNoSecretText` looks for, and which are **not** written out
// again in any fixture.
//
// Named and shared because the leak this guards against is exactly a string that appears
// in more than one place: a fixture with its own literal copy of a secret body is a
// secret body the test can no longer find in the response it asserted against, and the
// assertion silently becomes about a string the page never contained.
//
// `firstCalloutTitle` is here rather than only the two bodies because it is the one part
// of a callout that is not the body and that a formatter is most likely to reach for —
// "which callout did that" is a question a title answers, and the answer must never reach
// a browser or a log.
const (
	// firstCalloutBody is the block-id callout's body.
	firstCalloutBody = "Captain Aldric replaced the eastern signal fire."

	// secondCalloutBody is the derived-anchor callout's body.
	secondCalloutBody = "A second secret, and a second captain."

	// firstCalloutTitle is the header line's title.
	firstCalloutTitle = "The traitor"
)

// TestTheRevealRouteIsMountedBehindTheEditGate is ADR 0024 asserted where it can be: a
// `player`'s request is refused and an anonymous one is challenged, *before* the handler
// runs.
//
// The handler contributes nothing to that — it never asks whether the reader may edit —
// so this test is really a test that the chain refuses, and it would fail the day
// somebody mounted the route outside `RequireEdit`. A route mounted behind `RequireRead`
// instead would pass the anonymous case's status check and fail the player's, which is
// why both are here. And the third assertion is the load-bearing one: a 403 for a player
// and a 401 for an anonymous reader are statuses, and a handler that answered them *and
// then wrote the byte* would pass both.
func TestTheRevealRouteIsMountedBehindTheEditGate(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	validator := fixed.validatorFor(pagePath)
	body := revealBody(t, map[string]any{"anchor": "traitor", "revealed": true})

	player := fixed.reveal(pagePath, playerRequestor(), validator, body)
	if player.Code != http.StatusForbidden {
		t.Errorf("a player PUT = %d, want 403 (S-14.4, S-6.5, §5.6.4); body:\n%s",
			player.Code, player.Body)
	}

	anonymous := fixed.reveal(pagePath, anonymousRequestor(), validator, body)
	if anonymous.Code != http.StatusUnauthorized && anonymous.Code != http.StatusNotFound {
		t.Errorf("an anonymous PUT = %d, want 401 or 404 (S-14.4); body:\n%s",
			anonymous.Code, anonymous.Body)
	}

	// And nothing was written by either attempt. The gate is the only thing that refused
	// them, so this is the half of the authorisation claim the status codes cannot
	// carry — and it is the half a "the gate will catch it" review skips.
	if got := fixed.read(pagePath); got != withSecrets {
		t.Errorf("a refused write changed the file:\n%s", got)
	}

	if rows := fixed.ledgerRows(); len(rows) != 0 {
		t.Errorf("a refused write recorded %d ledger rows:%s\n"+
			"A player publishing a campaign's secrets is the one thing §5.6.4 says cannot "+
			"happen, and this is the clause that says it did not.",
			len(rows), describeLedger(rows))
	}

	if entries := fixed.auditRows(); len(entries) != 0 {
		t.Errorf("a refused write wrote %d audit rows:%s", len(entries), describeAudit(entries))
	}
}

// TestAPrivateCampaignIs404ForAMemberOfNoOtherCampaign is the "no access is 404" half of
// ADR 0024, on this route.
//
// The route is mounted behind `RequireEdit`, and a private campaign is 404 for everybody
// who is not a member. That equality is the point: a 403 on a private campaign confirms
// it exists, and the route that mounts the tightest gate is the one where the distinction
// is most tempting to get wrong.
//
// **A member with the player role is deliberately absent**, for the reason `edit`'s copy
// of this test gives and the reason is worth restating because it is easy to get wrong
// the other way. `domain.ResolveAccess` resolves a private campaign's player member to
// `TierPlayer`, so `RequireEdit` answers that reader 403 — and that is **correct**, not a
// leak: the reader holds a membership row, so they already know the campaign exists, and
// a 403 tells them nothing they did not have. The oracle ADR 0024 closes is the one a
// *non-member* could open, and a non-member resolves to `TierNone` on a private campaign
// however their identity is established.
//
// (This was written the other way round first — with the player in the table expecting a
// 404 — and it failed against a gate that is behaving exactly as `edit`'s does.)
func TestAPrivateCampaignIs404ForAMemberOfNoOtherCampaign(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newPrivateHarness(t)
	fixed.write(pagePath, withSecrets)

	body := revealBody(t, map[string]any{"anchor": "traitor", "revealed": true})

	for name, requestor := range map[string]domain.Requestor{
		"a signed-in non-member":               strangerRequestor(),
		"an anonymous reader":                  anonymousRequestor(),
		"a signed-in non-member, in a new tab": strangerRequestor(),
		"an anonymous reader, in a new tab":    anonymousRequestor(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recorder := fixed.reveal(pagePath, requestor, `W/"anything"`, body)

			if recorder.Code != http.StatusNotFound {
				t.Errorf("a private campaign answered %d for %s, want 404; a 403 would "+
					"confirm the campaign exists", recorder.Code, name)
			}

			if got := fixed.read(pagePath); got != withSecrets {
				t.Errorf("a refused write changed the file:\n%s", got)
			}
		})
	}
}

// TestTheRefusalsAreTheSameBytesAsNoAccessIs is the oracle test, and it is the one that
// has to **compare** rather than to search.
//
// Four refusals that must be indistinguishable to anyone who cannot see the campaign:
//
//   - the **gate's** 404 for a private campaign;
//   - the gate's 404 for a slug naming no campaign at all;
//   - the route's 404 for a page that is not there;
//   - the route's 404 for a secret that is not on the page.
//
// Asserted on the **whole body** and on both headers, not on a substring. "Contains
// `not found`" is satisfied by a body that also says which of the four happened, which
// is precisely the leak: a reader who could tell "the page is not there" from "the
// secret is not there" learns the page exists from a URL they guessed, and a reader who
// could tell "the campaign is not yours" from either learns the campaign exists.
//
// The headers are in the assertion because `Cache-Control` is load-bearing rather than
// cosmetic: all four are reader-dependent (200 for a member, 404 for a stranger), so a
// reverse proxy in front of a self-hosted instance — the ordinary deployment — that
// stored one would serve it to the GM entitled to that page.
//
// The fourth row is the one a mutation is most likely to break, because
// `errUnknownSecret`'s own message says "no such secret on this page" and it is a
// perfectly reasonable sentence to write. That is precisely why it must not reach a
// body.
func TestTheRefusalsAreTheSameBytesAsNoAccessIs(t *testing.T) {
	t.Parallel()

	const (
		pagePath  = "Vault.md"
		missing   = "Nowhere.md"
		wantBody  = `{"error":"not found"}` + "\n"
		wantCache = "private, no-store"
		wantType  = "application/json; charset=utf-8"
	)

	// A private campaign, so the gate answers 404 for the two gate rows and the route
	// answers 404 for its own two — all four through one chain.
	fixed := newPrivateHarness(t)
	fixed.write(pagePath, withSecrets)

	present := fixed.validatorFor(pagePath)

	for name, testCase := range map[string]struct {
		requestor domain.Requestor
		target    string
		body      string
		ifMatch   string
	}{
		"the gate, for a non-member": {
			requestor: strangerRequestor(),
			target:    "/c/" + fixed.campaign.Slug + "/secrets/" + pagePath,
			body:      revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			ifMatch:   present,
		},
		"the gate, for a slug naming no campaign": {
			requestor: gmRequestor(),
			target:    "/c/no-such-campaign/secrets/" + pagePath,
			body:      revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			ifMatch:   present,
		},
		"a page that is not there": {
			requestor: gmRequestor(),
			target:    "/c/" + fixed.campaign.Slug + "/secrets/" + missing,
			body:      revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			ifMatch:   `W/"anything"`,
		},
		"a secret that is not on the page": {
			requestor: gmRequestor(),
			target:    "/c/" + fixed.campaign.Slug + "/secrets/" + pagePath,
			body:      revealBody(t, map[string]any{"anchor": "admiral", "revealed": true}),
			ifMatch:   present,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			header := http.Header{}
			if testCase.ifMatch != "" {
				header.Set("If-Match", testCase.ifMatch)
			}

			recorder := fixed.request(http.MethodPut, testCase.target,
				testCase.requestor, header, testCase.body)

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("%s = %d, want 404; body:\n%s", name, recorder.Code, recorder.Body)
			}

			if got := recorder.Body.String(); got != wantBody {
				t.Errorf("%s: the body is %q, want %q\n"+
					"Every 404 on this surface must be the same bytes, or a reader can tell "+
					"which of the four refusals it was.", name, got, wantBody)
			}

			if got := recorder.Header().Get("Cache-Control"); got != wantCache {
				t.Errorf("%s: Cache-Control is %q, want %q — every one of these responses "+
					"is reader-dependent, so a shared cache must not store it",
					name, got, wantCache)
			}

			if got := recorder.Header().Get("Content-Type"); got != wantType {
				t.Errorf("%s: Content-Type is %q, want %q", name, got, wantType)
			}
		})
	}
}

// TestTheHandlersOwnGmCheckIsNeverTheDecision is the third claim: the handler does not
// authorise, and the store's re-check is what actually holds.
//
// The gate is told — by the fixture — that the requestor is a GM, and the request still
// comes back 500 with **no ledger row**, because `store.RevealSecret` re-reads the
// membership inside its own transaction and the row was deleted from the database
// underneath the gate. That is the observable form of "the handler asked the gate rather
// than deciding", and it is worth a test because it is the only defence that survives a
// revocation racing a request.
//
// It also asserts the direction of the failure, which is the whole of §5.6.2's rule for
// this subsystem: a 500 with no row means reconciliation will not re-apply anything, so
// the outcome is toward hiding.
func TestTheHandlersOwnGmCheckIsNeverTheDecision(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	// The membership goes **after** the harness is built, so the gate — which this
	// fixture answers from memory — still believes the requestor is the GM. The two
	// sources disagreeing is exactly the race this test stages, and taking the
	// validator first is what keeps the precondition out of the picture: the failure
	// under test is the write's, not the comparison's.
	validator := fixed.validatorFor(pagePath)

	if err := sharedStore.DeleteMembership(
		fixed.t.Context(),
		fixed.campaign.ID,
		gmUser,
	); err != nil {
		t.Fatalf("delete the GM's membership: %v", err)
	}

	recorder := fixed.reveal(pagePath, gmRequestor(), validator,
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

	if recorder.Code == http.StatusNoContent {
		t.Fatalf("a demoted GM's reveal = 204; store.RevealSecret re-checks the membership " +
			"inside its own transaction, and that check — not the gate the handler consulted " +
			"— is the one that decides (store.ErrNotGM)")
	}

	// The status is 500 rather than 403 on purpose. The *gate* is what answers 403, and
	// the gate has already said yes; the failure happened after that, inside the write.
	// Reporting it as 403 would blame the reader for a fault the route had.
	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("a demoted GM's reveal = %d, want 500; body:\n%s", recorder.Code, recorder.Body)
	}

	// No row, which is the property that makes the 500 the safe direction: nothing for
	// reconciliation to re-apply.
	if rows := fixed.ledgerRows(); len(rows) != 0 {
		t.Errorf("a demoted GM recorded %d ledger rows:%s", len(rows), describeLedger(rows))
	}

	if entries := fixed.auditRows(); len(entries) != 0 {
		t.Errorf("a demoted GM wrote %d audit rows:%s", len(entries), describeAudit(entries))
	}

	// And the byte is down, which is stated rather than wished away: the ordering is
	// deliberate (see the package header), and the reason it is safe is the absence of
	// the row above rather than the presence of one.
	if got := fixed.read(pagePath); got != firstHalfRevealed {
		t.Errorf("the page is\n%q\nwant\n%q\nThe byte goes down before the ledger by "+
			"design; no ledger row is what makes that safe.", got, firstHalfRevealed)
	}
}

// TestTheRefusalBodiesCarryNoPageContent is the S-12.3 half of the surface: not one
// refusal this route can produce may carry a page's secret text — for any requestor, at
// any status, in the body or in a header.
//
// A table over the refusals a reader can provoke, because the leak this is guarding
// against is the *error* path. A handler that formatted a callout's title or its first
// body line into a refusal would be leaking the secret on exactly the requests nobody
// audits, and every one of those requests is a GM having a bad time — which is precisely
// when someone adds a `slog.String("secret", …)` and moves on.
//
// **The GM's 412 is absent, and its absence is the point.** S-6.2 requires that response
// to carry the page's current source, and on a page holding a `[!secret]` that source is
// the secret. It is safe because the gate admits only the GM — see
// `TestTheConflictBodyCarriesThePageOnlyToTheGm`, which asserts that positively and then
// asserts the *absence* for every reader the gate refuses. Putting it in this table would
// have meant asserting a contradiction, and dropping it silently would have left the one
// response on this route that does carry page text unchecked.
//
// `current` is the other field worth explaining: a row that has to reach a particular
// status has to present the **matching** validator, or it gets a 412 instead and never
// tests what it names. Two rows here were wrong for exactly that reason before they were
// fixed — a "secret that is not there" row sending `W/"anything"` was answered 412, and
// the table then reported the page source it legitimately carries as a leak.
func TestTheRefusalBodiesCarryNoPageContent(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	for name, testCase := range map[string]struct {
		target string
		body   string
		// current asks for the page's own validator rather than a stale one, so the
		// request reaches the status the row names.
		current bool
	}{
		"no If-Match": {
			target: pagePath,
			body:   revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
		},
		"a page that is not there": {
			target:  "Nowhere.md",
			body:    revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			current: true,
		},
		"a secret that is not on the page": {
			target:  pagePath,
			body:    revealBody(t, map[string]any{"anchor": "admiral", "revealed": true}),
			current: true,
		},
		"an ordinal past the end": {
			target:  pagePath,
			body:    revealBody(t, map[string]any{"ordinal": 7, "revealed": true}),
			current: true,
		},
		"a reveal of a nested callout": {
			target:  "Nested.md",
			body:    revealBody(t, map[string]any{"ordinal": 0, "revealed": true}),
			current: true,
		},
		"a malformed body": {
			target:  pagePath,
			body:    `{"anchor":"traitor"}`,
			current: true,
		},
		"a misspelled field": {
			target:  pagePath,
			body:    `{"anchro":"traitor","revealed":true}`,
			current: true,
		},
		"a traversal": {
			target:  "..%2f..%2fetc%2fpasswd",
			body:    revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			current: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write(pagePath, withSecrets)
			fixed.write("Nested.md",
				"Prose.\n\n> [!secret]- Outer\n> > [!secret]- Inner  ^inner\n> > Body.\n")

			header := http.Header{}
			if testCase.current {
				header.Set("If-Match", fixed.validatorFor(pagePath))
			}

			recorder := fixed.request(http.MethodPut,
				"/c/"+fixed.campaign.Slug+"/secrets/"+testCase.target,
				gmRequestor(), header, testCase.body)

			if recorder.Code < 400 {
				t.Fatalf("%s answered %d, which is not a refusal at all; the table is "+
					"meant to cover every status a reader can provoke for a mistake",
					name, recorder.Code)
			}

			assertNoSecretText(t, name+" (the body)", recorder.Body.String())
			assertNoSecretText(t, name+" (the headers)", headersOf(recorder.Header()))
		})
	}
}

// TestTheConflictBodyCarriesThePageOnlyToTheGm is the **exception**, asserted as an
// exception rather than left as a gap in the rule.
//
// S-6.2 requires the 412 to carry "the current body and its hash", and the current body
// of a page holding a `[!secret]` is the secret. That is safe here because the only
// reader who can reach a 412 is a GM the gate admitted — a player is refused 403 before
// the handler runs, an anonymous reader 401, a stranger 404 — so the requirement and
// S-12.3 do not conflict on this surface.
//
// **That is a claim about the chain rather than about the response**, and a test that
// only inspected the response would be satisfied by a route that also handed the page to
// everybody else. So both halves are asserted: the GM does get the source (S-6.2,
// positively, with the *whole* page rather than the one callout), and every reader who
// does not is refused before it with a response carrying none of it.
func TestTheConflictBodyCarriesThePageOnlyToTheGm(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	stale := fixed.validatorFor(pagePath)
	fixed.write(pagePath, firstHalfRevealed)

	body := revealBody(t, map[string]any{"anchor": "traitor", "revealed": true})

	// The GM's own 412, and it does carry the page — including the secret callout that
	// is still collapsed, which is the half S-6.2 asks for and the half S-12.3 would
	// object to if the reader were anybody else.
	gm := fixed.reveal(pagePath, gmRequestor(), stale, body)
	if gm.Code != http.StatusPreconditionFailed {
		t.Fatalf("the GM's stale reveal = %d, want 412; body:\n%s", gm.Code, gm.Body)
	}

	for _, fragment := range []string{firstCalloutBody, secondCalloutBody, firstCalloutTitle} {
		if !strings.Contains(gm.Body.String(), fragment) {
			t.Errorf("the GM's 412 does not carry %q, so S-6.2's \"412 with the current "+
				"body\" is not being answered:\n%s", fragment, gm.Body)
		}
	}

	// And nobody else. Same page, same stale validator, readers the gate refuses — and
	// the assertion is the *absence* of the page from every one of them.
	for name, requestor := range map[string]domain.Requestor{
		"a player":               playerRequestor(),
		"an anonymous reader":    anonymousRequestor(),
		"a signed-in non-member": strangerRequestor(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recorder := fixed.reveal(pagePath, requestor, stale, body)

			if recorder.Code == http.StatusPreconditionFailed {
				t.Fatalf("%s was answered 412, so it reached the handler — and a 412 "+
					"carries the page's source, secret text and all", name)
			}

			assertNoSecretText(t, name+" (the body)", recorder.Body.String())
			assertNoSecretText(t, name+" (the headers)", headersOf(recorder.Header()))
		})
	}
}

// assertNoSecretText fails if raw carries any of the page's secret fragments.
//
// The check is a plain `strings.Contains` on the **whole** value, deliberately, and the
// reason is the one `internal/content/redact_test.go` gives for its blunt raw-bytes
// layer: an assertion that parsed the response could be satisfied by text split across
// nodes, escaped, or sitting in an attribute. There is no structure here to hide in —
// these are JSON bodies and header sets — so the blunt check is both the simplest and
// the strongest available, and
// `TestTheSecretFragmentDetectorFindsAFragment` is what keeps it from being an
// assertion that cannot fail.
func assertNoSecretText(t *testing.T, where, raw string) {
	t.Helper()

	for _, secret := range secretFragments {
		if strings.Contains(raw, secret) {
			t.Errorf("%s carries %q.\n"+
				"S-12.3 forbids an error or an event carrying page content, and a "+
				"`[!secret]` body is page content — the temptation is specific, because "+
				"the natural debugging line for \"which callout did that\" is the callout's "+
				"own text.", where, secret)
		}
	}
}

// secretFragments is what must never appear in a response this route produces.
//
// A slice rather than three separate `Contains` calls so that the list has one owner and
// adding a fragment to the fixture means adding it here.
var secretFragments = []string{firstCalloutBody, secondCalloutBody, firstCalloutTitle}

// TestTheSecretFragmentDetectorFindsAFragment is the meta-test, and it exists because an
// absence assertion nobody has ever seen fail is a green light wired to nothing.
//
// The detector is driven with values that *do* carry each fragment, and each has to be
// caught. A `Contains` loop cannot really fail here, so the risk is not that the
// detector is wrong — it is that `secretFragments` has drifted empty, or that the
// fixture no longer contains what the list names, and then every `assertNoSecretText` in
// this package would be asserting against strings the page never had.
//
// So both ends are checked: each listed fragment is present in the page fixture, and each
// is found in a document that carries it.
func TestTheSecretFragmentDetectorFindsAFragment(t *testing.T) {
	t.Parallel()

	if len(secretFragments) == 0 {
		t.Fatal("no fragments are listed, so every assertNoSecretText in this package " +
			"would pass against a response containing the whole page")
	}

	// Each fragment is really in the fixture every one of those assertions uses.
	for _, fragment := range secretFragments {
		if !strings.Contains(withSecrets, fragment) {
			t.Errorf("the page fixture no longer contains %q, so asserting its absence "+
				"proves nothing", fragment)
		}
	}

	// And each is found when it is present, in each of the three places it could hide.
	for _, fragment := range secretFragments {
		t.Run(fragment, func(t *testing.T) {
			t.Parallel()

			for where, carrier := range map[string]string{
				"a JSON field": `{"error":"` + fragment + `"}`,
				"an HTML body": "<p>" + fragment + "</p>",
				"a header":     fragment,
			} {
				if !carriesAnyFragment(carrier) {
					t.Errorf("the detector did not find %q in %s: %q",
						fragment, where, carrier)
				}
			}
		})
	}
}

// carriesAnyFragment is the predicate `assertNoSecretText` uses, as a function so the
// meta-test can drive it without failing.
func carriesAnyFragment(raw string) bool {
	for _, fragment := range secretFragments {
		if strings.Contains(raw, fragment) {
			return true
		}
	}

	return false
}

// headersOf renders a header set as one string, so a secret in a header is as visible as
// one in a body.
//
// **Values only.** A header *name* is this project's vocabulary and a secret cannot be in
// one — and joining name and value would let a `Contains` match a name, which would be a
// false failure rather than a caught leak.
func headersOf(header http.Header) string {
	var out strings.Builder

	for _, values := range header {
		for _, value := range values {
			out.WriteString(value)
			out.WriteString("\n")
		}
	}

	return out.String()
}
