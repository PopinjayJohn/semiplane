package wiki_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/web/components"
)

// The route's response contract, asserted on served documents.
//
// Split from `nav_test.go` because the two answer different questions and have
// different failure modes: the navigation is a *shape* question and is asked over
// the parsed tree, while everything here is a *bytes* question — a validator, a
// directive, a status — and those live in headers a DOM cannot hold.

// EveryInteractiveElementCarriesTheTargetClass is UI §10.6, and §7.3's rule that
// the markup carries the class while the stylesheet decides what it means.
//
// Run over **every** state this route can answer rather than over one document,
// because the states are built by different branches: a 200 goes through
// `writePage` and a 404 through `writeFailure`, and they share only the composition
// in `document`. A rule checked on the 200 alone would pass on a failure state that
// rendered a landmark without its class, which is precisely the kind of bug a
// shared helper invites.
func TestEveryInteractiveElementCarriesTheTargetClass(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		requestor domain.Requestor
		path      string
	}{
		{name: "gm", requestor: gmRequestor(), path: "/c/greyhaven/wiki/Vault"},
		{name: "player", requestor: playerRequestor(), path: "/c/greyhaven/wiki/Vault"},
		{name: "anonymous", requestor: anonymousRequestor(), path: "/c/greyhaven/wiki/Vault"},
		{name: "a page that is not there", requestor: gmRequestor(), path: "/c/greyhaven/wiki/Nowhere"},
		{
			name: "a path that left the campaign", requestor: gmRequestor(),
			path: "/c/greyhaven/wiki/notes/%2e%2e%2fsecret",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t).withGameplaySystem()
			fixed.campaigns = &campaignLister{campaigns: []domain.Campaign{
				{ID: testCampID, Slug: testSlug, Name: "Greyhaven"},
			}}
			fixed.indexed("Vault.md", "notes/Goblin.md")
			fixed.write("Vault.md", pageBody("Iron and rust.\n"))

			audit := &audit{
				where: testCase.name,
				root:  parseDocument(t, fixed.get(testCase.path, testCase.requestor).Body.String()),
			}
			auditTargets(audit)
			audit.report(t)
		})
	}
}

// TestEveryRenderedTargetCarriesNoPositiveTabindex is UI §7.2's other structural
// rule, over the same set of documents.
//
// The tab order is the document's order; a positive `tabindex` replaces it with an
// author's ordering, and an element that is not in the document cannot be reached
// at all. The shell's three landmarks and two skip-link targets carry
// `tabindex="-1"` precisely so focus lands somewhere announced, so this asserts
// the *absence of the positive* rather than the presence of the negative — a
// document in which the skip-link targets lost their `tabindex` is a different
// failure, and one this rule is not written to catch.
func TestEveryRenderedTargetCarriesNoPositiveTabindex(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		requestor domain.Requestor
		path      string
	}{
		{name: "gm", requestor: gmRequestor(), path: "/c/greyhaven/wiki/Vault"},
		{name: "player", requestor: playerRequestor(), path: "/c/greyhaven/wiki/Vault"},
		{name: "anonymous", requestor: anonymousRequestor(), path: "/c/greyhaven/wiki/Vault"},
		{name: "a page that is not there", requestor: gmRequestor(), path: "/c/greyhaven/wiki/Nowhere"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write("Vault.md", pageBody("Iron and rust.\n"))

			audit := &audit{
				where: testCase.name,
				root:  parseDocument(t, fixed.get(testCase.path, testCase.requestor).Body.String()),
			}
			auditTabindex(audit)
			audit.report(t)
		})
	}
}

// TestVocabularySurvivesEveryDocumentThisRouteRenders is UI §1.2 and §10.2's vocabulary
// rule, over the parsed tree rather than over the response bytes.
//
// The tree because the four carriers are four different things and a substring
// assertion over the response cannot say which one it caught — a failure message
// naming the carrier is the difference between a fix in five minutes and a grep.
// The carriers are the audit's, and `TestEveryAuditFailsOnTheThingItWatches` is
// what holds the audit to all four, so this test is free to assert the *absence*
// over real documents without also proving the audit can see.
//
// The response bytes are asserted separately by the existing
// `TestTheInterfaceNeverNamesWorldOrSession`, and both are kept: the substring
// form catches anything the parser discards, and the tree form can say where.
func TestVocabularySurvivesEveryDocumentThisRouteRenders(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		requestor domain.Requestor
		path      string
	}{
		{name: "gm", requestor: gmRequestor(), path: "/c/greyhaven/wiki/Vault"},
		{name: "player", requestor: playerRequestor(), path: "/c/greyhaven/wiki/Vault"},
		{name: "anonymous", requestor: anonymousRequestor(), path: "/c/greyhaven/wiki/Vault"},
		{name: "a page that is not there", requestor: gmRequestor(), path: "/c/greyhaven/wiki/Nowhere"},
		{
			name: "a path that left the campaign", requestor: gmRequestor(),
			path: "/c/greyhaven/wiki/notes/%2e%2e%2fsecret",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write("Vault.md", pageBody("Iron and rust.\n"))

			audit := &audit{
				where: testCase.name,
				root:  parseDocument(t, fixed.get(testCase.path, testCase.requestor).Body.String()),
			}
			auditVocabulary(audit)
			audit.report(t)
		})
	}
}

// TestTheDegradedWarningAppearsOnceOnACampaignRoute is §7.2's "no heading is
// repeated", aimed at the pair that repeats it.
//
// The campaign shell hands `Instance.Degraded` to the **footer**, because on a
// campaign route the rail is meant to hold the campaign's own panels and
// `InstanceRail` renders the same warning as an `<h2>Not working</h2>`. So a route
// that passed the instance rail's degraded list through unchanged would meet the
// reader with that heading twice in one document — and only on a campaign with a
// broken subsystem, which is exactly the sort of document nobody opens a test
// browser for.
//
// The failure state is included because it renders the same rail and the same
// footer, and a composition that dropped the rail on the error path would make
// the 200 pass while the 404 failed.
func TestTheDegradedWarningAppearsOnceOnACampaignRoute(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/c/greyhaven/wiki/Vault", "/c/greyhaven/wiki/Nowhere"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.instance().Degraded = []components.DegradedView{
				{Name: "Content watcher", Detail: "the vault is not being watched"},
			}
			fixed.write("Vault.md", pageBody("Iron and rust.\n"))

			audit := &audit{
				where: path,
				root:  parseDocument(t, fixed.get(path, gmRequestor()).Body.String()),
			}
			auditHeadings(audit)
			audit.report(t)
		})
	}
}

// TestTheValidatorRevalidatesOnlyTheVariantItNames is S-5.3 seen from the client.
//
// Two properties, and the second is the one the first cannot see:
//
//  1. A client holding the current variant's validator gets a 304 and no bytes.
//  2. A client holding the **other** variant's validator gets a 200.
//
// The second is the leak path closed. The salt in `CacheKey.ETag` exists because
// without it a GM's validator and a player's would be the same string, and a
// client holding the GM's would be told "your copy is current" and keep the
// unredacted body. Asserting that the two strings differ — which
// `TestGMAndPlayerNeverShareAnETag` does — is necessary and not sufficient: two
// implementations can mint different strings and still compare them equal. This
// asserts the *consequence* through the same `If-None-Match` comparison a browser
// makes.
func TestTheValidatorRevalidatesOnlyTheVariantItNames(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	gmETag := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Header().Get("ETag")
	playerETag := fixed.get("/c/greyhaven/wiki/Vault", playerRequestor()).Header().Get("ETag")

	for _, testCase := range []struct {
		name      string
		sent      string
		requestor domain.Requestor
		want      int
	}{
		{
			name: "the gm revalidating the gm variant", sent: gmETag,
			requestor: gmRequestor(), want: http.StatusNotModified,
		},
		{
			name: "the player revalidating the player variant", sent: playerETag,
			requestor: playerRequestor(), want: http.StatusNotModified,
		},
		{
			// The one that matters. A player holding the GM's validator must be
			// told to fetch, not told its copy is current.
			name: "a player revalidating the gm variant", sent: gmETag,
			requestor: playerRequestor(), want: http.StatusOK,
		},
		{
			name: "the gm revalidating the player variant", sent: playerETag,
			requestor: gmRequestor(), want: http.StatusOK,
		},
		{
			name: "a stale validator", sent: `W/"0000"`,
			requestor: gmRequestor(), want: http.StatusOK,
		},
		{
			// The one a revalidation implementation forgets: a request carrying *no*
			// `If-None-Match` at all. A guard that dropped the empty-header case
			// answers 304 for every such request, so a client that reached the route
			// without the header would be told its copy is current without having
			// named anything.
			name: "no validator at all", sent: "",
			requestor: gmRequestor(), want: http.StatusOK,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			header := http.Header{}
			if testCase.sent != "" {
				header.Set("If-None-Match", testCase.sent)
			}

			recorder := fixed.getWith("/c/greyhaven/wiki/Vault", testCase.requestor, header)

			if recorder.Code != testCase.want {
				t.Errorf("status = %d, want %d; a client was told %s", recorder.Code,
					testCase.want,
					revalidationVerdict(recorder.Code))
			}

			if testCase.want == http.StatusNotModified && recorder.Body.Len() != 0 {
				t.Errorf("a 304 carried %d bytes; RFC 9110 §15.4.5 says it carries none",
					recorder.Body.Len())
			}
		})
	}
}

// revalidationVerdict says what a wrong status means, for a failure message.
func revalidationVerdict(got int) string {
	if got == http.StatusNotModified {
		return "its copy is current when it is not (S-5.3: a validator is salted " +
			"with include_secrets)"
	}

	return "its copy is stale when it is current"
}

// TestEachVariantCarriesItsOwnCacheDirectives is §6.6's cache table, asserted by
// **equality** rather than by `strings.Contains`.
//
// The existing `TestOnlyTheGMVariantMayBeStored` checks that `no-store` is present
// on one variant and absent on the others, which is S-5.4's half. The equality is
// the rest of it: `private, no-cache` on a redacted page is a *choice* this
// project's `content.CacheControl` made — stricter than ADR 0016's table asks for,
// because a campaign can be made private while a shared cache still holds its
// pages — and a substring assertion would pass on a value that had become
// `public, max-age=3600`, which is the opposite choice.
func TestEachVariantCarriesItsOwnCacheDirectives(t *testing.T) {
	t.Parallel()

	// Repeated rather than referenced, because these are the values the record
	// fixes and a test that imported them from the code under test would pass when
	// the code changed them. The GM value is S-5.4 verbatim.
	const (
		wantSecret   = "private, no-store"
		wantRedacted = "private, no-cache"
	)

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	for _, testCase := range []struct {
		name      string
		requestor domain.Requestor
		want      string
	}{
		{name: "gm", requestor: gmRequestor(), want: wantSecret},
		{name: "player", requestor: playerRequestor(), want: wantRedacted},
		{name: "anonymous", requestor: anonymousRequestor(), want: wantRedacted},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			recorder := fixed.get("/c/greyhaven/wiki/Vault", testCase.requestor)
			got := recorder.Header().Get("Cache-Control")

			if got != testCase.want {
				t.Errorf("Cache-Control = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestTheWikiRouteVariesOnTheCookieAndNothingElse is ADR 0035's corrected text and
// S-13.5, and the two are in tension on purpose.
//
// S-13.5 says "**No `Vary: Cookie` is emitted**", and the reason it gives is that
// `sp_ui` never varies the document. This route emits `Vary: Cookie` anyway,
// because the document *does* vary — by access tier, since the shell carries the
// reader's name and a sign-out form — and a cache that keyed only on the URL would
// hand one reader another's header. ADR 0035 originally generalised S-13.5 into
// "no `Vary` at all, on any route", deleted this route's header because a test
// could not see the variation it was protecting, and was corrected.
//
// So both halves are asserted, and the second is the one that keeps the first
// honest: the value must be exactly `Cookie` and must **not** name `sp_ui`, which
// is the variation S-13.5 actually forbids and the one that would fragment every
// shared cache in front of the instance.
//
// Every tier **and** the failure state. A header written on the success path only
// would leave the 404 looking cacheable-but-not-varying, which is the exact
// combination that serves one reader's error page to another — and the error page
// carries the request id, so it is a page a cache must not keep anyway.
func TestTheWikiRouteVariesOnTheCookieAndNothingElse(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	for _, testCase := range []struct {
		name      string
		path      string
		requestor domain.Requestor
	}{
		{name: "gm", path: "/c/greyhaven/wiki/Vault", requestor: gmRequestor()},
		{name: "player", path: "/c/greyhaven/wiki/Vault", requestor: playerRequestor()},
		{name: "anonymous", path: "/c/greyhaven/wiki/Vault", requestor: anonymousRequestor()},
		{
			name: "a page that is not there", path: "/c/greyhaven/wiki/Nowhere",
			requestor: gmRequestor(),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := fixed.get(testCase.path, testCase.requestor).Header().Get("Vary")

			if got != "Cookie" {
				t.Errorf("Vary = %q, want %q exactly: the document carries the "+
					"reader's name and a sign-out form, and nothing else about the "+
					"cookie reaches the bytes (ADR 0035)", got, "Cookie")
			}
		})
	}
}

// TestTheDocumentDoesNotVaryByTheThemeCookie is the load-bearing half of the
// record above, asserted the way the record says it must be: by the **bytes**,
// across six cookie values, rather than by the absence of a header.
//
// ADR 0035's argument is that a test asserting a header's absence cannot see the
// variation the header was protecting. The same argument runs the other way: an
// absence assertion cannot see a *future* cookie dependency added with no header
// change to notice. So each document is fetched six times — no cookie, four valid
// preferences, and a malformed one — and every pair of responses must be
// byte-identical.
//
// Both documents this route can serve are covered, and that is not thoroughness
// for its own sake: the 404 is written by a different branch of the handler with
// its own `ShellView`, so a cookie dependency introduced there would be invisible
// to a check that only looked at the 200.
//
// The 404 embeds the request id, which differs on every request by design, so
// every request here carries the **same** inbound `X-Request-Id`. That is not a
// trick to make the comparison work — `middleware.RequestID` honours a validated
// inbound id precisely so a proxy's correlation id survives, and holding it
// constant is what lets the one genuinely per-request value in the document be
// excluded from the question this test asks.
func TestTheDocumentDoesNotVaryByTheThemeCookie(t *testing.T) {
	t.Parallel()

	values := []string{
		"",
		"sp_ui=theme=auto&ui=auto",
		"sp_ui=theme=dark&ui=tv",
		"sp_ui=theme=light&ui=phone",
		"sp_ui=theme=dark ui=tv",
		"sp_ui=nonsense",
	}

	for _, document := range []struct {
		name string
		path string
	}{
		{name: "a page", path: "/c/greyhaven/wiki/Vault"},
		{name: "a page that is not there", path: "/c/greyhaven/wiki/Nowhere"},
	} {
		t.Run(document.name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t)
			fixed.write("Vault.md", pageBody("Iron and rust.\n"))

			with := func(value string) *httptest.ResponseRecorder {
				return fixed.getWith(document.path, gmRequestor(), http.Header{
					"Cookie":       []string{value},
					"X-Request-Id": []string{"cookie-audit"},
				})
			}

			baseline := with(values[0]).Body.String()

			for _, value := range values[1:] {
				if with(value).Body.String() != baseline {
					t.Errorf("Cookie: %q changed the %s; UI §3.7 resolves "+
						"data-theme and data-ui client-side before the first paint, so "+
						"the bytes must be identical for every reader (S-13.5)",
						value, document.name)
				}
			}
		})
	}
}

// TestAPrivateCampaignAndACampaignThatDoesNotExistAnswerIdentically is S-14.3 and
// AGENTS.md's "no access is 404, never 403", at the strongest form available here:
// two 404s with the same status, the same content type and the **same bytes**.
//
// The reason it is in this package is that this route is what a stranger would
// probe. A private campaign the reader is not a member of has to be
// indistinguishable from a slug that names nothing at all, because a 403 — or a
// 404 whose body mentioned the campaign, or a slower response, or a different
// `Content-Type` — turns the status code into an existence oracle.
//
// The request id is the one header that differs, and it is asserted to differ:
// `middleware.RequestID` is per request by design, and a test that demanded
// identical headers would be asking the two responses to be the same request.
func TestAPrivateCampaignAndACampaignThatDoesNotExistAnswerIdentically(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).private()
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	private := fixed.get("/c/greyhaven/wiki/Vault", anonymousRequestor())
	absent := fixed.get("/c/no-such-campaign/wiki/Vault", gmRequestor())

	if private.Code != http.StatusNotFound {
		t.Errorf("a private campaign answered %d to an anonymous reader, want 404",
			private.Code)
	}

	if absent.Code != private.Code {
		t.Errorf("a campaign that does not exist answered %d and a private one "+
			"answered %d; the two must be indistinguishable", absent.Code, private.Code)
	}

	for _, header := range []string{"Content-Type"} {
		if got, want := private.Header().Get(header), absent.Header().Get(header); got != want {
			t.Errorf("%s is %q for a private campaign and %q for an absent one",
				header, got, want)
		}
	}

	if private.Body.String() != absent.Body.String() {
		t.Errorf("the two 404s differ.\nprivate: %q\nabsent:  %q",
			private.Body.String(), absent.Body.String())
	}

	// And neither mentions the campaign either body could be about, which is the
	// other half: a body that echoed the slug would undo the equality.
	for _, forbidden := range []string{"greyhaven", "no-such-campaign", "Vault"} {
		if strings.Contains(private.Body.String(), forbidden) {
			t.Errorf("the 404 body mentions %q", forbidden)
		}
	}
}

// TestThePageBodyIsByteIdenticalAcrossViewers is ADR 0017 and S-5.1, and this
// work item is a place it could have been broken.
//
// The *document* is now reader-dependent by design — it carries the reader's name,
// and the navigation's Admin row is GM-only — and that is ADR 0035's correction
// rather than a contradiction. What must stay byte-identical is the **page**,
// because the render cache holds exactly two variants of it and a third would be a
// per-user cache, which §5.5 rules out.
//
// So the assertion is scoped to `page-body`: the sanitised HTML with its
// references' addresses written in, which is the one thing this route caches. A
// page whose body varied by viewer would need the viewer's identity in the key, and
// that is a cache this project explicitly does not have.
func TestThePageBodyIsByteIdenticalAcrossViewers(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.pages = pageListing{pages: []domain.Page{
		{CampaignID: testCampID, Path: "Goblin.md"},
	}}
	fixed.write("Vault.md", pageBody("See [[Goblin]].\n"))

	for _, testCase := range []struct {
		name      string
		requestor domain.Requestor
	}{
		{name: "player", requestor: playerRequestor()},
		{name: "anonymous", requestor: anonymousRequestor()},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			gm := pageFragment(fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String())
			other := pageFragment(
				fixed.get("/c/greyhaven/wiki/Vault", testCase.requestor).Body.String())

			if gm != other {
				t.Errorf("the page body differs between a GM and a %s.\ngm:    %q\n%s: %q",
					testCase.name, gm, testCase.name, other)
			}

			if !strings.Contains(gm, `href="/c/greyhaven/wiki/Goblin"`) {
				t.Errorf("the page body carries no resolved reference, so the "+
					"comparison above was between two empty strings: %q", gm)
			}
		})
	}
}

// TestTheDocumentTitleFollowsTheShellVariantForm records a **known divergence**
// rather than a desired state, and it exists so that closing it is a test failure
// somebody chose over a test failure somebody inherited.
//
// UI §7.2 fixes the form as "Page — Section — Campaign", and `components` has two
// helpers for it: `documentTitle(page, instance)` for a pre-campaign route, which
// drops the middle part, and `documentTitleForCampaign(page, instance, campaign)`
// for a route inside one, which fills it. This route is inside a campaign, so §7.2's
// full form is "Vault — Greyhaven — Greyhaven" — the campaign name in the section
// slot and the *instance* name in the campaign slot.
//
// It gets "Vault — Greyhaven" instead, because `documentTitleForCampaign` is
// unexported and `internal/web/components` is not this work item's file. The fix
// is one line there (`func DocumentTitleForCampaign`) and one line here; the
// integration report names both.
//
// Asserted anyway, because a title that silently changed shape is exactly the
// drift the constant's comment warns about, and a test that says "this is the
// wrong form and here it is written down" is what makes the wrongness visible.
func TestTheDocumentTitleFollowsTheShellVariantForm(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	document := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String()

	root := parseDocument(t, document)
	head := findElements(root, "title")
	if len(head) != 1 {
		t.Fatalf("the document has %d <title> elements, want 1", len(head))
	}

	if got := strings.TrimSpace(textOf(head[0])); got != "Vault — Greyhaven" {
		t.Errorf("the document title is %q, want %q. UI §7.2's three-part form for a "+
			"route inside a campaign is \"Page — Campaign — Instance\"; this route "+
			"composes the two-part form because components.documentTitleForCampaign "+
			"is unexported", got, "Vault — Greyhaven")
	}
}

// TestTheBannerNamesTheCampaignRatherThanTheInstance is UI §8.3's rank 1, and it
// is a change this work item makes rather than one it merely preserves.
//
// Before the campaign reference was filled in, the header rendered its brand link
// — the *instance's* name — on a wiki page inside a campaign, because
// `ShellView.Campaign` was left at its zero value. Inside a campaign the current
// location is the campaign, and the instance name is noise in a 56px bar.
//
// Asserted on the link's `href` as well as its presence, because a header that
// reads "Greyhaven" and links to `/` is the shape of the bug: the label was right
// and the destination was the campaign list at the root, which is not a campaign.
func TestTheBannerNamesTheCampaignRatherThanTheInstance(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	root := parseDocument(t, fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String())

	banner := elementsWithRole(root, "banner")
	if len(banner) != 1 {
		t.Fatalf("the document has %d banners, want 1", len(banner))
	}

	link := elementByTestID(t, banner[0], "header-campaign")
	if got := attribute(link, "href"); got != "/c/greyhaven" {
		t.Errorf("the banner's campaign link points at %q, want /c/greyhaven", got)
	}

	if got := strings.TrimSpace(textOf(link)); got != "Greyhaven" {
		t.Errorf("the banner's campaign link reads %q, want the campaign's name", got)
	}

	if brand := elementsWithTestID(banner[0], "header-home"); len(brand) != 0 {
		t.Errorf("the banner inside a campaign has %d brand links, want none: "+
			"§8.3's rank 1 is the current location", len(brand))
	}
}

// TestTheBannerFallsBackToTheCampaignsSlug is the unnamed-campaign half, and it is
// asserted because a campaign registered without a name still has a URL and a
// banner reading an empty slot is the first thing a new install looks like.
//
// The same fallback `components.NewCampaignCard` and `documentTitle` make, written
// three times because `chrome.CampaignRef.label` is unexported on purpose. Here
// it is observable rather than a helper's business: the campaign's *name* is empty
// and the *slug* is what appears.
func TestTheBannerFallsBackToTheCampaignsSlug(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.campaignName = ""
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	recorder := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", recorder.Code, recorder.Body)
	}

	banner := elementsWithRole(parseDocument(t, recorder.Body.String()), "banner")
	link := elementByTestID(t, banner[0], "header-campaign")

	if got := strings.TrimSpace(textOf(link)); got != testSlug {
		t.Errorf("the banner reads %q for an unnamed campaign, want its slug %q",
			got, testSlug)
	}

	// And the document title, which is the other place the fallback shows. Both
	// halves in one test because they are one fallback.
	if !strings.Contains(recorder.Body.String(), ">Vault — "+testSlug+"<") {
		t.Errorf("the document title is not %q for an unnamed campaign:\n%s",
			"Vault — "+testSlug, recorder.Body)
	}
}
