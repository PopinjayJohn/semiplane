package search_test

// UI §10.2 and §10.6 for `GET /c/{slug}/search`, gate-blocking.
//
// # Why this file exists
//
// `make a11y` names this package in `A11Y_ROUTE_PKGS`, and naming a package there
// is a claim that it has §10.2 audits. `A11Y_TESTS` is a list of substrings and
// `go test -run` answers "[no tests to run]" and exits 0, so the claim can be
// satisfied by a **coincidence**: before this file, the only two tests in this
// package that matched any alternative in the pattern were
// `TestEveryDocumentHasExactlyOneH1CarryingTheCountAndQuery` (by `ExactlyOneH1`)
// and `TestEveryControlCarriesTheTargetClass` (by `Target`). The other thirty-one
// matched nothing.
//
// That is the failure this file exists to prevent, and it is the same shape as a
// green gate that looked somewhere else: **the gate was green for the wrong
// reason.** Renaming the thirty-one so they matched the pattern would have made
// the guard quiet and the gate no stronger, because "the package contributes a
// test" and "the route is audited by rules that can fail" are different claims
// and only the second one is worth anything. So this file adds the entry points
// `assets` and `wiki` use, with the negative control they use:
//
//	TestEveryRouteSatisfiesTheStructuralContract
//	TestEveryRouteCarriesTheTargetClassOnEveryFocusStop
//	TestEveryRouteAuditRejectsTheViolationItClaimsTo
//	TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch
//	TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe
//
// The first and second run the rules over the route's real documents. The third,
// fourth and fifth are what make them a gate rather than a comment: each rule is
// shown to reject a fixture that violates it, shown to say nothing about the
// documents it must not touch, and shown to report nothing at all on a document
// this route could serve.
//
// # What is reused and what is a second copy, stated rather than hidden
//
// The DOM plumbing is **not** a second copy. `document`, `doc.elements`,
// `doc.find`, `doc.focusable`, `isFocusable`, `attribute`, `hasAttribute`,
// `hasClassToken`, `textOf`, `findElements`, `describe`, `excerpt`,
// `firstBytes`, `retiredEntities`, `auditedQueries`, `testRows`, `hit`,
// `searchURL`, `resultCap`, `renderedRecorder`, `renderedDocument` and the
// requestors are all the package's own, from `harness_test.go` and
// `search_test.go`.
//
// Five **rules** do have a second spelling, and the reason is a signature rather
// than a disagreement: the rules below take an `auditFailer` so they can be run
// against a fixture with the failure count recorded, while the route's own five
// audits (`TestEveryDocumentHasExactlyOneH1CarryingTheCountAndQuery`,
// `TestNoTabindexAboveMinusOne`, `TestEveryTestHookAppearsExactlyOnce`,
// `TestNoRetiredEntityIsNamedAnywhere`, `TestEveryControlCarriesTheTargetClass`)
// are `*testing.T` functions iterating the package's document list. Those five
// are the route's own coverage and are **not** mine to reshape, so they stay
// exactly as they are and the gate's spelling sits beside them. Both spellings
// walk the same low-level helpers over the same documents, so the duplicated
// part is a rule's own few lines and not the element set it judges.
//
// The eventual fix is one exported audit package that `httpapi_test` and each
// route's `route_a11y_test.go` import — the same duplication
// `wiki/route_a11y_test.go` names. Until that exists this file stays a *narrow*
// copy: the ten rules §10.2 and §10.6 state for this route and nothing else.
//
// # The documents
//
// `auditedQueries()` is the package's own list of four and is reused whole,
// because a second subject list is a subject list that can drift from the first.
// Three documents are added, each because a rule is checked on it and not on the
// four:
//
//   - **The three reader tiers.** The result list's `ETag` is salted by tier and
//     `TestTheDocumentsDifferByReaderIsTheSubstantiveVaryCheck` measures the
//     documents differing, so the GM's, a player's and an anonymous reader's
//     result lists are three documents and not one with three spellings. The
//     anonymous document is also the only one with no account zone, which is why
//     the required hooks are per-subject rather than a flat list.
//   - **The capped list.** A result list that reaches `resultCap` renders a
//     caveat paragraph the other documents do not, and a rule checked on four
//     documents is a rule checked on four implementations.
//
// # The responses that are not documents
//
// Three, and all three are asserted rather than left to inference:
//
//   - The **304**. `TestEveryDocumentCarriesAnETagAndRevalidates` already asserts
//     that a revalidation carries no body, over all four documents, so this file
//     does not repeat it.
//   - The **access gate's refusal**, which is ADR 0024's byte-identical JSON and
//     therefore not a document at all.
//     `TestEveryRouteRefusesAnUnknownCampaignWithANonDocument`.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/domain"
)

// --- The documents ---------------------------------------------------------------

// routeDocument is one audited document and what this route's view model promises
// for it.
type routeDocument struct {
	rendered doc
	// signedIn says whether the shell renders its account zone. It is the only
	// thing the required hooks differ on, and it is asked per document rather than
	// assumed because the shell's own comment says the zone is absent for a reader
	// who is not signed in — so a hook that is missing from the anonymous document
	// is correct, and a flat list would report it as a §10.2 failure.
	signedIn bool
}

// auditedRoute returns every HTML document this route can put in front of a reader
// who passed the access gate.
func auditedRoute(t *testing.T) []routeDocument {
	t.Helper()

	documents := make([]routeDocument, 0, len(auditedQueries())+3)

	for where, query := range auditedQueries() {
		documents = append(documents, routeDocument{
			rendered: renderedDocument(t, where, query),
			signedIn: true,
		})
	}

	// The capped list, and the two other tiers. The idle and no-results documents
	// have no rows and no tier-dependent content, so the result list is where a
	// document that differs by reader is observable at all.
	documents = append(documents,
		routeDocument{
			rendered: document(t, "a capped result list", cappedRecorder(t)),
			signedIn: true,
		},
		routeDocument{
			rendered: document(t, "results for a player", tierRecorder(t, playerRequestor())),
			signedIn: true,
		},
		routeDocument{
			rendered: document(t, "results for an anonymous reader",
				tierRecorder(t, anonymousRequestor())),
			signedIn: false,
		},
	)

	return documents
}

// cappedRecorder serves a result list that has reached the route's cap.
func cappedRecorder(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	rows := make([]domain.SearchHit, 0, resultCap)
	for index := range resultCap {
		name := strconv.Itoa(index)
		rows = append(rows, hit(int64(index+1), "Notes/"+name+".md",
			"Note "+name, "a muster roll about goblins"))
	}

	return newHarness(t).withRows(rows...).get(searchURL("goblin"), gmRequestor())
}

// tierRecorder serves the result list to a reader other than the GM.
//
// Its own harness rather than a `renderedRecorder` case, because `renderedRecorder`
// switches on the *document* name and its `default` branch fails the test on a
// name it does not know — which is the right behaviour for a fixture list and the
// wrong one here, because the tier is the variable rather than the document.
func tierRecorder(t *testing.T, requestor domain.Requestor) *httptest.ResponseRecorder {
	t.Helper()

	return newHarness(t).withRows(testRows()...).get(searchURL("goblin"), requestor)
}

// --- The audit's own machinery ---------------------------------------------------

// auditFailer is the subset of *testing.T the rules use.
//
// An interface rather than `*testing.T` because the rules have to be *tested* — a
// rule that cannot be shown to reject a violation it claims to catch is a gate
// wired to nothing — and a test of a `*testing.T`-typed function is a test that
// fails the suite. `silentFailer` below satisfies this and records instead of
// reporting.
//
// Deliberately without `Fatalf`: the harness's own document parser aborts on a
// response that is not HTML, and that is correct behaviour for it to have.
type auditFailer interface {
	Helper()
	Errorf(format string, args ...any)
}

// silentFailer records `Errorf` calls instead of reporting them.
//
// A `testing.T` whose `Errorf` is intercepted, so a rule can be *tested* rather
// than merely run. That is the only way to assert a rule fires, and asserting it
// is the only way to know it is a gate rather than a comment: phase 5's first-draft
// gate tests could not fail, and an audit that reports nothing is the same failure
// with a green light on top.
//
// The *messages* are kept as well as the count, because a rule that fires for the
// wrong reason is still a rule that appears to work: a landmark audit that also
// reported duplicate hooks would pass a fixture built to trip the landmark rule,
// and the gate would look covered while the thing it names was not being checked.
type silentFailer struct {
	*testing.T

	failures int
	messages []string
}

// Errorf records the failure and carries on, so one document produces a count and
// a list rather than stopping at the first violation.
func (f *silentFailer) Errorf(format string, args ...any) {
	f.failures++
	f.messages = append(f.messages, fmt.Sprintf(format, args...))
	f.Logf(format, args...)
}

// Helper satisfies the rules' `t.Helper()` calls without recording anything.
func (f *silentFailer) Helper() {}

// mentions reports whether any recorded message contains a fragment.
func (f *silentFailer) mentions(fragment string) bool {
	return slices.ContainsFunc(f.messages, func(message string) bool {
		return strings.Contains(message, fragment)
	})
}

// parseFixture parses a minimal document written to trip one rule.
//
// Separate from the harness's `document` because a fixture is not a response: it
// has no route, no status and no headers, and the rule under test should be the
// only thing wrong with it.
func parseFixture(t *testing.T, body string) doc {
	t.Helper()

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	return doc{where: "the fixture", status: http.StatusOK, headers: http.Header{}, root: root}
}

// --- The DOM helpers this file adds ----------------------------------------------

// byID returns the element carrying an id, or nil.
//
// Distinct from the harness's `doc.testID`, which looks a hook up by `data-testid`
// and takes a `*testing.T` to fail on; this one is the `id` half of ARIA
// reference integrity and is used on fixtures, where failing the test would be the
// wrong answer.
func byID(rendered doc, id string) *html.Node {
	var found *html.Node

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		for child := node.FirstChild; child != nil && found == nil; child = child.NextSibling {
			if child.Type == html.ElementNode && attribute(child, "id") == id {
				found = child

				return
			}

			walk(child)
		}
	}

	walk(rendered.root)

	return found
}

// roleOf returns an element's explicit role, or the implicit one its tag carries.
//
// The implicit half is not a convenience: §7.2's contract is written as
// `header[banner]` and `footer[contentinfo]`, and a document relying on the
// implicit role is correct. The audit has to agree with the specification rather
// than with the subset somebody wrote an attribute for.
//
// A `<form>` is deliberately absent: a form is a landmark only with an explicit
// role, and calling it `form` here would invent a landmark the specification does
// not have. This route's forms carry `role="search"` explicitly, which is the
// reason the rule below can read them.
func roleOf(node *html.Node) string {
	if explicit := attribute(node, "role"); explicit != "" {
		return explicit
	}

	switch node.Data {
	case "header":
		return "banner"
	case "footer":
		return "contentinfo"
	case "nav":
		return "navigation"
	case "main":
		return "main"
	case "aside":
		return "complementary"
	default:
		return ""
	}
}

// landmark is one landmark region and how it is named.
type landmark struct {
	role string
	// name is the accessible name, following `aria-labelledby` when that is what it
	// uses.
	name string
	// namedBy records the mechanism, so a failure can say "labelled by nothing"
	// rather than "labelled by the empty string".
	namedBy string
	node    *html.Node
}

// routeLandmarks returns the document's landmark regions in document order.
func routeLandmarks(rendered doc) []landmark {
	var found []landmark

	rendered.elements(func(node *html.Node) {
		role := roleOf(node)

		switch role {
		case "banner", "navigation", "main", "complementary", "contentinfo", "search":
			found = append(found, landmark{
				role:    role,
				name:    accessibleName(rendered, node),
				namedBy: namingMechanism(node),
				node:    node,
			})
		default:
		}
	})

	return found
}

// namingMechanism records which attribute named a landmark.
func namingMechanism(node *html.Node) string {
	if attribute(node, "aria-label") != "" {
		return "aria-label"
	}

	if hasAttribute(node, "aria-labelledby") {
		return "aria-labelledby"
	}

	return "nothing"
}

// accessibleName returns a landmark's name, following `aria-labelledby` when used.
func accessibleName(rendered doc, node *html.Node) string {
	if label := attribute(node, "aria-label"); label != "" {
		return label
	}

	var parts []string

	for id := range strings.FieldsSeq(attribute(node, "aria-labelledby")) {
		if target := byID(rendered, id); target != nil {
			parts = append(parts, strings.TrimSpace(textOf(target)))
		}
	}

	return strings.Join(parts, " ")
}

// skipLink is one skip link and what it points at.
type skipLink struct {
	text string
	href string
	node *html.Node
}

// skipLinks returns the document's skip links, in document order.
//
// By class rather than by `href`: §7.2 fixes the targets and the class is the hook
// the stylesheet uses to move the link off-screen until it is focused, so a "skip"
// link without the class is not off-screen and is therefore in the wrong place.
// Reading it from the class is the same claim made from the other end.
func skipLinks(rendered doc) []skipLink {
	var found []skipLink

	rendered.elements(func(node *html.Node) {
		if node.Data != "a" || !hasClassToken(node, "skip-link") {
			return
		}

		found = append(found, skipLink{
			text: strings.TrimSpace(textOf(node)),
			href: attribute(node, "href"),
			node: node,
		})
	})

	return found
}

// containsFocusStop reports whether a subtree holds anything a keyboard reaches.
//
// The harness's `isFocusable`, which excludes a `disabled` control and knows
// `area` — §10.6's own set, and this project's opinion of it is not the thing
// under test.
func containsFocusStop(node *html.Node) bool {
	found := false

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		for child := current.FirstChild; child != nil && !found; child = child.NextSibling {
			if isFocusable(child) {
				found = true

				return
			}

			walk(child)
		}
	}

	walk(node)

	return found
}

// hasAncestorTag reports whether any ancestor carries the given tag.
func hasAncestorTag(node *html.Node, tag string) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && ancestor.Data == tag {
			return true
		}
	}

	return false
}

// headingLevel returns an element's heading level, or false when it is not one.
func headingLevel(node *html.Node) (int, bool) {
	if node.Type != html.ElementNode || len(node.Data) != 2 || node.Data[0] != 'h' {
		return 0, false
	}

	level, err := strconv.Atoi(node.Data[1:])
	if err != nil || level < 1 || level > 6 {
		return 0, false
	}

	return level, true
}

// --- §10.2: the rules -----------------------------------------------------------

// TestEveryRouteSatisfiesTheStructuralContract is UI §10.2, gate-blocking, for every
// document this route renders.
//
// Each rule in its own subtest, so a failure names the rule rather than "the a11y
// test failed". The order is §10.2's own list plus the two it implies and does not
// spell out — heading levels and reference integrity — because a landmark can be
// present and correctly labelled while a document's outline and its ARIA wiring are
// both broken, and neither shows up in a "landmarks are fine" result.
func TestEveryRouteSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	for _, subject := range auditedRoute(t) {
		t.Run(subject.rendered.where, func(t *testing.T) {
			t.Parallel()

			t.Run("ExactlyOneH1", func(t *testing.T) {
				assertTheDocumentHasExactlyOneH1(t, subject.rendered)
			})
			t.Run("HeadingLevelsNeverSkip", func(t *testing.T) {
				assertHeadingLevelsNeverSkip(t, subject.rendered)
			})
			t.Run("LandmarksArePresentAndDistinguishing", func(t *testing.T) {
				assertLandmarksArePresentAndDistinguishing(t, subject.rendered)
			})
			t.Run("NoPositiveTabindex", func(t *testing.T) {
				assertNoPositiveTabindex(t, subject.rendered)
			})
			t.Run("NoInlineOutlineSuppression", func(t *testing.T) {
				assertNoInlineOutlineSuppression(t, subject.rendered)
			})
			t.Run("NoAriaHiddenOnAFocusStop", func(t *testing.T) {
				assertNoAriaHiddenOnAFocusStop(t, subject.rendered)
			})
			t.Run("SkipLinksComeFirstAndResolve", func(t *testing.T) {
				assertSkipLinksComeFirstAndResolve(t, subject.rendered)
			})
			t.Run("EveryTestIDIsPresent", func(t *testing.T) {
				assertEveryTestIDIsPresent(t, subject.rendered, subject.signedIn)
			})
			t.Run("NoRetiredEntityIsNamed", func(t *testing.T) {
				assertNoRetiredEntityIsNamed(t, subject.rendered)
			})
			t.Run("EveryReferenceResolves", func(t *testing.T) {
				assertEveryReferenceResolves(t, subject.rendered)
			})
		})
	}
}

// assertTheDocumentHasExactlyOneH1 is §10.2's first rule and §7.2's.
//
// Exactly one, not at least one. Two `<h1>`s tell a screen reader the page has two
// titles; none tell it the page has none while looking complete to a sighted
// reader.
//
// The route's own `TestEveryDocumentHasExactlyOneH1CarryingTheCountAndQuery` holds
// the same rule over a superset of these documents *and* asserts the count and the
// query are in the heading; this is the gate's spelling of the structural half,
// which needs an `auditFailer` so the negative control below can run it.
func assertTheDocumentHasExactlyOneH1(t auditFailer, rendered doc) {
	t.Helper()

	headings := rendered.find("h1")
	if len(headings) == 1 {
		return
	}

	names := make([]string, 0, len(headings))
	for _, heading := range headings {
		names = append(names, describe(heading))
	}

	t.Errorf("%s: the document has %d <h1> elements, want exactly 1 (UI §10.2, §7.2); "+
		"found: %s", rendered.where, len(headings), strings.Join(names, ", "))
}

// assertHeadingLevelsNeverSkip is §7.2's second structural rule.
//
// Separate from the `<h1>` count because it can fail where the count passes: a
// document whose headings start at `<h3>` has exactly one `<h1>`-shaped hole and no
// valid outline at all.
//
// **This is the one rule on this route that is decided by two components neither of
// which knows about the other.** The centre's heading is the route's own `<h1>` on
// the failure document and a §4.7 state's on the other three, and the rail's is
// `components.InstanceRail`'s — and §7.2's rule is about the *sequence*. Every one
// of the three §4.7 states is mounted at `ui.HeadingPage` for this reason; mounting
// one at `HeadingInPage` would put an `<h2>` where the document's `<h1>` should be,
// and the count test would still pass.
func assertHeadingLevelsNeverSkip(t auditFailer, rendered doc) {
	t.Helper()

	previous := 0

	rendered.elements(func(node *html.Node) {
		level, isHeading := headingLevel(node)
		if !isHeading {
			return
		}

		if previous != 0 && level > previous+1 {
			t.Errorf("%s: a heading jumps from h%d to h%d at %s; a skipped level is "+
				"announced as a missing section (UI §7.2)",
				rendered.where, previous, level, describe(node))
		}

		previous = level
	})
}

// assertLandmarksArePresentAndDistinguishing is §10.2's landmark rule and §7.2's, in
// four parts.
//
// Presence, because a missing landmark is a region a landmark-navigation reader
// cannot jump to. Distinguishing labels, because two landmarks of the same role
// with the same name make landmark navigation useless. Exactness, for the names
// §7.2 writes out. And nesting, because a `contentinfo` inside a `main` is the
// article's and not the page's, and a screen reader's landmark list will say
// otherwise.
//
// **The `navigation` half is where this route is worth auditing rather than
// assuming.** §4.6 removes the navigation before a campaign exists and
// `results.templ` composes the **pre-campaign** shell — its own comment says so and
// says the two campaign routes change together — so these documents carry no
// navigation landmark and therefore no `#nav` skip link either. Both directions of
// that pairing are asserted in `assertSkipLinksComeFirstAndResolve`.
func assertLandmarksArePresentAndDistinguishing(t auditFailer, rendered doc) {
	t.Helper()

	found := routeLandmarks(rendered)

	present := map[string]int{}
	for _, region := range found {
		present[region.role]++
	}

	// `main` is the only landmark required unconditionally: §4.6 removes the
	// navigation before a campaign exists, and §3.1's tiers move the rail out of
	// the flow rather than out of the document, so the rail stays in the
	// accessibility tree at every tier.
	if present["main"] == 0 {
		t.Errorf("%s: the document has no main landmark (UI §10.2, §7.2)", rendered.where)
	}

	if present["main"] > 1 {
		t.Errorf("%s: the document has %d main landmarks, want 1 (UI §7.2); two of "+
			"them give a screen reader two articles and no way to choose",
			rendered.where, present["main"])
	}

	for _, region := range found {
		switch region.role {
		case "navigation", "complementary", "search":
			if region.name == "" {
				t.Errorf("%s: the %s landmark at %s is labelled by %s; §10.2 requires "+
					"distinguishing labels, and an unnamed landmark cannot be told from "+
					"another of the same role (UI §7.2)",
					rendered.where, region.role, describe(region.node), region.namedBy)
			}
		case "banner", "contentinfo":
		default:
		}
	}

	// The names §7.2 fixes. The two navigation names are checked as a set rather
	// than positionally, because which of them comes first is a tier decision the
	// stylesheet makes and the markup does not.
	allowedNavigation := map[string]bool{"Campaign": true, "Primary": true}

	// searchLandmarkNames are the two `role="search"` labels §7.2's own zones fix:
	// the header's persistent form, and this route's own form for the campaign being
	// searched. `components.ShellView` carries no `Search` field, so the header's
	// form is **not** in any document audited here — and it is in the set anyway,
	// because the rule is that the two names must be *distinct*, and a reader who
	// later wires the header's search in should not have to edit the gate to make
	// the change pass. The complementary half of that rule — that this route's
	// centre form is not labelled with the banner's name — is held by the route's
	// own `TestTheSearchRouteHasAtMostOneSearchLandmarkAndItIsNamed`, which is
	// where it belongs, because only that test knows the banner's label.
	searchLandmarkNames := []string{"Search pages", "Search this campaign"}

	allowedSearch := make(map[string]bool, len(searchLandmarkNames))
	for _, name := range searchLandmarkNames {
		allowedSearch[name] = true
	}

	for _, region := range found {
		switch region.role {
		case "navigation":
			if !allowedNavigation[region.name] {
				t.Errorf("%s: a navigation landmark is labelled %q; UI §7.2 names them "+
					"%q (the campaign nav) and %q (the compact bar)",
					rendered.where, region.name, "Campaign", "Primary")
			}
		case "complementary":
			if region.name != "Utilities" {
				t.Errorf("%s: the complementary landmark is labelled %q, want %q (UI §7.2)",
					rendered.where, region.name, "Utilities")
			}
		case "search":
			if !allowedSearch[region.name] {
				t.Errorf("%s: a search landmark is labelled %q; UI §7.2 fixes the two "+
					"search labels, and they must be distinct: %v",
					rendered.where, region.name, searchLandmarkNames)
			}
		case "banner", "contentinfo", "main":
		default:
		}
	}

	// Distinctness across the whole document.
	seen := map[string]string{}

	for _, region := range found {
		if region.name == "" {
			continue
		}

		key := region.role + "=" + region.name
		if first, duplicate := seen[key]; duplicate {
			t.Errorf("%s: two %s landmarks are both labelled %q (%s and %s); §10.2 "+
				"requires distinguishing labels or landmark navigation is useless",
				rendered.where, region.role, region.name, first, describe(region.node))
		}

		seen[key] = describe(region.node)
	}

	for _, region := range found {
		if region.role == "contentinfo" && hasAncestorTag(region.node, "main") {
			t.Errorf("%s: a contentinfo landmark is inside <main> at %s; it is the "+
				"page's footer, not the article's (UI §7.2)",
				rendered.where, describe(region.node))
		}
	}
}

// assertNoPositiveTabindex is §10.2's tabindex rule and §7.4's "no positive
// tabindex, anywhere — gate failure".
//
// Zero is excluded too. §7.4 says Tab follows natural document order, and a
// `tabindex="0"` moves one element to the front of the focus order while the markup
// still reads in the original order — so the two disagree and neither a reader nor a
// test can tell which one the page means. Only `-1` is usable.
//
// The value is parsed as a number rather than compared as a string, and an
// unparseable value is itself a finding: three browsers disagreeing about the tab
// order is the failure, and `+3` sorts before `0` in a string comparison.
//
// This route is the one place `+1` is easy to write by accident: the refine form
// and the §4.7 states each carry a focusable field, and "make this field first" is
// the obvious way to move focus to it.
func assertNoPositiveTabindex(t auditFailer, rendered doc) {
	t.Helper()

	rendered.elements(func(node *html.Node) {
		raw := attribute(node, "tabindex")
		if raw == "" {
			return
		}

		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			t.Errorf("%s: %s carries tabindex=%q, which is not an integer; three "+
				"browsers would disagree about the tab order (UI §10.2, §7.2)",
				rendered.where, describe(node), raw)

			return
		}

		if value > -1 {
			t.Errorf("%s: %s carries tabindex=%d; only -1 is permitted. 0 moves the "+
				"element to the front of the tab order while the markup still reads "+
				"in document order (UI §10.2, §7.4)",
				rendered.where, describe(node), value)
		}
	})
}

// assertNoInlineOutlineSuppression is §10.2's "no `outline: none` without a
// replacement", over the half of it a document can carry.
//
// **The other half is the stylesheet's, and it is not this file's.** A rule in a
// `.css` file that removes a focus indicator is invisible to a DOM, which is why
// `internal/web`'s `TestNoRuleRemovesAFocusIndicatorWithoutAReplacement` reads the
// *built* stylesheet. Run that half from here and it would be a second copy of the
// same rule with a different fixture.
//
// What a route can violate is the **inline** half: a `style` attribute a template
// interpolated, which overrides the sheet for one element and is invisible to every
// stylesheet-level gate.
//
// It is not hypothetical here, and the reason is this route's own subject: a search
// document interpolates a **caller-supplied query** into the field's `value`, and a
// template that reached for the element's inline style to mark it current would put
// a caller-controlled string into a `style` attribute. Reading the attribute as a
// list of *declarations* rather than as a substring is what makes the rule survive
// `outline-offset` and `outline-colour`, which suppress nothing.
func assertNoInlineOutlineSuppression(t auditFailer, rendered doc) {
	t.Helper()

	rendered.elements(func(node *html.Node) {
		style := attribute(node, "style")
		if style == "" {
			return
		}

		for declaration := range strings.SplitSeq(style, ";") {
			property, value, split := strings.Cut(declaration, ":")
			if !split {
				continue
			}

			property = strings.ToLower(strings.TrimSpace(property))
			if property != "outline" {
				continue
			}

			switch strings.ToLower(strings.TrimSpace(value)) {
			case "none", "0":
				t.Errorf("%s: %s carries an inline `outline: %s`; §10.2 and §7.10 "+
					"prohibit removing a focus indicator without a visible replacement, "+
					"and this stylesheet's replacement is the two-tone ring rather than "+
					"a suppression. Nothing on this element puts one back",
					rendered.where, describe(node), strings.TrimSpace(value))
			default:
			}
		}
	})
}

// assertNoAriaHiddenOnAFocusStop is §10.2's `aria-hidden` rule and §7.10's, asserted
// on the *combination* rather than the attribute.
//
// Not on the attribute: the legitimate case in this shell is an icon inside a
// focusable link — an `<svg aria-hidden="true">` within an `<a href>`. The attribute
// alone is therefore not the finding; the attribute on something a keyboard can
// reach is.
//
// Two cases, and the second catches the real bugs: a focusable element *inside* an
// `aria-hidden` subtree is just as unreachable, and the offending attribute is on an
// ancestor several levels up.
func assertNoAriaHiddenOnAFocusStop(t auditFailer, rendered doc) {
	t.Helper()

	rendered.elements(func(node *html.Node) {
		if attribute(node, "aria-hidden") != "true" {
			return
		}

		if isFocusable(node) {
			t.Errorf("%s: %s is focusable and aria-hidden; a control a screen reader "+
				"cannot see is one that reader cannot reach (UI §10.2, §7.10)",
				rendered.where, describe(node))
		}

		if containsFocusStop(node) {
			t.Errorf("%s: %s is aria-hidden and contains focusable elements, so "+
				"everything inside it is unreachable to a screen reader (UI §7.10)",
				rendered.where, describe(node))
		}

		for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
			if ancestor.Type == html.ElementNode &&
				attribute(ancestor, "aria-hidden") == "true" && containsFocusStop(ancestor) {
				t.Errorf("%s: %s is inside the aria-hidden subtree at %s, which contains "+
					"focusable elements; everything focusable inside it is unreachable "+
					"to a screen reader (UI §7.10)",
					rendered.where, describe(node), describe(ancestor))

				return
			}
		}
	})
}

// assertSkipLinksComeFirstAndResolve is §10.2's "skip links first in tab order" and
// §7.2's own list, in three parts.
//
// Position — the links must be the first focus stops in the document. A skip link
// after the banner is not a skip link, it is a link named "skip".
//
// Resolution — each `href="#id"` must resolve, and to a focusable landmark. A skip
// link to a landmark that is not in the document moves focus nowhere, which costs a
// reader a keypress and teaches them the skip links are unreliable.
//
// Order — §7.2's sequence, with the campaign-navigation link only where the
// navigation exists. §4.6 removes the nav before a campaign does and §7.2 puts the
// link "only where the nav exists", so the link and the landmark are one condition
// expressed in two places. Both directions are asserted because "a link with no
// landmark" and "a landmark with no link" are both failures and only one of them is
// visible in a rendering.
//
// **On this route the check is sharper than on the wiki route's.** A search
// document's focus order is not only the shell's: the refine form's field and
// submit button come first among the route's own controls, and §4.7 gives the *idle*
// state a field that is focused on arrival. A skip link that slipped behind either
// would put the reader's first Tab into a search field, which is the exact thing a
// skip link exists to prevent.
func assertSkipLinksComeFirstAndResolve(t auditFailer, rendered doc) {
	t.Helper()

	links := skipLinks(rendered)
	if len(links) == 0 {
		t.Errorf("%s: the document has no skip link; §7.2 makes them the first "+
			"focusable elements and this shell always has at least one", rendered.where)

		return
	}

	focusStops := rendered.focusable()

	for index, link := range links {
		if index >= len(focusStops) || focusStops[index] != link.node {
			t.Errorf("%s: skip link %d (%q) is not focus stop %d; §7.2 puts the skip "+
				"links first in tab order, before the banner",
				rendered.where, index+1, link.text, index+1)

			continue
		}

		fragment, isFragment := strings.CutPrefix(link.href, "#")
		if !isFragment || fragment == "" {
			t.Errorf("%s: skip link %q has href %q; §7.2's links are fragments",
				rendered.where, link.text, link.href)

			continue
		}

		target := byID(rendered, fragment)
		if target == nil {
			t.Errorf("%s: skip link %q points at #%s, which is not in the document; "+
				"§4.6 removes some landmarks per route, so the link has to go with them",
				rendered.where, link.text, fragment)

			continue
		}

		if !hasAttribute(target, "tabindex") {
			t.Errorf("%s: skip link %q lands on %s, which has no tabindex; without one "+
				"the target is not focusable and focus lands at the top of a scroll "+
				"container instead of on an announced element (UI §7.2)",
				rendered.where, link.text, describe(target))
		}

		if roleOf(target) == "" {
			t.Errorf("%s: skip link %q lands on %s, which is not a landmark; a skip "+
				"link's target is the region it names (UI §7.2)",
				rendered.where, link.text, describe(target))
		}
	}

	contentAt, navigationAt := -1, -1

	for index, link := range links {
		switch {
		case strings.Contains(link.text, "content"):
			contentAt = index
		case strings.Contains(link.text, "navigation"):
			navigationAt = index
		case strings.Contains(link.text, "utilities"):
		default:
		}
	}

	if contentAt != 0 {
		t.Errorf("%s: the first skip link is %q; §7.2's order is content first, then "+
			"campaign navigation where the nav exists, then utilities",
			rendered.where, firstSkipLinkText(links))
	}

	if navigationAt >= 0 && navigationAt < contentAt {
		t.Errorf("%s: the campaign-navigation skip link precedes the content link; "+
			"§7.2's order is content first", rendered.where)
	}

	hasNavLandmark := byID(rendered, "nav") != nil
	hasNavLink := navigationAt >= 0

	if hasNavLandmark && !hasNavLink {
		t.Errorf("%s: the document has a navigation landmark but no skip link to it; "+
			"§7.2 lists %q -> #nav wherever the nav exists",
			rendered.where, "Skip to campaign navigation")
	}

	if hasNavLink && !hasNavLandmark {
		t.Errorf("%s: the document has a skip link to #nav but no navigation landmark; "+
			"§4.6 removes the nav before a campaign exists and a skip link to an "+
			"absent landmark moves focus nowhere", rendered.where)
	}
}

// firstSkipLinkText names the first skip link for a failure message.
func firstSkipLinkText(links []skipLink) string {
	if len(links) == 0 {
		return "(none)"
	}

	return links[0].text
}

// searchState is one of this route's four documents and the hooks its template
// promises.
//
// Named rather than a bare list of hooks because a missing-hook failure has to say
// *which* state it half-rendered, and a list of hooks cannot.
type searchState struct {
	name  string
	hooks []string
}

// searchStates are the four documents this route renders, by state.
//
// **The route's four documents are mutually exclusive**, and that is what makes the
// coverage half of the rule possible: a result list and a §4.7 idle state in one
// document would render two things at once, and none of them would render the page.
// Asserting the *pair* is what makes the well-formedness half mean something — a
// route that stopped rendering its own centre slot would otherwise satisfy "every
// hook present" by rendering none, because `TestEveryTestHookAppearsExactlyOnce`
// counts duplicates and finds nothing to count.
//
// The hook lists are read off `results.templ` and `ui/states.templ`, and the two
// that matter most for ARIA are here rather than inferred: `search-results` and
// `error-state` are the ids the route's own `aria-labelledby` attributes point at.
var searchStates = []searchState{
	{
		name: "search.SearchResults",
		hooks: []string{
			"search-results", "search-result-count", "search-result-query",
			"search-result-list", "search-refine", "search-refine-query",
			"search-refine-hint", "search-refine-submit",
		},
	},
	{
		name: "search.IdlePage",
		hooks: []string{
			"state-search-idle", "search-idle-query", "search-idle-hint",
			"search-idle-submit",
		},
	},
	{
		name: "search.EmptyPage",
		hooks: []string{
			"state-search-no-results",
			"search-no-results-term",
			"search-no-results-query",
			"search-no-results-hint",
			"search-no-results-submit",
		},
	},
	{
		name:  "search.FailurePage",
		hooks: []string{"search-failure-heading", "error-state", "error-state-message"},
	},
}

// chromeHooks are the hooks the shell promises on every document this route
// composes, given the reader's signed-in state.
//
// Per state rather than one flat list, for the reason `routeDocument.signedIn`
// gives: `chrome.headerAccount`'s own comment says the zone is "absent entirely for
// a reader who is not signed in", so `header-account` and `header-sign-out` are
// absent from the anonymous document and a flat list would call their absence a
// §10.2 failure. This is the same shape as `assertSkipLinksComeFirstAndResolve`'s
// navigation pairing — a per-route difference the shell states, which a generic
// audit has to be told about rather than assume.
func chromeHooks(signedIn bool) []string {
	hooks := []string{
		"shell", "shell-header", "header-home", "header-theme", "shell-main",
		"shell-rail", "rail-panels", "shell-footer", "footer-version",
		"skip-to-content", "skip-to-utilities",
	}

	if signedIn {
		return append(hooks, "header-account", "header-sign-out")
	}

	return hooks
}

// assertEveryTestIDIsPresent is §10.2's "every `data-testid` present", in two halves.
//
// The well-formedness half: a hook must have a non-empty value and appear once. A
// hook that appears twice selects nothing, and an *absent* hook is not an empty one —
// counting the empty string would report every unhooked element as a duplicate of
// every other, which is how a first draft of this reported fifteen duplicates on a
// correct shell.
//
// The coverage half: the hooks **this route's view model promises**, by state. The
// route's own `TestEveryTestHookAppearsExactlyOnce` holds the well-formedness half
// and nothing else; the coverage half is here because it is the half that can tell a
// route which template it stopped rendering.
func assertEveryTestIDIsPresent(t auditFailer, rendered doc, signedIn bool) {
	t.Helper()

	counts := map[string]int{}

	rendered.elements(func(node *html.Node) {
		// `hasAttribute`, not a non-empty value: an element carrying an *empty*
		// `data-testid` is a finding, and reading the value would skip it.
		if !hasAttribute(node, "data-testid") {
			return
		}

		counts[attribute(node, "data-testid")]++
	})

	duplicates := make([]string, 0, len(counts))

	for hook, count := range counts {
		switch {
		case count > 1:
			duplicates = append(duplicates, hook+" x"+strconv.Itoa(count))
		case strings.TrimSpace(hook) == "":
			t.Errorf("%s: an element carries an empty data-testid; a hook with no "+
				"value selects nothing and cannot be asserted on (UI §10.2)", rendered.where)
		default:
		}
	}

	if len(duplicates) > 0 {
		sortStrings(duplicates)

		t.Errorf("%s: these data-testid hooks appear more than once: %s; a hook that "+
			"appears twice selects nothing (UI §10.2)",
			rendered.where, strings.Join(duplicates, ", "))
	}

	// The hooks the shell promises on this route's documents.
	for _, required := range chromeHooks(signedIn) {
		if counts[required] == 0 {
			t.Errorf("%s: the document carries no %q hook; components.Shell promises "+
				"it on every document this route composes (UI §10.2)",
				rendered.where, required)
		}
	}

	// Exactly one state, rendered whole. "Whole" is the load-bearing word: a state
	// rendered half-way is the failure a count cannot see, and it is the shape the
	// natural "hoist the form out of the state and into the route" refactor takes.
	//
	// A state whose hooks are **all** absent has not been half-rendered — the
	// document is simply in a different one of the four — so it is neither a
	// complete state nor a partial one. That is why the count is of *complete*
	// states and the half-rendered report is per missing hook rather than per
	// absent set.
	complete := 0

	for _, state := range searchStates {
		if presentHooks(counts, state.hooks) == len(state.hooks) {
			complete++

			continue
		}

		if presentHooks(counts, state.hooks) == 0 {
			continue
		}

		for _, hook := range state.hooks {
			if counts[hook] > 0 {
				continue
			}

			t.Errorf("%s: the document carries no %q hook; %s renders it on every "+
				"document of that state, so a state rendered half-way is neither "+
				"state nor a document (UI §10.2)",
				rendered.where, hook, state.name)
		}
	}

	if complete != 1 {
		t.Errorf("%s: the document renders %d complete states, want exactly 1; "+
			"results.templ mounts one of %s, and two of them at once renders two "+
			"different things at the same time (UI §10.2)",
			rendered.where, complete, stateNames())
	}
}

// presentHooks returns how many of a state's hooks the document carries.
func presentHooks(counts map[string]int, hooks []string) int {
	present := 0

	for _, hook := range hooks {
		if counts[hook] > 0 {
			present++
		}
	}

	return present
}

// stateNames names every state this route renders, for a failure message.
func stateNames() []string {
	names := make([]string, 0, len(searchStates))
	for _, state := range searchStates {
		names = append(names, state.name)
	}

	return names
}

// assertNoRetiredEntityIsNamed is UI §1.2 and §10.2's last clause: no rendered string
// contains "world" or "session".
//
// The scan covers **every text node, every comment, every attribute value and every
// attribute name**, and each inclusion has a reason.
//
// Comments: a word in a comment is invisible to a reader, which is exactly why an
// audit reading only text nodes would pass on a document whose markup still names a
// retired entity — and the comment is the one place a renderer is tempted to leave
// one, because nobody sees it. This shell renders two inline scripts, so the comment
// half is not hypothetical.
//
// Attribute values: an `aria-label` or a `title` is announced to a reader, so a
// retired entity named there is named to the person the rule protects.
//
// Attribute names: a `data-world` is invisible to a reader and obvious to a developer
// grepping for the feature it implies, which makes it exactly as retired as a label.
//
// **The rule is scoped by what this route *authors*.** A result title is campaign
// content: the words are absent from semiplane's own copy, and a GM whose page is
// called "The World Map" will see their own title on their own screen and no audit
// changes that. So the fixtures carry titles free of the words and this polices the
// route's copy rather than the campaign's — which is the same scoping the route's
// own `TestNoRetiredEntityIsNamedAnywhere` states, and is why the two are the same
// rule rather than two answers.
//
// The words themselves are the package's `retiredEntities`, read from one place
// rather than restated: a second list is a list that can drift from UI §1.2.
func assertNoRetiredEntityIsNamed(t auditFailer, rendered doc) {
	t.Helper()

	report := func(place, text string) {
		lowered := strings.ToLower(text)

		for _, retired := range retiredEntities {
			if strings.Contains(lowered, retired) {
				t.Errorf("%s: %s contains %q; neither entity exists, and §1.2 makes the "+
					"words appear nowhere in the interface", rendered.where, place, retired)
			}
		}
	}

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		switch node.Type {
		case html.TextNode:
			report("a text node", node.Data)
		case html.CommentNode:
			report("an HTML comment", node.Data)
		case html.ElementNode:
			for _, attr := range node.Attr {
				report("the attribute "+attr.Key, attr.Val)
				report("an attribute name", attr.Key)
			}
		case html.DoctypeNode:
		default:
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(rendered.root)
}

// assertEveryReferenceResolves is the ARIA integrity rule §10.2's list implies and
// does not spell out: a document whose `aria-controls` points at nothing is a control
// claiming to control something invisible.
//
// All five reference attributes, plus `<label for>` and `href="#…"`. The id
// uniqueness half is not incidental: two elements sharing an id make every reference
// to it ambiguous, and a screen reader resolves the ambiguity by picking the first —
// so a control can end up controlling the wrong element with nothing in the markup to
// say so.
//
// **This is the rule that matters most on this route**, and it is the one the
// package's thirty-three tests do not have. Every one of this route's four documents
// is a form with at least three references in it, and each one is written by a
// different file:
//
//   - `<section aria-labelledby="search-results-heading">` over an `<h1>` the route
//     writes — `results.templ`;
//   - `<section aria-labelledby="state-search-idle-heading">` over a heading id that
//     `ui.headingID` *derives* from the test hook, so a rename of either end breaks
//     the reference without breaking compilation — `ui/states.templ`;
//   - `<label for="search-refine-query">` and `aria-describedby="search-refine-hint"`
//     on the refine field, whose ids are written out in `results.templ` and not
//     derived from anything.
//
// A field whose `<label for>` resolves to nothing is a field with no accessible name
// at all, and §7.7 opens on exactly that.
func assertEveryReferenceResolves(t auditFailer, rendered doc) {
	t.Helper()

	ids := map[string]int{}

	rendered.elements(func(node *html.Node) {
		if id := attribute(node, "id"); id != "" {
			ids[id]++
		}
	})

	for id, count := range ids {
		if count > 1 {
			t.Errorf("%s: id %q appears %d times; every reference to it is ambiguous "+
				"and a screen reader resolves the ambiguity by picking the first (UI §7.2)",
				rendered.where, id, count)
		}
	}

	references := []string{
		"aria-controls",
		"aria-labelledby",
		"aria-describedby",
		"aria-owns",
		"aria-activedescendant",
	}

	rendered.elements(func(node *html.Node) {
		for _, name := range references {
			value := attribute(node, name)
			if value == "" {
				continue
			}

			for id := range strings.FieldsSeq(value) {
				if byID(rendered, id) == nil {
					t.Errorf("%s: %s carries %s=%q, which resolves to no element; a "+
						"control that claims to control something invisible is a control "+
						"that lies (UI §7.2)", rendered.where, describe(node), name, id)
				}
			}
		}

		// §7.7: a real `<label for>`, never a placeholder. A `for` pointing at nothing
		// is a field with no accessible name.
		if node.Data == "label" {
			if target := attribute(node, "for"); target != "" && byID(rendered, target) == nil {
				t.Errorf("%s: %s has for=%q, which resolves to no element; the field has "+
					"no accessible name (UI §7.7)", rendered.where, describe(node), target)
			}
		}

		if node.Data == "a" {
			fragment, isFragment := strings.CutPrefix(attribute(node, "href"), "#")
			if isFragment && fragment != "" && byID(rendered, fragment) == nil {
				t.Errorf("%s: %s has href=\"#%s\", which resolves to no element",
					rendered.where, describe(node), fragment)
			}
		}
	})
}

// --- §10.6 -----------------------------------------------------------------------

// TestEveryRouteCarriesTheTargetClassOnEveryFocusStop is UI §10.6 and §7.3's "every
// interactive element … uses it", over every document this route composes.
//
// Its own top-level test rather than an eleventh subtest of
// `TestEveryRouteSatisfiesTheStructuralContract`, because §10.6 is a different
// section of the record with its own floor, and a rule that fails is easier to route
// to a person when the failure line names the section it came from.
//
// The class is on the element and the minimum is in the stylesheet, which is why this
// is a structural rule and not a measurement: the markup says what the element is
// and `internal/web`'s `TestTheTargetMinimumsMeetTheSpecifiedFloors` asserts the
// sheet's side from the **built** stylesheet.
func TestEveryRouteCarriesTheTargetClassOnEveryFocusStop(t *testing.T) {
	t.Parallel()

	for _, subject := range auditedRoute(t) {
		t.Run(subject.rendered.where, func(t *testing.T) {
			t.Parallel()

			assertEveryFocusStopCarriesTarget(t, subject.rendered)
		})
	}
}

// assertEveryFocusStopCarriesTarget is §10.6's "grep rendered markup for interactive
// elements missing the class".
//
// A **per-element** walk rather than a count, because a count cannot name the element
// and a failure message that does not name it is a failure a reader has to go and
// re-derive.
//
// The element set is the harness's own `doc.focusable` and its `isFocusable`, which
// is §10.6's set plus `tabindex` and minus `disabled` — so the rule and the route's
// own `TestEveryControlCarriesTheTargetClass` judge the same list, and the
// duplicated part below is a loop rather than an opinion about what is interactive.
//
// The message also says **where the element came from**, which is the difference
// between a finding somebody can act on and one they can only forward. A focus stop
// the shell wrote is `internal/web`'s; one inside `section[data-testid=search-results]`
// is the route's; one inside a `data-path` row is a result link from
// `results.templ`. Three owners, three fixes, and a message that does not
// distinguish them sends the reader to the wrong package.
func assertEveryFocusStopCarriesTarget(t auditFailer, rendered doc) {
	t.Helper()

	for _, node := range rendered.focusable() {
		if hasClassToken(node, "target") {
			continue
		}

		t.Errorf("%s: %s is focusable and does not carry the .target class; §7.3 "+
			"enforces the --target-min minimum by construction and §10.6 audits for it "+
			"including plugin output. %s", rendered.where, describe(node), targetOwner(node))
	}
}

// targetOwner names the package that would have to change for a focus stop to carry
// the class.
//
// **Innermost first, and that ordering is load-bearing rather than cosmetic.** The
// refine form is *inside* `section[data-testid=search-results]`, so a switch written
// outside-in makes the refine branch unreachable — a case no document can reach,
// which is the same dead-branch mistake as an audit branch no fixture reaches, and
// the reason the two hooks are checked in this order rather than the order they
// appear in the markup. Innermost-first is also the more useful answer: a control on
// the refine *form* and a control on the result list's *section* are different edits
// in the same file, and naming the outer one for both sends a reader to the wrong
// half of it.
func targetOwner(node *html.Node) string {
	switch {
	case hasAncestorWithHook(node, "search-refine"):
		return "It is inside form[data-testid=search-refine], so it is the route's own " +
			"refine field and the fix is in internal/httpapi/search"
	case hasAncestorWithHook(node, "search-results"):
		return "It is inside section[data-testid=search-results], so it is a control " +
			"results.templ wrote and the fix is in internal/httpapi/search"
	case hasAncestorAttributePrefix(node, "data-path"):
		return "It is inside a result row, so it is the result link results.templ writes " +
			"and the fix is in internal/httpapi/search"
	default:
		return "The shell wrote it, so the fix is in internal/web/components"
	}
}

// hasAncestorWithHook reports whether any ancestor carries the given test hook.
//
// By hook rather than by a class: `data-testid` is the contract the templates state,
// and a rule keyed on it keeps working when the styling class is renamed.
func hasAncestorWithHook(node *html.Node, hook string) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && attribute(ancestor, "data-testid") == hook {
			return true
		}
	}

	return false
}

// hasAncestorAttributePrefix reports whether any ancestor carries an attribute whose
// name starts with prefix.
//
// Read from the attribute's *name* rather than from a known value, so a row that
// carried a different path is still recognised as a row. The rows are located by
// `data-path` — `pages_campaign_path_key` makes it unique within a campaign — and by
// nothing else, which is why `results.templ` puts no hook on them.
func hasAncestorAttributePrefix(node *html.Node, prefix string) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type != html.ElementNode {
			continue
		}

		for _, attr := range ancestor.Attr {
			if strings.HasPrefix(attr.Key, prefix) {
				return true
			}
		}
	}

	return false
}

// --- The responses that are not documents ----------------------------------------

// TestEveryRouteRefusesAnUnknownCampaignWithANonDocument is ADR 0024 from the audit's
// side, and the reason the document list stops where it does.
//
// A slug naming no campaign is refused by `campaigns.RequireRead` before this route's
// handler runs, and the gate's answer is `{"error":"not found"}` as JSON. It is
// deliberately byte-identical to the answer for a private campaign the reader is not
// a member of, so the refusal cannot become an existence oracle.
//
// The consequence for §10.2 is that the body is **not a document**, and every §10.2
// rule is a claim about a document's structure. Asserting that here rather than
// leaving it to inference is what stops the subject list from drifting: a future
// change that made this route answer an unknown campaign with a rendered shell would
// be a *good* change for §10.2 and a **security regression**, and this test is what
// says so.
//
// The package's own `TestTheRouteMountedWithoutItsGateRefusesRatherThanSearching`
// is the neighbouring claim — that a route reached without its gate searches
// nothing — and neither test can see the other: one is about the gate being absent,
// the other about the gate being present and refusing.
func TestEveryRouteRefusesAnUnknownCampaignWithANonDocument(t *testing.T) {
	t.Parallel()

	unknown := newHarness(t).withRows(testRows()...).
		get("/c/no-such-campaign/search?q=goblin", gmRequestor())

	privateCampaign := newPrivateHarness(t).get(searchURL("goblin"), anonymousRequestor())

	for _, testCase := range []struct {
		where    string
		recorder *httptest.ResponseRecorder
	}{
		{"a campaign nobody registered", unknown},
		{"a private campaign the reader is not a member of", privateCampaign},
	} {
		t.Run(testCase.where, func(t *testing.T) {
			t.Parallel()

			if testCase.recorder.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", testCase.recorder.Code)
			}

			if got := testCase.recorder.Header().Get("Content-Type"); got !=
				"application/json; charset=utf-8" {
				t.Errorf("Content-Type = %q, want the gate's JSON; a rendered shell "+
					"here would still have to be byte-identical to the private "+
					"campaign's, and a difference between two refusals is an existence "+
					"oracle (ADR 0024)", got)
			}

			if strings.Contains(strings.ToLower(testCase.recorder.Body.String()), "<html") {
				t.Errorf("the gate's refusal is an HTML document, so §10.2's rules would "+
					"apply to it and this file's subject list is incomplete:\n%s",
					firstBytes(testCase.recorder.Body.String()))
			}

			if got := testCase.recorder.Body.String(); !strings.Contains(got, "not found") {
				t.Errorf("the refusal body is %q, want the gate's message; two refusals "+
					"that differ are an existence oracle whatever their status (ADR 0024)",
					excerpt(got))
			}
		})
	}
}

// newPrivateHarness builds the harness's route over a **private** campaign.
//
// The harness has a `visibility` field and no `private()` setter, because nothing in
// the package needed one until now: the visibility assertions live in
// `visibility_test.go` against a real store, and the 404-oracle case is the only
// thing the fake can express. Written here rather than added to `harness_test.go` so
// this work item touches one file.
func newPrivateHarness(t *testing.T) *harness {
	t.Helper()

	harness := newHarness(t)
	harness.visibility = domain.VisibilityPrivate

	return harness
}

// --- The mutation log -----------------------------------------------------------

// Fifteen mutations were applied to `results.templ` and to this file's own fixtures,
// and each was caught, in the order below.
//
// The record is here rather than in a scratch document because a mutation log nobody
// reads is a comment about diligence, and this one is read by whoever changes the
// route next and wants to know which assertions are load-bearing. Each row names the
// subtest that fired, because "the a11y test failed" is not an answer.
//
//	 1. `results.templ`: the route's own `<h1>` written above the result list
//	     → SatisfiesTheStructuralContract/results/ExactlyOneH1.
//	 2. `results.templ`: the result count demoted from `<h1>` to `<h2>`, opening and
//	     closing tag together — templ's parser rejects an unbalanced document, so the
//	     first attempt at this row never generated at all, and the pass it recorded
//	     was the mutation failing rather than the audit passing.
//	     → results/ExactlyOneH1.
//	 3. `results.templ`: the refine form's `aria-label` shortened to "Search"
//	     → results/LandmarksArePresentAndDistinguishing.
//	 4. `results.templ`: `aria-describedby` on the refine field pointed at an id that
//	     is not in the document
//	     → results/EveryReferenceResolves.
//	 5. `results.templ`: the result list's `<section aria-labelledby>` pointed at an
//	     id that is not in the document
//	     → results/EveryReferenceResolves.
//	 6. `results.templ`: the refine field's `<label for>` renamed
//	     → results/EveryReferenceResolves.
//	 7. `results.templ`: `tabindex="0"` interpolated onto the refine field
//	     → results/NoPositiveTabindex.
//	 8. `results.templ`: `.target` dropped from the refine submit button
//	     → CarriesTheTargetClassOnEveryFocusStop.
//	 9. `results.templ`: `style="outline: none"` on the result list's `<section>`
//	     → results/NoInlineOutlineSuppression.
//	10. `results.templ`: `aria-hidden="true"` on the refine form
//	     → results/NoAriaHiddenOnAFocusStop.
//	11. `results.templ`: the `search-result-list` hook removed from the `<ol>`
//	     → results/EveryTestIDIsPresent, "a state rendered half-way".
//	12. `results.templ`: the same hook put on every `<li>`
//	     → results/EveryTestIDIsPresent, "appear more than once".
//	13. `results.templ`: `<!-- the world of this account -->` added above the count
//	     → results/NoRetiredEntityIsNamed.
//	14. `results.templ`: `title="Your session"` on the refine form
//	     → results/NoRetiredEntityIsNamed.
//	15. `results.templ`: `data-world="1"` on the result list's `<section>`
//	     → results/NoRetiredEntityIsNamed, "an attribute name".
//
// **Two of the fifteen did not work on the first attempt, and both were failures of
// the mutation rather than of the assertion.** Row 2 never generated (above), and the
// first attempt at row 4 *removed* the `aria-describedby` rather than renaming its
// value — which passes, because §10.2's rule is about a reference that resolves to
// nothing and not about a hint a field does not carry. The second is the same shape
// as this package's own mutation log's note about an escape applied to a component
// whose text another package already bounds: an assertion that passes because the
// thing it watches is not the thing on the page. Both are recorded because that is
// the mistake this discipline exists to catch.
//
// The negative controls below are themselves mutations of `correctDocument`, and
// `mutate` asserts its needle is present so that a stale fixture is a test failure
// rather than a silent pass — the third version of the same mistake, and the one that
// would have been easiest to ship.

// --- The audit's own tests -------------------------------------------------------

// TestEveryRouteAuditRejectsTheViolationItClaimsTo is the negative control: each
// §10.2 rule is run over a minimal document built to violate exactly that rule, and
// each must produce at least one finding **that mentions the thing it is about**.
//
// **This is the test that decides whether the other ones are a gate.** A rule that
// cannot fail is a rule that is not there, and phase 5 found three gate tests that
// could not — which is why the assertion is not "the audit ran" but "the audit
// objected, and its objection names this rule".
//
// The message check is what makes a rule's *identity* testable rather than its
// existence. A landmark audit that also reported duplicate hooks would pass a
// fixture built to trip the landmark rule, and the gate would look covered while the
// thing it names was not being checked. Only one rule runs per row, so the fixtures
// do not have to satisfy the other nine — but they are kept minimal anyway, because
// a fixture that trips four rules and asserts one of them is a fixture whose failure
// is ambiguous when a rule starts reporting twice.
//
// **The search-specific rows are not decoration.** Four of the ten rules had no
// negative control anywhere in the package before this file, because the package's
// audits for them are top-level `*testing.T` functions that cannot be run against a
// fixture; a rule with no way to be shown failing is exactly the shape the
// `A11Y_ROUTE_PKGS` guard was written to prevent, one level down.
func TestEveryRouteAuditRejectsTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		body    string
		check   func(auditFailer, doc)
		wantOne string
	}{
		{
			name: "a second h1",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><h1>Also page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertTheDocumentHasExactlyOneH1(t, rendered)
			},
			wantOne: "<h1> elements, want exactly 1",
		},
		{
			name: "no h1 at all",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><p>Page.</p></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertTheDocumentHasExactlyOneH1(t, rendered)
			},
			wantOne: "0 <h1> elements",
		},
		{
			name: "a heading that skips a level",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><h3>Sub</h3></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertHeadingLevelsNeverSkip(t, rendered)
			},
			wantOne: "jumps from h1 to h3",
		},
		{
			// The route-shaped half of the rule: `shell.templ` renders `<main>` before
			// `<aside>`, so a rail panel that nests its own heading a level deeper than
			// the rail's `<h2>` skips from the reader's point of view even though every
			// component is individually correct. There is no author markup on this route
			// to make a heading skip — the wiki route's case — so the rail is where the
			// rule has subjects here.
			name: "a rail panel heading a level too deep",
			body: `<html><head><title>t</title></head><body><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main><aside id="rail" role="complementary" aria-label="Utilities" tabindex="-1" class="target"><h2>Instance</h2><h4>Subsystem</h4></aside></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertHeadingLevelsNeverSkip(t, rendered)
			},
			wantOne: "jumps from h2 to h4",
		},
		{
			name: "no main landmark",
			body: `<html><head><title>t</title></head><body><h1>Page</h1></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertLandmarksArePresentAndDistinguishing(t, rendered)
			},
			wantOne: "no main landmark",
		},
		{
			name: "two main landmarks",
			body: `<html><head><title>t</title></head><body><main id="one" role="main" class="target"><h1>Page</h1></main><main id="two" role="main" class="target"><p>More.</p></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertLandmarksArePresentAndDistinguishing(t, rendered)
			},
			wantOne: "want 1",
		},
		{
			name: "an unnamed search landmark",
			body: `<html><head><title>t</title></head><body><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1><form role="search"><input id="q" class="target" type="search"/></form></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertLandmarksArePresentAndDistinguishing(t, rendered)
			},
			wantOne: "the search landmark",
		},
		{
			name: "a complementary landmark mislabelled",
			body: `<html><head><title>t</title></head><body><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main><aside id="rail" role="complementary" aria-label="Extras" tabindex="-1" class="target"></aside></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertLandmarksArePresentAndDistinguishing(t, rendered)
			},
			wantOne: "the complementary landmark is labelled",
		},
		{
			name: "two search landmarks with one name",
			body: `<html><head><title>t</title></head><body><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1><form role="search" aria-label="Search this campaign"></form><form role="search" aria-label="Search this campaign"></form></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertLandmarksArePresentAndDistinguishing(t, rendered)
			},
			wantOne: "are both labelled",
		},
		{
			// The `allowedNavigation` branch. Unreachable on this route's documents,
			// because the pre-campaign shell renders no navigation landmark at all —
			// which is exactly why it needs a fixture: a branch no document reaches is
			// a branch nobody has run.
			name: "a navigation landmark mislabelled",
			body: `<html><head><title>t</title></head><body><a class="skip-link target" href="#main">Skip to content</a><a class="skip-link target" href="#nav">Skip to campaign navigation</a><nav id="nav" role="navigation" aria-label="Places" tabindex="-1" class="target"></nav><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertLandmarksArePresentAndDistinguishing(t, rendered)
			},
			wantOne: "a navigation landmark is labelled",
		},
		{
			// The `allowedSearch` branch, and the reason the header's "Search pages" is
			// in the allowed set: a search landmark carrying a third name is a finding
			// either way, and only the pair that §7.2 fixes is not.
			name: "a search landmark labelled something else",
			body: `<html><head><title>t</title></head><body><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1><form role="search" aria-label="Search"><input id="q" class="target" type="search"/></form></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertLandmarksArePresentAndDistinguishing(t, rendered)
			},
			wantOne: "a search landmark is labelled",
		},
		{
			name: "a contentinfo inside main",
			body: `<html><head><title>t</title></head><body><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1><footer role="contentinfo"></footer></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertLandmarksArePresentAndDistinguishing(t, rendered)
			},
			wantOne: "inside <main>",
		},
		{
			name: "a positive tabindex",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="3" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoPositiveTabindex(t, rendered)
			},
			wantOne: "only -1 is permitted",
		},
		{
			// Signed and padded, which is what a value interpolated from a variable
			// looks like. `strconv.Atoi(strings.TrimSpace(" +1 "))` reads it as 1; a
			// string comparison against "0" does not, because "+1" sorts before "0".
			// The row exists so the numeric parse is load-bearing rather than a
			// stylistic preference — the failure it prevents is a browser disagreeing
			// with this gate about the tab order.
			name: "a tabindex with a sign and padding",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex=" +1 " class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoPositiveTabindex(t, rendered)
			},
			wantOne: "tabindex=1",
		},
		{
			// A zero tabindex is also a finding, and it is the one a string comparison
			// against "0" gets wrong in the other direction.
			name: "a zero tabindex",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="0" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoPositiveTabindex(t, rendered)
			},
			wantOne: "only -1 is permitted",
		},
		{
			name: "a tabindex that is not an integer",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="first" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoPositiveTabindex(t, rendered)
			},
			wantOne: "which is not an integer",
		},
		{
			name: "an inline outline suppression",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1 style="outline: none">Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoInlineOutlineSuppression(t, rendered)
			},
			wantOne: "outline: none",
		},
		{
			name: "an inline outline of zero",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1 style="outline : 0">Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoInlineOutlineSuppression(t, rendered)
			},
			wantOne: "outline: 0",
		},
		{
			name: "a focusable element that is aria-hidden",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><a href="/a" aria-hidden="true">A</a></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoAriaHiddenOnAFocusStop(t, rendered)
			},
			wantOne: "is focusable and aria-hidden",
		},
		{
			// The case that catches real bugs: the offending attribute is on an
			// ancestor, and a rule that only read the attribute on the element would
			// pass it.
			name: "a focusable element inside an aria-hidden subtree",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><div aria-hidden="true"><a href="/a">A</a></div></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoAriaHiddenOnAFocusStop(t, rendered)
			},
			wantOne: "contains focusable elements",
		},
		{
			name: "no skip link at all",
			body: `<html><head><title>t</title></head><body><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertSkipLinksComeFirstAndResolve(t, rendered)
			},
			wantOne: "has no skip link",
		},
		{
			name: "a skip link after the banner",
			body: `<html><head><title>t</title></head><body><header role="banner"><a href="/a" class="target">A</a></header><a class="skip-link target" href="#main">Skip to content</a><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertSkipLinksComeFirstAndResolve(t, rendered)
			},
			wantOne: "skip links first in tab order",
		},
		{
			name: "a skip link to an absent landmark",
			body: `<html><head><title>t</title></head><body><a class="skip-link target" href="#nowhere">Skip to content</a><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertSkipLinksComeFirstAndResolve(t, rendered)
			},
			wantOne: "not in the document",
		},
		{
			// §4.6's pairing, and the direction a rendering cannot show: a navigation
			// landmark with no skip link to it.
			name: "a navigation landmark with no skip link",
			body: `<html><head><title>t</title></head><body><a class="skip-link target" href="#main">Skip to content</a><nav id="nav" role="navigation" aria-label="Campaign" tabindex="-1" class="target"></nav><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertSkipLinksComeFirstAndResolve(t, rendered)
			},
			wantOne: "no skip link to",
		},
		{
			// And the other direction, which is what a future change adding the nav to
			// this route's shell without the link would produce.
			name: "a skip link to a navigation landmark that is not there",
			body: `<html><head><title>t</title></head><body><a class="skip-link target" href="#main">Skip to content</a><a class="skip-link target" href="#nav">Skip to campaign navigation</a><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertSkipLinksComeFirstAndResolve(t, rendered)
			},
			wantOne: "no navigation landmark",
		},
		{
			// The branch no document reaches: §7.2's links are fragments, so a skip
			// link whose href is a path moves focus nowhere *and* is not a skip link.
			name: "a skip link that is not a fragment",
			body: `<html><head><title>t</title></head><body><a class="skip-link target" href="/c/greyhaven/wiki">Skip to content</a><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertSkipLinksComeFirstAndResolve(t, rendered)
			},
			wantOne: "§7.2's links are fragments",
		},
		{
			// The other half of "resolves": an id that is in the document but is not
			// the region the link names. This is what a skip link aimed at a wrapper
			// `<div>` looks like after a layout refactor.
			name: "a skip link to something that is not a landmark",
			body: `<html><head><title>t</title></head><body><a class="skip-link target" href="#wrapper">Skip to content</a><div id="wrapper" tabindex="-1" class="target"><main id="main" role="main" class="target"><h1>Page</h1></main></div></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertSkipLinksComeFirstAndResolve(t, rendered)
			},
			wantOne: "which is not a landmark",
		},
		{
			// §7.2's order is content first. The two links are both first in tab order,
			// so only the *order* rule can catch this — which is why it is a fixture of
			// its own rather than a variant of the row above.
			name: "the utilities skip link before the content one",
			body: `<html><head><title>t</title></head><body><a class="skip-link target" href="#rail">Skip to utilities</a><a class="skip-link target" href="#main">Skip to content</a><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main><aside id="rail" role="complementary" aria-label="Utilities" tabindex="-1" class="target"></aside></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertSkipLinksComeFirstAndResolve(t, rendered)
			},
			wantOne: "§7.2's order is content first",
		},
		{
			name: "a skip link whose target is not focusable",
			body: `<html><head><title>t</title></head><body><a class="skip-link target" href="#main">Skip to content</a><main id="main" role="main" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertSkipLinksComeFirstAndResolve(t, rendered)
			},
			wantOne: "has no tabindex",
		},
		{
			// The hook rows below all mutate `correctDocument` rather than being
			// minimal literals, because a literal fixture for the hook rule is missing
			// every other hook by construction: it would fire for twelve reasons and
			// the one under test would be indistinguishable from the rest. That is the
			// "an assertion that passes because the thing it watches is not the thing on
			// the page" shape, and it is the same one the package's own mutation log
			// records as its worst of three.
			name: "a repeated data-testid",
			body: mutate(t, `data-testid="search-refine-hint"`, `data-testid="search-result-count"`),
			check: func(t auditFailer, rendered doc) {
				assertEveryTestIDIsPresent(t, rendered, true)
			},
			wantOne: "more than once",
		},
		{
			// The half a count cannot reach, and the one the package's own
			// `TestEveryTestHookAppearsExactlyOnce` does not have: the hook is present
			// *once*, and it is still wrong. The harness's `attribute` helper returns ""
			// for both an absent and an empty attribute, so the rule reads presence
			// with `hasAttribute` and the value after it.
			name: "an empty data-testid",
			body: mutate(t, `data-testid="search-refine-hint"`, `data-testid=""`),
			check: func(t auditFailer, rendered doc) {
				assertEveryTestIDIsPresent(t, rendered, true)
			},
			wantOne: "empty data-testid",
		},
		{
			// A hook this route's own view model promises and did not render: the
			// result list, with every other hook of that state present. The hook is
			// replaced with a `data-` attribute, not removed, so the element survives
			// and the finding is a missing promise rather than a missing list.
			name: "a state rendered half-way",
			body: mutate(t, `data-testid="search-result-list"`, `data-shell-result-list`),
			check: func(t auditFailer, rendered doc) {
				assertEveryTestIDIsPresent(t, rendered, true)
			},
			wantOne: `no "search-result-list" hook`,
		},
		{
			// Two states in one document — what hoisting the refine form out of
			// `SearchResults` and into the route would produce. §7.2's rule for two
			// landmarks of one role is that they must not share a name, and the two
			// states' forms both say "Search this campaign", so that refactor breaks
			// the landmark rule too; the hook rule catches it first.
			name: "two states rendered at once",
			body: mutate(t, `</section></main>`,
				`<section data-testid="state-search-idle"><h1>Search</h1>`+
					`<input id="search-idle-query" data-testid="search-idle-query" type="search"/>`+
					`<p id="search-idle-hint" data-testid="search-idle-hint">Type a word.</p>`+
					`<button data-testid="search-idle-submit">Search</button></section></section></main>`),
			check: func(t auditFailer, rendered doc) {
				assertEveryTestIDIsPresent(t, rendered, true)
			},
			wantOne: "renders 2 complete states",
		},
		{
			// The direction §4.1 decides: the account zone is absent for a reader who
			// is not signed in, so on a document that *is* signed in its absence is a
			// missing promise. The same mutation is asserted the other way round in
			// `TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch`, which is what
			// stops the rule from being "the account hooks are always present" — the
			// mistake the wiki route's own navigation pairing is written against.
			name: "an account zone missing from a signed-in document",
			body: mutate(t, `data-testid="header-account"`, `data-shell-account`),
			check: func(t auditFailer, rendered doc) {
				assertEveryTestIDIsPresent(t, rendered, true)
			},
			wantOne: `no "header-account" hook`,
		},
		{
			name: "the word in a text node",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><p>the World of this account</p></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoRetiredEntityIsNamed(t, rendered)
			},
			wantOne: "a text node",
		},
		{
			// The case a rendered-document reader skips: a reader never sees a
			// comment, which is precisely why it must be scanned.
			name: "the word in an HTML comment",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><!-- the world of this account --></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoRetiredEntityIsNamed(t, rendered)
			},
			wantOne: "an HTML comment",
		},
		{
			name: "the word in an aria-label",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><nav aria-label="game sessions"></nav></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoRetiredEntityIsNamed(t, rendered)
			},
			wantOne: "the attribute aria-label",
		},
		{
			name: "the word in a title attribute",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><span title="Your session">x</span></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoRetiredEntityIsNamed(t, rendered)
			},
			wantOne: "the attribute title",
		},
		{
			// The nastiest one, because it is the one a scan over attribute *values*
			// would miss and a scan over the raw markup would find: the word is in the
			// *name* of an attribute nothing renders.
			name: "the word in an attribute name",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><div data-world="true"></div></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoRetiredEntityIsNamed(t, rendered)
			},
			wantOne: "an attribute name",
		},
		{
			// The failure a substring audit over the response body cannot see, and the
			// reason this rule walks the tree: the echoed query is a text node, so the
			// reader *does* see it — but a `strings.Contains(body, "world")` on a
			// document whose word is in an `aria-label` passes.
			name: "the word in the centre of the route's own copy",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><form role="search" aria-label="Search the world"><input class="target" id="q" type="search"/></form></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoRetiredEntityIsNamed(t, rendered)
			},
			wantOne: "the attribute aria-label",
		},
		{
			name: "a reference that resolves to nothing",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><button class="target" aria-controls="nowhere">Go</button></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryReferenceResolves(t, rendered)
			},
			wantOne: "resolves to no element",
		},
		{
			// The one this route's four forms are most likely to break, and the one
			// no test in the package had: `ui.headingID` *derives* the heading id from
			// the test hook, so renaming either end breaks the reference without
			// breaking compilation.
			name: "an aria-labelledby naming a heading that is not there",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><section aria-labelledby="state-search-idle-heading" data-testid="state-search-idle"><h1>Search</h1></section></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryReferenceResolves(t, rendered)
			},
			wantOne: "resolves to no element",
		},
		{
			// The branch that matters most on this route and the one a rename breaks
			// silently: `aria-describedby` is a hint id written out by hand in
			// `results.templ` and in `ui.searchForm`, and nothing derives it from
			// anything, so a field loses its hint without a compile error.
			name: "an aria-describedby naming a hint that is not there",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><input id="search-refine-query" class="target" type="search" aria-describedby="search-refine-hint"/></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryReferenceResolves(t, rendered)
			},
			wantOne: "aria-describedby",
		},
		{
			// The three reference attributes this route writes none of. They are in the
			// rule because §10.2's list implies the rule and the list does not spell it
			// out, and a branch no fixture reaches is a branch nobody has run — so all
			// three are here in one row rather than three near-identical ones.
			name: "the reference attributes this route does not write",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><div aria-owns="ghost"></div><input class="target" aria-activedescendant="row-1"/><a class="target" href="#nowhere">A</a></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryReferenceResolves(t, rendered)
			},
			wantOne: "resolves to no element",
		},
		{
			// §7.7's own case: the refine field with a label pointing at an id the
			// template renamed.
			name: "a label for nothing",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><label for="search-refine-query">Search this campaign</label><input id="q" class="target" type="search"/></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryReferenceResolves(t, rendered)
			},
			wantOne: "no accessible name",
		},
		{
			// Two forms, one id — which is what two copies of `ui.searchForm` side by
			// side produces, and `ui/states.templ`'s own comment says its `prefix`
			// parameter exists to stop it.
			name: "an id used twice",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><label for="q">Search this campaign</label><input id="q" class="target" type="search"/><label for="q">Search this campaign</label><input id="q" class="target" type="search"/></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryReferenceResolves(t, rendered)
			},
			wantOne: `id "q" appears 2 times`,
		},
		{
			name: "a link with no target class",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><a class="card-link" href="/a">A</a></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryFocusStopCarriesTarget(t, rendered)
			},
			wantOne: "does not carry the .target class",
		},
		{
			// A `<button>` rather than an anchor, so the element type is the variable
			// and an audit that only walked `a[href]` would pass this row.
			name: "a submit button with no target class",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><button type="submit">Search</button></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryFocusStopCarriesTarget(t, rendered)
			},
			wantOne: "does not carry the .target class",
		},
		{
			// The tabindex branch, which is the one an `a[href]`-shaped audit never
			// reaches, and the one the shell's own `tabindex="-1"` landmarks are on.
			name: "an element with a tabindex and no target class",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><div class="region" tabindex="-1">R</div></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryFocusStopCarriesTarget(t, rendered)
			},
			wantOne: "does not carry the .target class",
		},
		{
			// The `search-refine` owner branch: the route's own field. It is a separate
			// row from the row one below because `results.templ`'s refine form is a
			// *different file section* from its result list, and a message that named
			// only the package would send a reader looking in the wrong half of it.
			name: "a refine control with no target class",
			body: mutate(t, `class="target button button--primary"`, `class="button button--primary"`),
			check: func(t auditFailer, rendered doc) {
				assertEveryFocusStopCarriesTarget(t, rendered)
			},
			wantOne: "form[data-testid=search-refine]",
		},
		{
			// The `search-results` owner branch, for a focus stop inside the result
			// list's own `<section>` that is not in a row — here the section's heading
			// made focusable, which is what a future "make the count a filter toggle"
			// change produces.
			name: "a control in the result section with no target class",
			body: mutate(t, `<h1 id="search-results-heading" data-testid="search-result-count">`,
				`<h1 id="search-results-heading" data-testid="search-result-count" tabindex="0">`),
			check: func(t auditFailer, rendered doc) {
				assertEveryFocusStopCarriesTarget(t, rendered)
			},
			wantOne: "section[data-testid=search-results]",
		},
		{
			// The message has to name the owning package, because a focus stop in a
			// result row is `results.templ`'s and one in the banner is
			// `internal/web`'s. A message that does not distinguish them sends the
			// reader to the wrong package, so the owner half is asserted too.
			name: "a result link with no target class",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><ol><li class="card" data-path="Notes/Goblin.md"><a class="card-link" href="/c/greyhaven/wiki/Notes/Goblin">Goblin</a></li></ol></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryFocusStopCarriesTarget(t, rendered)
			},
			wantOne: "internal/httpapi/search",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{T: t}

			testCase.check(counter, parseFixture(t, testCase.body))

			if counter.failures == 0 {
				t.Fatalf("the rule reported nothing for %s; a rule that cannot fail is "+
					"a gate wired to nothing (UI §10.2)", testCase.name)
			}

			// And it fired for the *right* reason. A count alone cannot tell which
			// rule spoke, and an audit that fires for an unrelated reason looks exactly
			// like one that works.
			if !counter.mentions(testCase.wantOne) {
				t.Errorf("the rule reported %d failures for %s but none of them mentions "+
					"%q, so the rule that fired was not the rule under test; messages:\n  %s",
					counter.failures, testCase.name, testCase.wantOne,
					strings.Join(counter.messages, "\n  "))
			}
		})
	}
}

// TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch is the negative control's
// other direction, and a separate test rather than a flag on the table above because
// the two are about opposite failure modes — a row that said the wrong thing would
// otherwise look like a row written wrong.
//
// Each fixture is the *nearest* document to a violation that is not one. An audit
// that walked every element for `.target`, or rejected every `tabindex` for being
// non-zero, or counted every `tabindex` as a focus stop, would report these — and
// would be enforcing rules the record does not have. §10.6 is about *interactive*
// elements and an anchor with no `href` is not one; `-1` is what §7.2 asks for.
//
// **The two route-specific rows matter most here**, because they are the rules that
// would be wrong if written generically: an anonymous reader's document has no
// account zone, and this route's documents have no navigation landmark. An audit
// that assumed either would be enforcing a rule this shell does not have — and the
// §10.2 vocabulary audit in `internal/httpapi` has a documented case of exactly that
// mistake.
func TestEveryRouteAuditSaysNothingAboutWhatItMustNotTouch(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name string
		body string
		// wantFindings is false for every row but one, and the field is here so the
		// exception is visible in the table rather than encoded in a predicate keyed
		// on the row's name — which is the shape a "fix the test" edit takes.
		wantFindings bool
		check        func(auditFailer, doc)
	}{
		{
			name: "a paragraph is not interactive",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><p>Prose.</p></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryFocusStopCarriesTarget(t, rendered)
			},
		},
		{
			// A result row's snippet is a paragraph and is not a target, however long
			// it is — and on this route it is FTS5's excerpt, so it can be.
			name: "a result snippet is not a focus stop",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><ol><li class="card" data-path="Notes/Goblin.md"><p class="card-meta">Three goblins by the road, and then a very long excerpt.</p></li></ol></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryFocusStopCarriesTarget(t, rendered)
			},
		},
		{
			name: "an anchor with no href is not a link",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><a id="anchor">Not a link</a></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryFocusStopCarriesTarget(t, rendered)
			},
		},
		{
			// The other direction: a class on a non-interactive element is not a
			// violation either, and §4.11.1 makes carrying `.target` a *plugin's*
			// obligation rather than something the shell refuses to share.
			name: "a target class on a non-interactive element",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><p class="target">Prose.</p></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryFocusStopCarriesTarget(t, rendered)
			},
		},
		{
			name: "a negative tabindex is what §7.2 asks for",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoPositiveTabindex(t, rendered)
			},
		},
		{
			// The padded counterpart of the row above: `Atoi(strings.TrimSpace(" -1 "))`
			// is -1, so a value a template interpolated with spaces around it is still
			// the negative one §7.2 asks for and is not a finding.
			name: "a padded negative tabindex",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex=" -1 " class="target"><h1>Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoPositiveTabindex(t, rendered)
			},
		},
		{
			name: "an inline outline that suppresses nothing",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1 style="outline-offset: 2px; outline-colour: red">Page</h1></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoInlineOutlineSuppression(t, rendered)
			},
		},
		{
			// The legitimate `aria-hidden`: an icon inside a focusable link. §7.10
			// prohibits the attribute on something a keyboard can reach, not on a
			// decorative child of one.
			name: "aria-hidden on a decorative icon inside a link",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><a class="target" href="/a">A <svg aria-hidden="true"></svg></a></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertNoAriaHiddenOnAFocusStop(t, rendered)
			},
		},
		{
			// §4.6's pairing, the correct way round: this route composes the
			// pre-campaign shell, so its documents carry neither the navigation
			// landmark nor the link to it, and that is not a finding.
			name: "no navigation landmark and no link to one",
			body: `<html><head><title>t</title></head><body><a class="skip-link target" href="#main">Skip to content</a><a class="skip-link target" href="#rail">Skip to utilities</a><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1></main><aside id="rail" role="complementary" aria-label="Utilities" tabindex="-1" class="target"></aside></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertSkipLinksComeFirstAndResolve(t, rendered)
			},
		},
		{
			// The two §7.2 search labels are distinct, and a document carrying both —
			// which is what lands when the header's search form is finally wired to
			// this route's action — is not a finding under either rule.
			name: "the banner's search landmark and this route's own",
			body: `<html><head><title>t</title></head><body><header role="banner"><form role="search" aria-label="Search pages"><label for="h">Search pages</label><input id="h" class="target" type="search"/></form></header><a class="skip-link target" href="#main">Skip to content</a><main id="main" role="main" tabindex="-1" class="target"><h1>Page</h1><form role="search" aria-label="Search this campaign"><label for="c">Search this campaign</label><input id="c" class="target" type="search"/></form></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertLandmarksArePresentAndDistinguishing(t, rendered)
			},
		},
		{
			// An element with no hook at all is not a finding, and `correctDocument`
			// carries five of them — the `<ol>`, the `<li>`, the two `<p class="card-meta">`
			// and the account `<span>`. The "test hooks" row of the passing control is
			// therefore also this rule's "must not touch" row, and the row here would
			// have to duplicate that fixture to say it again.
			//
			// The object also carries an attribute the audit must not mistake for a
			// hook: `data-shell-rail` on the rail and `data-path` on the row. A rule
			// that read "every `data-` attribute is a test hook" would report both, and
			// `results.templ`'s own comment says why the row carries `data-path` at all.
			name: "data- attributes that are not test hooks",
			body: correctDocument,
			check: func(t auditFailer, rendered doc) {
				assertEveryTestIDIsPresent(t, rendered, true)
			},
		},
		{
			// The direction §4.1 decides, and the same mutation the rejecting table
			// applies to a signed-in document: `chrome.headerAccount`'s own comment
			// says the zone is "absent entirely for a reader who is not signed in", so
			// on an anonymous document its absence is **not** a missing promise. An
			// audit with one flat hook list would report two findings here, and the
			// honest fix for that would be to delete the hooks from the audit rather
			// than to ask which reader it was looking at.
			name: "no account zone on an anonymous document",
			body: mutate(t, `data-testid="header-account"`, `data-shell-account`),
			check: func(t auditFailer, rendered doc) {
				assertEveryTestIDIsPresent(t, rendered, false)
			},
		},
		{
			// An id used once, and a `<label for>` and an `aria-describedby` that both
			// resolve — the shape every one of this route's four forms has.
			name: "an id used once, referenced twice",
			body: `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><label for="q">Search this campaign</label><input id="q" class="target" type="search" aria-describedby="q"/><form role="search" aria-label="Search this campaign"></form></main></body></html>`,
			check: func(t auditFailer, rendered doc) {
				assertEveryReferenceResolves(t, rendered)
			},
		},
		{
			// A word that merely *contains* nothing retired. The row is here to say
			// that knowingly: §1.2's rule is a case-insensitive substring, so
			// "Worldly" and "worlds" are findings, and a reader who later adds a word
			// boundary is changing the rule rather than fixing an audit.
			name:         "the retired word inside a longer word is still a finding",
			body:         `<html><head><title>t</title></head><body><main id="main" tabindex="-1" class="target"><h1>Page</h1><p>Worldly, worlds, sessions.</p></main></body></html>`,
			wantFindings: true,
			check: func(t auditFailer, rendered doc) {
				assertNoRetiredEntityIsNamed(t, rendered)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{T: t}

			testCase.check(counter, parseFixture(t, testCase.body))

			switch {
			case counter.failures == 0 && testCase.wantFindings:
				t.Errorf("the rule reported nothing for %s, want a finding", testCase.name)
			case counter.failures != 0 && !testCase.wantFindings:
				t.Errorf("the rule reported %d findings for %s, want none: %s",
					counter.failures, testCase.name, strings.Join(counter.messages, "; "))
			}
		})
	}
}

// correctDocument is the smallest document this route's *success* state renders: the
// two skip links, the banner, the result list with the count and the refine form, the
// rail and the footer.
//
// **Hand-written rather than served, and package-level rather than local to the
// control that introduced it**, for two reasons. A served document is already covered
// by `TestEveryRouteSatisfiesTheStructuralContract`, so using one for the
// "reports nothing" control would make that control identical to the test above it.
// And the fixture rows for `assertEveryTestIDIsPresent` have to start from *this*
// document and take one thing away, because a hand-written minimal fixture for the
// hook rule is missing every other hook by construction — it would fire for twelve
// reasons and the one under test would be indistinguishable from the rest.
//
// It is the *results* state rather than the idle one because it is the state with
// every kind of reference in it: a section named by `aria-labelledby`, a `<label for>`,
// an `aria-describedby`, a derived heading id, and a landmark on a form.
//
// It also carries elements with **no** hook at all — the `<ol>`, the `<li>`, the two
// `<p class="card-meta">` and the account `<span>` — which is what makes it a subject
// for the "says nothing about what it must not touch" half of the hook rule.
const correctDocument = `<!doctype html>
<html lang="en"><head><title>Search — Greyhaven</title></head><body>
<a class="skip-link target" href="#main" data-testid="skip-to-content">Skip to content</a>
<a class="skip-link target" href="#rail" data-testid="skip-to-utilities">Skip to utilities</a>
<div class="shell shell--pre-campaign" data-testid="shell">
<header class="shell-header" role="banner" data-testid="shell-header">
<a class="shell-brand target" href="/" data-testid="header-home">Greyhaven</a>
<button type="button" class="target header-theme" aria-pressed="false" data-testid="header-theme">Theme: auto</button>
<div class="shell-account" data-testid="header-account"><span class="shell-account-name">mira</span>
<form method="post" action="/logout"><button type="submit" class="target" data-testid="header-sign-out">Sign out</button></form>
</div></header>
<main id="main" class="shell-main target" role="main" tabindex="-1" data-testid="shell-main">
<section class="search" aria-labelledby="search-results-heading" data-testid="search-results">
<h1 id="search-results-heading" data-testid="search-result-count">3 results for <em data-testid="search-result-query">goblin</em></h1>
<form class="stack form" method="get" action="/c/greyhaven/search" role="search" aria-label="Search this campaign" data-testid="search-refine">
<div class="field"><label for="search-refine-query">Search this campaign</label>
<input id="search-refine-query" class="target" type="search" name="q" value="goblin" aria-describedby="search-refine-hint" data-testid="search-refine-query"/>
<p id="search-refine-hint" class="field-hint" data-testid="search-refine-hint">Change the words and submit again.</p></div>
<button type="submit" class="target button button--primary" data-testid="search-refine-submit">Search</button></form>
<ol class="card-list" data-testid="search-result-list">
<li class="card" data-path="Adventures/Goblin Cave.md">
<a class="card-link target" href="/c/greyhaven/wiki/Adventures/Goblin_Cave">Goblin Cave</a>
<p class="card-meta">Adventures/Goblin Cave</p>
<p class="card-meta">Three goblins by the road.</p></li>
</ol></section></main>
<aside id="rail" class="shell-rail target" role="complementary" aria-label="Utilities" tabindex="-1" data-shell-rail data-testid="shell-rail">
<div class="rail-panels" data-testid="rail-panels">
<section class="panel" aria-labelledby="instance-rail-heading" data-testid="rail-instance">
<h2 id="instance-rail-heading">Instance</h2>
<p class="panel-line" data-testid="rail-instance-name">Greyhaven</p>
<p class="panel-line" data-testid="rail-instance-version">0.3.0</p></section></div></aside>
<footer class="shell-footer" role="contentinfo" data-testid="shell-footer">
<span class="shell-footer-item" data-testid="footer-version">Version 0.3.0</span></footer></div></body></html>`

// mutate takes one thing away from (or puts one thing into) `correctDocument`.
//
// `strings.Replace` rather than a table of whole fixtures, and it asserts the needle
// is present. That assertion is the point: a fixture row written as a literal is a
// second copy of this document that can drift from it, and a stale copy fails for a
// reason that has nothing to do with the rule under test — which is the "a fixture
// serving the same markup twice" failure the package's own mutation log already
// records once. A mutation that cannot be applied is a test failure, not a silent
// pass.
func mutate(t *testing.T, needle, replacement string) string {
	t.Helper()

	if !strings.Contains(correctDocument, needle) {
		t.Fatalf("the fixture no longer contains %q; the mutation row and "+
			"correctDocument have drifted apart, and every row that mutates it is now "+
			"testing a document nobody wrote", needle)
	}

	return strings.Replace(correctDocument, needle, replacement, 1)
}

// TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe is the third control: each
// rule must also be capable of reporting **nothing**, on a document that satisfies it.
//
// An audit that fails on everything is not a gate either — it is a gate wired to the
// wrong end, and the way that is discovered is a red suite in the first week, after
// which the audit gets switched off rather than fixed.
//
// The document is `correctDocument`, and the reason it is a hand-written fixture
// rather than one this route serves is above it.
func TestEveryRouteAuditPassesOnADocumentThisRouteCouldServe(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		check func(auditFailer, doc)
	}{
		{name: "one h1", check: func(t auditFailer, rendered doc) {
			assertTheDocumentHasExactlyOneH1(t, rendered)
		}},
		{name: "heading levels", check: func(t auditFailer, rendered doc) {
			assertHeadingLevelsNeverSkip(t, rendered)
		}},
		{name: "landmarks", check: func(t auditFailer, rendered doc) {
			assertLandmarksArePresentAndDistinguishing(t, rendered)
		}},
		{name: "tabindex", check: func(t auditFailer, rendered doc) {
			assertNoPositiveTabindex(t, rendered)
		}},
		{name: "inline outline", check: func(t auditFailer, rendered doc) {
			assertNoInlineOutlineSuppression(t, rendered)
		}},
		{name: "aria-hidden", check: func(t auditFailer, rendered doc) {
			assertNoAriaHiddenOnAFocusStop(t, rendered)
		}},
		{name: "skip links", check: func(t auditFailer, rendered doc) {
			assertSkipLinksComeFirstAndResolve(t, rendered)
		}},
		{name: "test hooks", check: func(t auditFailer, rendered doc) {
			assertEveryTestIDIsPresent(t, rendered, true)
		}},
		{name: "vocabulary", check: func(t auditFailer, rendered doc) {
			assertNoRetiredEntityIsNamed(t, rendered)
		}},
		{name: "references", check: func(t auditFailer, rendered doc) {
			assertEveryReferenceResolves(t, rendered)
		}},
		{name: "target class", check: func(t auditFailer, rendered doc) {
			assertEveryFocusStopCarriesTarget(t, rendered)
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			counter := &silentFailer{T: t}

			testCase.check(counter, parseFixture(t, correctDocument))

			if counter.failures != 0 {
				t.Errorf("the rule reported %d findings on a document this route could "+
					"actually serve, so it is too eager and will be switched off rather "+
					"than fixed: %s", counter.failures, strings.Join(counter.messages, "; "))
			}
		})
	}
}
