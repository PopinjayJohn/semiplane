package plugins_test

// UI §10.2 and §10.6 for every document this route renders, gate-blocking.
//
// # Why this file exists, and why it is here
//
// `Makefile`'s `A11Y_ROUTE_PKGS` is a `$(wildcard …)` over route package directories — so
// creating `internal/httpapi/plugins/` **automatically** added it to the a11y gate's package
// list, and the guard would have failed the build because the package contributed no test
// matching `A11Y_TESTS`. This file is what the guard is asking for, and it is here rather
// than in `internal/httpapi` for the reason `wiki`'s is: that package's audit helpers are
// unexported in `httpapi_test`, and **a test package is not importable**. So the helpers
// below are a second copy.
//
// That duplication is real and is named rather than hidden. The eventual fix is one
// exported audit package imported by `httpapi_test`, `wiki_test` and `plugins_test`, and
// until that exists this file is deliberately a *narrow* copy: the rules §10.2 and §10.6
// state for **this** route and nothing else, so the duplication stays small and the file a
// reviewer diffs against the others is short.
//
// # The documents, and why the list is short but not one
//
// §10.2 says "for every route". A route's *states* are separate documents built by separate
// branches, and a rule checked on the 200 alone is a rule checked on one implementation. So:
//
//   - **the widget**, the roller's page before any roll — a `GET` for a player, which is the
//     shape the reader meets;
//   - **the roll result**, which is a *different document*: it carries an extra block, the
//     form's values are echoed back, and the outcome element is new markup nothing else on
//     this route produces;
//   - **the roll refusal**, which is different again: an `error` state with its own element
//     and its own copy.
//
// Those three are where a rule can fail that passes everywhere else — a skipped heading
// level introduced by the outcome block, a focus stop added by the result, a retired word in
// the refusal copy — and none of them is reachable from any of the others.
//
// # The responses that are not documents, asserted rather than inferred
//
// Two. A subject list that quietly omits them is a subject list that can drift:
//
//   - the **204** a refused or absent link preview answers. It is a response on a named
//     route and it has no landmarks, no headings and no focus stops;
//   - the access gate's refusal, which is ADR 0024's byte-identical JSON and therefore not
//     a document at all.

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/web/plugins/dice"
)

// --- The documents -----------------------------------------------------------

// auditedDocuments returns every HTML document this route can put in front of a reader who
// passed the access gate.
//
// One harness for the three states that share a campaign, because a second one would mean a
// second registry and a second hub and the point is that the *same* route produced all of
// them.
func auditedDocuments(t *testing.T) []renderedDocument {
	t.Helper()

	documents := make([]renderedDocument, 0, 3)

	documents = append(documents,
		fromRecorder("200 — the widget, before any roll", widgetPage(t)),
		fromRecorder("200 — a roll the server applied", appliedRoll(t)),
		fromRecorder("200 — a roll the server refused", refusedRoll(t)),
	)

	return documents
}

// widgetPage is the roller's page with no roll on it.
func widgetPage(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	fixed := newHarness(t).withPlugins().withHub().withSystems()

	return fixed.get(campaignPath(testSlug), playerRequestor())
}

// appliedRoll is a roll the server accepted, and it is the state that carries the outcome
// block nothing else on this route produces.
func appliedRoll(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	fixed := newHarness(t).withPlugins().withHub().withSystems().applied(42)

	return fixed.post(campaignPath(testSlug), map[string]string{
		"placement": "p1",
		"expr":      "1d20+5",
		"reason":    "Perception",
		"seq":       "7",
	}, playerRequestor())
}

// refusedRoll is a roll the server declined, and it is the state with an `error` block and
// its own copy.
func refusedRoll(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	fixed := newHarness(t).withPlugins().withHub().withSystems().
		refusing(realtime.RejectNotYourTurn)

	return fixed.post(campaignPath(testSlug), map[string]string{
		"placement": "p1",
		"expr":      "1d20+5",
		"reason":    "Perception",
		"seq":       "7",
	}, playerRequestor())
}

// fromRecorder captures a response as an audited document.
func fromRecorder(where string, recorder *httptest.ResponseRecorder) renderedDocument {
	return renderedDocument{where: where, status: recorder.Code, body: recorder.Body.Bytes()}
}

// --- The audit ---------------------------------------------------------------

// TestEveryRouteSatisfiesTheStructuralContract is UI §10.2, gate-blocking, for every
// document this route renders.
//
// Each rule in its own subtest, so a failure names the rule rather than "the a11y test
// failed". The order is §10.2's own list, plus the two it implies and does not spell out —
// heading levels and reference integrity — because a landmark can be present and correctly
// labelled while a document's outline and its ARIA wiring are both broken, and neither shows
// up in a "landmarks are fine" result.
func TestEveryRouteSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	for _, doc := range auditedDocuments(t) {
		t.Run(doc.where, func(t *testing.T) {
			t.Parallel()

			audit := parseDocument(t, doc)

			t.Run("ExactlyOneH1", func(t *testing.T) {
				assertExactlyOneH1(t, audit)
			})
			t.Run("HeadingLevelsNeverSkip", func(t *testing.T) {
				assertHeadingLevelsNeverSkip(t, audit)
			})
			t.Run("LandmarksArePresentAndDistinguishing", func(t *testing.T) {
				assertLandmarksArePresentAndDistinguishing(t, audit)
			})
			t.Run("NoPositiveTabindex", func(t *testing.T) {
				assertNoPositiveTabindex(t, audit)
			})
			t.Run("NoInlineOutlineSuppression", func(t *testing.T) {
				assertNoInlineOutlineSuppression(t, audit)
			})
			t.Run("NoAriaHiddenOnAFocusStop", func(t *testing.T) {
				assertNoAriaHiddenOnAFocusStop(t, audit)
			})
			t.Run("SkipLinksComeFirstAndResolve", func(t *testing.T) {
				assertSkipLinksComeFirstAndResolve(t, audit)
			})
			t.Run("EveryTestIDIsPresent", func(t *testing.T) {
				assertEveryTestIDIsPresent(t, audit)
			})
			t.Run("Vocabulary", func(t *testing.T) {
				assertNoRetiredEntityIsNamed(t, audit)
			})
			t.Run("EveryReferenceResolves", func(t *testing.T) {
				assertEveryReferenceResolves(t, audit)
			})
		})
	}
}

// assertExactlyOneH1 is §10.2's first rule, and §7.2's.
//
// Exactly one, not at least one. Two `<h1>`s tell a screen reader the page has two titles;
// none tell it the page has none while looking complete to a sighted reader.
//
// **This route's second `<h1>` would come from a plugin**, which is the half the wiki route
// cannot make: there the second heading is author markdown arriving through Obsidian Sync,
// and here it is a `webplugins.PageType.Render` returning a component with its own `<h1>`
// because nobody told it the document had one. A UI plugin runs in this process with full
// authority, so §10.6's "including plugin output" makes this audit a gate on plugins too.
func assertExactlyOneH1(t auditFailer, audit *docAudit) {
	t.Helper()

	headings := findAll(audit.root, "h1")
	if len(headings) == 1 {
		return
	}

	names := make([]string, 0, len(headings))
	for _, heading := range headings {
		names = append(names, nodePath(heading))
	}

	t.Errorf("%s: the document has %d <h1> elements, want exactly 1 (UI §10.2, §7.2); "+
		"found: %s. On this route the second is a page type's own component: a UI plugin "+
		"renders in this process and §10.6's target-size and vocabulary rules explicitly "+
		"include plugin output", audit.where, len(headings), strings.Join(names, ", "))
}

// assertHeadingLevelsNeverSkip is §7.2's second structural rule.
//
// Separate from the `<h1>` count because it can fail where the count passes: a document whose
// headings start at `<h3>` has exactly one `<h1>`-shaped hole and no valid outline at all.
func assertHeadingLevelsNeverSkip(t auditFailer, audit *docAudit) {
	t.Helper()

	previous := 0

	audit.elements(func(node *html.Node) {
		level, isHeading := headingLevel(node)
		if !isHeading {
			return
		}

		if previous != 0 && level > previous+1 {
			t.Errorf("%s: a heading jumps from h%d to h%d at %s; a skipped level is "+
				"announced as a missing section (UI §7.2)",
				audit.where, previous, level, nodePath(node))
		}

		previous = level
	})
}

// headingLevel returns an element's heading level, or false when it is not a heading.
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

// assertLandmarksArePresentAndDistinguishing is §10.2's landmark rule and §7.2's, in four
// parts.
//
// Presence, because a missing landmark is a region a landmark-navigation reader cannot jump
// to. Distinguishing labels, because two landmarks of the same role with the same name make
// landmark navigation useless. Exactness, for the roles the record writes out. And nesting,
// because a `contentinfo` inside a `main` is the article's and not the page's.
//
// **The navigation half is where this route is worth auditing rather than assuming.**
// §4.6 removes the navigation before a campaign exists and §7.2 puts the navigation's skip
// link in the document only where the navigation does — and this route's document is not the
// campaign shell at all: it is a widget with one `main` and no banner, no rail and no
// footer. So there is no `#nav` skip link **and** no navigation landmark, and both directions
// of that pairing are asserted below, which is what stops a future change from adding one
// without the other.
func assertLandmarksArePresentAndDistinguishing(t auditFailer, audit *docAudit) {
	t.Helper()

	found := audit.landmarks()

	present := map[string]int{}
	for _, region := range found {
		present[region.role]++
	}

	// `main` is the only landmark required unconditionally (§3.1's tiers move the rail out
	// of the flow rather than out of the document, and this document has no rail at all).
	if present["main"] == 0 {
		t.Errorf("%s: the document has no main landmark (UI §10.2, §7.2)", audit.where)
	}

	if present["main"] > 1 {
		t.Errorf("%s: the document has %d main landmarks, want 1 (UI §7.2); two of them "+
			"give a screen reader two articles and no way to choose", audit.where, present["main"])
	}

	// Distinctness across the whole document, before the per-role labels: a duplicate is a
	// duplicate whatever the roles are.
	seen := map[string]string{}

	for _, region := range found {
		if region.name == "" {
			continue
		}

		key := region.role + "=" + region.name
		if first, duplicate := seen[key]; duplicate {
			t.Errorf("%s: two %s landmarks are both labelled %q (%s and %s); §10.2 requires "+
				"distinguishing labels or landmark navigation is useless",
				audit.where, region.role, region.name, first, nodePath(region.node))
		}

		seen[key] = nodePath(region.node)
	}

	// §7.2's fixed names, for the roles this document can carry. A `navigation` or a
	// `complementary` here would be a landmark the record does not put on a plugin page, and
	// a plugin that added one would need to name it as the record spells it.
	for _, region := range found {
		switch region.role {
		case "navigation":
			if !allowedNavigation[region.name] {
				t.Errorf("%s: a navigation landmark is labelled %q; UI §7.2 names them %q "+
					"(the campaign nav) and %q (the compact bar)",
					audit.where, region.name, "Campaign", "Primary")
			}
		case "complementary":
			if region.name != "Utilities" {
				t.Errorf("%s: the complementary landmark is labelled %q, want %q (UI §7.2)",
					audit.where, region.name, "Utilities")
			}
		case "search":
			if !allowedSearch[region.name] {
				t.Errorf("%s: a search landmark is labelled %q; UI §7.2 requires the header's "+
					"form and the search route's own form to be named differently, and those "+
					"are the two names the record fixes: %v",
					audit.where, region.name, allowedSearchNames)
			}
		case "banner", "contentinfo", "main":
		default:
			t.Errorf("%s: %s at %s is a %s landmark, which is not one UI §7.2's plugin "+
				"document places; a landmark the record does not describe is a region a "+
				"reader can jump to and nobody knows what it is for",
				audit.where, region.role, nodePath(region.node), region.role)
		}
	}

	for _, region := range found {
		if region.role == "contentinfo" && hasAncestor(region.node, "main") {
			t.Errorf("%s: a contentinfo landmark is inside <main> at %s; it is the page's "+
				"footer, not the article's (UI §7.2)", audit.where, nodePath(region.node))
		}
	}
}

// allowedNavigation and allowedSearchNames are the labels §7.2's own zones fix.
//
// **Sets, not single literals**, for the reason `wiki`'s audit gives: the record's
// requirement is that the names be *distinct*, which is a rule about the pair. Asserting one
// member of a pair tests it by naming the other, and would have failed the search route's own
// landmark the moment that route landed.
var (
	allowedNavigation = map[string]bool{"Campaign": true, "Primary": true}

	allowedSearchNames = []string{"Search pages", "Search this campaign"}
)

// allowedSearch is `allowedSearchNames` as a set, built once because a `map` inside the rule
// would be rebuilt per landmark.
var allowedSearch = func() map[string]bool {
	names := make(map[string]bool, len(allowedSearchNames))

	for _, name := range allowedSearchNames {
		names[name] = true
	}

	return names
}()

// assertNoPositiveTabindex is §10.2's tabindex rule and §7.4's "no positive tabindex,
// anywhere — gate failure".
//
// Zero is excluded too. §7.4 says Tab follows natural document order, and a
// `tabindex="0"` moves one element to the front of the focus order while the markup still
// reads in the original order — so the two disagree and neither a reader nor a test can tell
// which one the page means. Only `-1` is usable: it makes an element programmatically
// focusable for a skip link without putting it in the tab sequence.
//
// The value is parsed as a number rather than compared as a string, and an unparseable value
// is itself a finding: three browsers disagreeing about the tab order is the failure, and
// `+3` sorted before `0` in a string comparison.
func assertNoPositiveTabindex(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		raw := attr(node, "tabindex")
		if raw == "" {
			return
		}

		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			t.Errorf("%s: %s carries tabindex=%q, which is not an integer; three browsers "+
				"would disagree about the tab order (UI §10.2, §7.2)",
				audit.where, nodePath(node), raw)

			return
		}

		if value > -1 {
			t.Errorf("%s: %s carries tabindex=%d; only -1 is permitted. 0 moves the "+
				"element to the front of the tab order while the markup still reads in "+
				"document order (UI §10.2, §7.4)", audit.where, nodePath(node), value)
		}
	})
}

// assertNoInlineOutlineSuppression is §10.2's "no `outline: none` without a replacement",
// over the half of it a document can carry.
//
// **The other half is the stylesheet's, and it is not this file's.**
// `internal/web`'s `TestNoRuleRemovesAFocusIndicatorWithoutAReplacement` reads the *built*
// stylesheet and asserts both that `outline: none` appears nowhere and that `:focus-visible`
// is present. Run that half from here it would be a second copy of the same rule with a
// different fixture.
//
// What a route can violate is the **inline** half: a `style` attribute the route or a
// template interpolated, which overrides the sheet for one element and is invisible to every
// stylesheet-level gate. On this route it is not hypothetical — a plugin renders in this
// process, and a plugin that emitted an inline suppression would defeat the focus ring for
// one element with nothing in the stylesheet saying so.
//
// Read as a *declaration* in the attribute rather than as a substring, so that
// `outline-offset` and `outline-colour` — which suppress nothing — are not findings, and
// `outline : 0` is.
func assertNoInlineOutlineSuppression(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		style := attr(node, "style")
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
				t.Errorf("%s: %s carries an inline `outline: %s`; §10.2 and §7.10 prohibit "+
					"removing a focus indicator without a visible replacement, and this "+
					"stylesheet's replacement is the two-tone ring rather than a suppression. "+
					"Nothing on this element puts one back",
					audit.where, nodePath(node), strings.TrimSpace(value))
			default:
			}
		}
	})
}

// assertNoAriaHiddenOnAFocusStop is §10.2's `aria-hidden` rule and §7.10's, asserted on the
// *combination* rather than the attribute.
//
// Not on the attribute: the legitimate case in a document like this one is an icon inside a
// focusable link — an `<svg aria-hidden="true">` within an `<a href>`, which the link preview
// could carry. The attribute alone is therefore not the finding; the attribute on something a
// keyboard can reach is.
//
// Two cases, and the second catches real bugs: a focusable element **inside** an
// `aria-hidden` subtree is just as unreachable, and the offending attribute is on an ancestor
// several levels up.
func assertNoAriaHiddenOnAFocusStop(t auditFailer, audit *docAudit) {
	t.Helper()

	audit.elements(func(node *html.Node) {
		if attr(node, "aria-hidden") != "true" {
			return
		}

		if isFocusStop(node) {
			t.Errorf("%s: %s is focusable and aria-hidden; a control a screen reader cannot "+
				"see is one that reader cannot reach (UI §10.2, §7.10)",
				audit.where, nodePath(node))
		}

		if containsFocusStop(node) {
			t.Errorf("%s: %s is aria-hidden and contains focusable elements, so everything "+
				"inside it is unreachable to a screen reader (UI §7.10)",
				audit.where, nodePath(node))
		}

		for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
			if ancestor.Type == html.ElementNode &&
				attr(ancestor, "aria-hidden") == "true" && containsFocusStop(ancestor) {
				t.Errorf("%s: %s is inside the aria-hidden subtree at %s, which contains "+
					"focusable elements (UI §7.10)",
					audit.where, nodePath(node), nodePath(ancestor))

				return
			}
		}
	})
}

// assertSkipLinksComeFirstAndResolve is §10.2's "skip links first in tab order" and §7.2's
// own list, in three parts.
//
// Position — the links must be the first focus stops in the document. A skip link after the
// banner is not a skip link, it is a link named "skip". Resolution — each `href="#id"` must
// resolve, and to a **focusable landmark**: a skip link to a landmark that is not in the
// document moves focus nowhere, which costs a reader a keypress and teaches them the skip
// links are unreliable. Order — §7.2's sequence, with the campaign-navigation link only
// where the navigation exists.
//
// **Both directions of the nav pairing are asserted**, and on this route the interesting
// case is the negative one: there is no navigation landmark and no `#nav` link, so both
// absences must hold. A future change that adds a navigation landmark to a plugin document
// without its skip link fails here.
func assertSkipLinksComeFirstAndResolve(t auditFailer, audit *docAudit) {
	t.Helper()

	links := audit.skipLinks()
	if len(links) == 0 {
		t.Errorf("%s: the document has no skip link; §7.2 makes them the first focusable "+
			"elements and this document always has at least one", audit.where)

		return
	}

	focusStops := audit.focusStops()

	for index, link := range links {
		if index >= len(focusStops) || focusStops[index] != link.node {
			t.Errorf("%s: skip link %d (%q) is not focus stop %d; §7.2 puts the skip "+
				"links first in tab order", audit.where, index+1, link.text, index+1)

			continue
		}

		fragment, isFragment := strings.CutPrefix(link.href, "#")
		if !isFragment || fragment == "" {
			t.Errorf("%s: skip link %q has href %q; §7.2's links are fragments",
				audit.where, link.text, link.href)

			continue
		}

		target := byID(audit.root, fragment)
		if target == nil {
			t.Errorf("%s: skip link %q points at #%s, which is not in the document; §4.6 "+
				"removes some landmarks per route, so the link has to go with them",
				audit.where, link.text, fragment)

			continue
		}

		if !hasAttribute(target, "tabindex") {
			t.Errorf("%s: skip link %q lands on %s, which has no tabindex; without one the "+
				"target is not focusable and focus lands at the top of a scroll container "+
				"instead of on an announced element (UI §7.2)",
				audit.where, link.text, nodePath(target))
		}

		if roleOf(target) == "" {
			t.Errorf("%s: skip link %q lands on %s, which is not a landmark; a skip "+
				"link's target is the region it names (UI §7.2)",
				audit.where, link.text, nodePath(target))
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
			audit.where, linkTextAt(links, 0))
	}

	if navigationAt >= 0 && navigationAt < contentAt {
		t.Errorf("%s: the campaign-navigation skip link precedes the content link; §7.2's "+
			"order is content first", audit.where)
	}

	hasNavLandmark := byID(audit.root, "nav") != nil
	hasNavLink := navigationAt >= 0

	if hasNavLandmark && !hasNavLink {
		t.Errorf("%s: the document has a navigation landmark but no skip link to it; §7.2 "+
			"lists %q -> #nav wherever the nav exists", audit.where, "Skip to campaign navigation")
	}

	if hasNavLink && !hasNavLandmark {
		t.Errorf("%s: the document has a skip link to #nav but no navigation landmark; "+
			"§4.6 removes the nav before a campaign exists and a skip link to an absent "+
			"landmark moves focus nowhere", audit.where)
	}
}

// linkTextAt names the nth skip link for a failure message.
func linkTextAt(links []skipLink, index int) string {
	if index >= len(links) {
		return "(none)"
	}

	return links[index].text
}

// assertEveryTestIDIsPresent is §10.2's "every `data-testid` present", in two halves.
//
// The well-formedness half: a hook must have a non-empty value and appear once. A hook that
// appears twice selects nothing, and an *absent* hook is not an empty one — counting the
// empty string would report every unhooked element as a duplicate of every other.
//
// The coverage half: **the hooks this route's own markup promises, by state.** The document
// shell always renders `page-title` and `shell-main`; the roller's form is always there; and
// `roll-result` and `roll-refused` are **alternatives** — the route renders one outcome state
// or the other or neither, and asserting exactly one of the pair would fail the widget, which
// correctly has neither.
func assertEveryTestIDIsPresent(t auditFailer, audit *docAudit) {
	t.Helper()

	counts := map[string]int{}

	audit.elements(func(node *html.Node) {
		if !hasAttribute(node, "data-testid") {
			return
		}

		counts[attr(node, "data-testid")]++
	})

	duplicates := make([]string, 0)

	for id, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, id+" x"+strconv.Itoa(count))
		}
	}

	sort.Strings(duplicates)

	if len(duplicates) > 0 {
		t.Errorf("%s: these data-testid hooks appear more than once: %s; a hook that appears "+
			"twice selects nothing (UI §10.2)", audit.where, strings.Join(duplicates, ", "))
	}

	for id := range counts {
		if strings.TrimSpace(id) == "" {
			t.Errorf("%s: an element carries an empty data-testid; a hook with no value "+
				"selects nothing and cannot be asserted on (UI §10.2)", audit.where)
		}
	}

	// The hooks this route's own templates promise on **every** state.
	for _, required := range []string{
		"page-title", "shell-main", "skip-to-main", "roll-form", "roll-submit",
	} {
		if counts[required] == 0 {
			t.Errorf("%s: the document carries no %q hook; this route's templates promise "+
				"it on every state (UI §10.2)", audit.where, required)
		}
	}

	// The three outcomes, at most one between them. All three would render three different
	// things at once; none would render the page's answer.
	outcomes := counts["roll-result"] + counts["roll-refused"]
	if outcomes > 1 {
		t.Errorf("%s: the document carries %d outcome hooks; a roll is applied, refused, or "+
			"neither has happened yet, and the widget's three states are alternatives "+
			"(UI §10.2)", audit.where, outcomes)
	}

	// And the outcome hooks the *document's own state* requires. This is the coverage half
	// doing its work: "every hook present" would be satisfied by a page that rendered no
	// outcome block at all, and §10.2's rule is about the states this route actually serves.
	switch {
	case strings.Contains(audit.where, "applied") && counts["roll-result"] == 0:
		t.Errorf("%s: a roll the server applied rendered no %q hook; the answer the hub "+
			"returned is the whole point of §10.6's dice roller", audit.where, "roll-result")
	case strings.Contains(audit.where, "refused") && counts["roll-refused"] == 0:
		t.Errorf("%s: a roll the server refused rendered no %q hook; a refusal that renders "+
			"nothing is a reader looking at an unchanged form", audit.where, "roll-refused")
	default:
	}
}

// assertNoRetiredEntityIsNamed is UI §1.2 and §10.2's last clause: no rendered string
// contains "world" or "session".
//
// The scan covers **every text node, every comment, every attribute value and every
// attribute name**, and each inclusion has a reason.
//
// Comments: a word in a comment is invisible to a reader, which is exactly why an audit
// reading only text nodes would pass on a document whose markup still names a retired entity
// — and the comment is the one place a renderer is tempted to leave one, because nobody sees
// it. On this route that matters twice over: the outcome blocks are plugin-rendered, and a
// plugin is the author of a page body.
//
// Attribute values: an `aria-label` or a `title` is announced to a reader, so a retired
// entity named there is named to the person the rule protects.
//
// Attribute names: a `data-world` is invisible to a reader and obvious to a developer
// grepping for the feature it implies, which makes it exactly as retired as a label.
func assertNoRetiredEntityIsNamed(t auditFailer, audit *docAudit) {
	t.Helper()

	report := func(where, text string) {
		lowered := strings.ToLower(text)

		for _, retired := range retiredWords {
			if strings.Contains(lowered, retired) {
				t.Errorf("%s: %s contains %q; neither entity exists, and §1.2 makes the "+
					"words appear nowhere in the interface", audit.where, where, retired)
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
			for _, attribute := range node.Attr {
				report("the attribute "+attribute.Key, attribute.Val)
				report("an attribute name", attribute.Key)
			}
		case html.DoctypeNode:
		default:
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(audit.root)
}

// assertEveryReferenceResolves is the ARIA integrity rule §10.2's list implies and does not
// spell out: a document whose `aria-controls` points at nothing is a control claiming to
// control something invisible.
//
// All five reference attributes, plus `<label for>` and `href="#…"`. The id uniqueness half is
// not incidental: two elements sharing an id make every reference to it ambiguous, and a
// screen reader resolves the ambiguity by picking the first.
//
// **The `<label for>` branch is the one this route actually exercises**, and it is the branch
// a widget is most likely to break: the roller's form has three labelled fields whose `for`
// names the input's id, and an id renamed in one place and not the other produces a field
// with no accessible name — which §7.7 calls out explicitly.
func assertEveryReferenceResolves(t auditFailer, audit *docAudit) {
	t.Helper()

	ids := map[string]int{}

	audit.elements(func(node *html.Node) {
		if id := attr(node, "id"); id != "" {
			ids[id]++
		}
	})

	for id, count := range ids {
		if count > 1 {
			t.Errorf("%s: id %q appears %d times; every reference to it is ambiguous and a "+
				"screen reader resolves the ambiguity by picking the first (UI §7.2)",
				audit.where, id, count)
		}
	}

	references := []string{
		"aria-controls",
		"aria-labelledby",
		"aria-describedby",
		"aria-owns",
		"aria-activedescendant",
	}

	audit.elements(func(node *html.Node) {
		for _, name := range references {
			value := attr(node, name)
			if value == "" {
				continue
			}

			for id := range strings.FieldsSeq(value) {
				if byID(audit.root, id) == nil {
					t.Errorf("%s: %s carries %s=%q, which resolves to no element; a control "+
						"that claims to control something invisible is a control that lies "+
						"(UI §7.2)", audit.where, nodePath(node), name, id)
				}
			}
		}

		// §7.7: a real `<label for>`, never a placeholder.
		if node.Data == "label" {
			if target := attr(node, "for"); target != "" && byID(audit.root, target) == nil {
				t.Errorf("%s: %s has for=%q, which resolves to no element; the field has no "+
					"accessible name (UI §7.7)", audit.where, nodePath(node), target)
			}
		}

		if node.Data == "a" {
			if fragment, isFragment := strings.CutPrefix(attr(node, "href"), "#"); isFragment &&
				fragment != "" && byID(audit.root, fragment) == nil {
				t.Errorf("%s: %s has href=\"#%s\", which resolves to no element",
					audit.where, nodePath(node), fragment)
			}
		}
	})

	// §7.7's other half: **a focus stop with no name at all.** The `<label for>` branch above
	// catches a field whose label points at nothing; this catches a control that never had a
	// label, an `aria-label`, or any text — a button whose only content is a decorative image.
	//
	// The reachability here is the point: the roller's outcome blocks carry conditional copy,
	// so a widget change that wrapped the submit button's label in an element the name
	// computation cannot see would produce exactly this document, and the `<label for>` branch
	// would say nothing about it.
	audit.elements(func(node *html.Node) {
		if !isFocusStop(node) {
			return
		}

		if accessibleName(node) != "" {
			return
		}

		t.Errorf("%s: %s is a focus stop with no accessible name; a control announced as "+
			"nothing cannot be reached by name, by landmark or by screen-reader search "+
			"(UI §7.7)", audit.where, nodePath(node))
	})
}

// accessibleName is the computed name for a control, in the order the platform computes it:
// `aria-labelledby`, `aria-label`, a `<label for>`, a wrapping `<label>`, the element's own
// text, and an image's `alt`.
//
// **Deliberately a subset of the full accname algorithm**, and the reason is that this audit
// is looking for a control with *no* name at all. Every branch that could supply a name is
// checked, so a false finding needs all of them absent — and a false finding here would be a
// rule no correct document could satisfy, which is worse than the case it catches.
func accessibleName(node *html.Node) string {
	for _, name := range []string{"aria-labelledby", "aria-label", "title"} {
		if value := attr(node, name); value != "" {
			// An id list resolves to the referenced elements' text; the ids themselves are
			// never the name.
			if name != "aria-labelledby" {
				return value
			}

			return referencedText(node, value)
		}
	}

	if node.Data == "img" {
		return attr(node, "alt")
	}

	if target := attr(node, "id"); target != "" {
		// **A search of the document, not of the ancestors.** A `<label for>` is a *sibling*
		// of the input it names in every form this project renders — the roller's three fields
		// are all `<div class="field">` wrappers holding a label and an input — so an upward
		// walk finds nothing and every field reads as unnamed. That is the false finding a
		// naive implementation produces, and it would have been "fixed" by adding the class
		// §7.7 does not need.
		if label := labelFor(documentOf(node), target); label != nil {
			return elementText(label)
		}
	}

	if wrapping := wrappingLabel(node); wrapping != nil {
		return elementText(wrapping)
	}

	return elementText(node)
}

// labelFor returns the `<label for>` naming an id, or nil.
func labelFor(root *html.Node, id string) *html.Node {
	var found *html.Node

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if found != nil {
			return
		}

		if node.Type == html.ElementNode && node.Data == "label" && attr(node, "for") == id {
			found = node

			return
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return found
}

// referencedText resolves an `aria-labelledby` id list to the text of the elements it names.
func referencedText(node *html.Node, ids string) string {
	var names []string

	for id := range strings.FieldsSeq(ids) {
		if target := byID(documentOf(node), id); target != nil {
			names = append(names, elementText(target))
		}
	}

	return strings.Join(names, " ")
}

// wrappingLabel returns the nearest ancestor `<label>`, or nil.
func wrappingLabel(node *html.Node) *html.Node {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && ancestor.Data == "label" {
			return ancestor
		}
	}

	return nil
}

// elementText is an element's text content, skipping anything an author hid from a reader.
func elementText(node *html.Node) string {
	var text strings.Builder

	var walk func(*html.Node)

	walk = func(current *html.Node) {
		if attr(current, "aria-hidden") == "true" {
			return
		}

		if current.Type == html.TextNode {
			text.WriteString(current.Data)

			return
		}

		if current.Type == html.ElementNode && current.Data == "img" {
			// An image's name comes from `alt`, and an empty `alt` is decorative — so the
			// text of a button containing one is the button's text, not the image's.
			text.WriteString(attr(current, "alt"))

			return
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return strings.TrimSpace(text.String())
}

// documentOf walks up to the parsed document, which is the tree `byID` searches.
func documentOf(node *html.Node) *html.Node {
	root := node

	for root.Parent != nil {
		root = root.Parent
	}

	return root
}

// --- §10.6 -------------------------------------------------------------------

// TestEveryRouteCarriesTheTargetClassOnEveryFocusStop is UI §10.6 and §7.3's "every
// interactive element … uses it", over every document this route composes.
//
// **This is the rule a plugin decides, and it is the one §10.6 names plugin output for.** The
// shell and the form primitives carry `ui.TargetClass` because their templates say so, and
// nothing about a plugin's markup is checked until this runs. A UI plugin renders in this
// process with full authority; the `.target` class is the only thing between its buttons and
// a 30px-tall control on a television, and the minimum is asserted from the **built**
// stylesheet by `internal/web`'s `TestTheTargetMinimumsMeetTheSpecifiedFloors`.
//
// The markup carries the class and the stylesheet decides what it means, which is why this is
// a structural rule and not a measurement.
//
// Its own top-level test rather than an eleventh subtest of
// `TestEveryRouteSatisfiesTheStructuralContract`, because §10.6 is a different section of the
// record with its own floor and a rule that fails is easier to route to a person when the
// failure line names the section it came from.
func TestEveryRouteCarriesTheTargetClassOnEveryFocusStop(t *testing.T) {
	t.Parallel()

	for _, doc := range auditedDocuments(t) {
		t.Run(doc.where, func(t *testing.T) {
			t.Parallel()

			audit := parseDocument(t, doc)

			assertEveryFocusStopCarriesTarget(t, audit)
		})
	}
}

// assertEveryFocusStopCarriesTarget is §10.6's "grep rendered markup for interactive
// elements missing the class".
//
// A **per-element** walk rather than a count, because a count cannot name the element and a
// failure message that does not name it is one a reader has to go and re-derive.
//
// **The hidden `seq` field is excluded by `isFocusStop`**, and that is not a loophole: it is
// not a focus stop, so §10.6 does not apply to it, and an audit that demanded a minimum
// target size on an element no keyboard can reach would be a rule a correct form cannot
// satisfy. The exclusion lives in one place and both the rule and the negative control read
// it.
func assertEveryFocusStopCarriesTarget(t auditFailer, audit *docAudit) {
	t.Helper()

	for _, node := range audit.focusStops() {
		if hasClass(node, "target") {
			continue
		}

		owner := "the route's own document wrote it, so the fix is in " +
			"internal/httpapi/plugins/document.go"
		if hasAncestorWithTestID(node, "roll-form") {
			owner = "it is inside the roller's form, so it came out of a plugin component " +
				"and the fix is in internal/web/plugins/dice"
		}

		t.Errorf("%s: %s is focusable and does not carry the .target class; §7.3 enforces "+
			"the --target-min minimum by construction and §10.6 audits for it **including "+
			"plugin output**. %s", audit.where, nodePath(node), owner)
	}
}

// hasAncestorWithTestID reports whether any ancestor carries the given test hook.
//
// By hook rather than by a class or a landmark: `data-testid` is the contract the templates
// state, and a rule keyed on it keeps working when the styling class is renamed.
func hasAncestorWithTestID(node *html.Node, id string) bool {
	for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
		if ancestor.Type == html.ElementNode && attr(ancestor, "data-testid") == id {
			return true
		}
	}

	return false
}

// --- The controls the audit itself depends on --------------------------------

// TestTheSkipLinkAndTheLandmarkItNamesAreOneCondition is AGENTS.md's pairing claim, in both
// directions, over the rendered bytes rather than over the template.
//
// "A link with no landmark" and "a landmark with no link" are both failures and only one of
// them is visible in a rendering — so both are asserted, and over the **response**, because a
// template-level assertion would not notice a route that stopped rendering the template at
// all.
func TestTheSkipLinkAndTheLandmarkItNamesAreOneCondition(t *testing.T) {
	t.Parallel()

	for _, doc := range auditedDocuments(t) {
		t.Run(doc.where, func(t *testing.T) {
			t.Parallel()

			audit := parseDocument(t, doc)

			links := audit.skipLinks()
			if len(links) != 1 {
				t.Fatalf("%s: the document carries %d skip links, want 1; §7.2 pairs each "+
					"one with the landmark it names and this document has one landmark",
					doc.where, len(links))
			}

			target := byID(audit.root, strings.TrimPrefix(links[0].href, "#"))
			if target == nil {
				t.Fatalf("%s: the skip link points at %q, which is not in the document; a "+
					"skip link to an absent landmark moves focus nowhere", doc.where, links[0].href)
			}

			if roleOf(target) != "main" {
				t.Errorf("%s: the skip link lands on %s, a %q; §7.2's link names the region "+
					"it skips to and this document's only region is main",
					doc.where, nodePath(target), roleOf(target))
			}

			if !hasAttribute(target, "tabindex") {
				t.Errorf("%s: the skip link's target has no tabindex, so focus lands at the "+
					"top of a scroll container instead of on an announced element (UI §7.2)",
					doc.where)
			}

			// **The other direction, and the one a rendering cannot show.** This document has
			// no navigation landmark, so §7.2's "only where the nav exists" says there must
			// be no navigation skip link. A future change that added one without the
			// landmark would fail here rather than ship a link that moves focus nowhere.
			if byID(audit.root, "nav") != nil {
				t.Errorf("%s: the document has a navigation landmark; this route's document "+
					"is a widget inside the campaign shell and adds no navigation of its own, "+
					"so the landmark is a region nobody can reach from a skip link",
					doc.where)
			}
		})
	}
}

// TestTheCentreSlotIsEscapedBeforeItReachesTheDocument holds the premise of
// `documentData.Centre`'s `template.HTML`.
//
// **This is the one place this project uses a "trust me" escape**, so what a test can hold is
// the premise rather than the conclusion: a hostile expression typed into the roller's form
// arrives on the page as *text* and not as markup. If a plugin's template ever stopped
// escaping, `documentData.Centre` would become an XSS in a route that renders untrusted
// input, and the mechanism that would report it is this test.
//
// The payload is the exact shape of an injection — a quote that closes the attribute, a
// focus handler, and a tag — because a payload that is merely `<script>` would be caught by
// any escaper and would prove nothing about the attribute context.
func TestTheCentreSlotIsEscapedBeforeItReachesTheDocument(t *testing.T) {
	t.Parallel()

	const hostile = `1d20" autofocus onfocus="alert(1)"><script>alert(2)</script><img src=x onerror=alert(3)>`

	fixed := newHarness(t).withPlugins().withHub().withSystems().applied(42)

	recorder := fixed.post(campaignPath(testSlug), map[string]string{
		"placement": "p1",
		"expr":      hostile,
		"reason":    hostile,
		"seq":       "7",
	}, playerRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("the roll answered %d, want 200: %q", recorder.Code,
			truncateBody(recorder.Body.String()))
	}

	audit := parseDocument(t, renderedDocument{
		where:  "a hostile expression",
		status: recorder.Code,
		body:   recorder.Body.Bytes(),
	})

	// **Nothing that executes.** A `<script>` element in the body, or an `on*` attribute on
	// any element, would mean the value reached the document as markup.
	if scripts := findAll(audit.root, "script"); len(scripts) > 2 {
		t.Errorf("the document carries %d <script> elements; this route's own head resolver "+
			"renders two, and a third means the plugin's value was not escaped",
			len(scripts))
	}

	audit.elements(func(node *html.Node) {
		for _, attribute := range node.Attr {
			name := strings.ToLower(attribute.Key)
			if strings.HasPrefix(name, "on") {
				t.Errorf("%s carries %s=%q; the plugin's template did not escape its "+
					"values, so a reader who types a dice expression could run script",
					nodePath(node), attribute.Key, truncateBody(attribute.Val))
			}
		}
	})

	// **The escaped form is present, which is the positive half.** Without it the three
	// assertions above would be vacuous: a template that rendered nothing passes them. The
	// check is over the raw bytes rather than `textOf`, because the value is echoed into an
	// `<input value>` **attribute** — where `html/template` escapes the quote as `&#34;` and
	// a text-node walk would not see it at all.
	if !strings.Contains(recorder.Body.String(), "onfocus") {
		t.Errorf("the hostile expression does not appear anywhere in the response; the "+
			"field discarded its input, which would make the escaping assertions above "+
			"vacuous. Body: %q", truncateBody(recorder.Body.String()))
	}

	// And the value round-tripped into the field the reader would edit, still escaped — the
	// echoed `value` is the one place a template could escape for display and then paste the
	// raw text into an attribute, which is the classic double-render bug.
	echoed := findAll(audit.root, "input")
	for _, field := range echoed {
		if attr(field, "name") != "expr" {
			continue
		}

		if value := attr(field, "value"); !strings.Contains(value, "onfocus") {
			t.Errorf("the expression field echoes %q; the reader's own input must come back "+
				"so a refused roll can be retried, and it must come back escaped",
				truncateBody(value))
		}
	}
}

// TestThePrefixedOpIsTheOnesTheRouteNames is the plugin's own rule, asserted where the
// document is built: a roll page for a campaign playing a system that resolves no rolls must
// still render, and this is the assertion that says the page did not invent one.
//
// §10.6's dice roller emits `{"op":"roll"}` and `dice.OpRoll` is the constant that says so.
// This test is here rather than in the plugin package because the failure it catches is a
// *route* failure: a route that dispatched `move_token` for a "roll" would leave the plugin
// innocent.
func TestThePrefixedOpIsTheOnesTheRouteNames(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withPlugins().withHub().withSystems().applied(42)

	recorder := fixed.post(campaignPath(testSlug), map[string]string{
		"placement": "p1", "expr": "1d20", "seq": "7",
	}, playerRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("the roll answered %d, want 200: %q", recorder.Code,
			truncateBody(recorder.Body.String()))
	}

	recorded := fixed.resolver.last(t)
	if recorded.intent.Frame.Op != realtime.Op(dice.OpRoll) {
		t.Errorf("the resolver saw op %q, want %q", recorded.intent.Frame.Op, dice.OpRoll)
	}
}

// TestTheRouteAppliesNoRoleListOfItsOwn is §7.2's authority, asserted as a *negative*.
//
// **A membership carrying a role this build has no name for is dispatched anyway**, and that
// is correct: `realtime.NewActor` does not check the role — `Join` does, for a peer — so a
// dispatch through `Actor` reaches the system with a role the system does not recognise, and
// the *system* is where §7.2's rule lives. `plugin.Resolver`'s own header says the same in
// as many words: it holds no authorisation list, because "a list here would be a second
// answer to a question `realtime.Core` already had to fake".
//
// So the test is the opposite of "it is refused": it asserts that **the route dispatched,
// with the role the membership carried, verbatim.** A route that grew a role check would
// refuse first — and that would be the second authority this package must not have, and the
// assertions below would fail on a route that looked *more* careful.
func TestTheRouteAppliesNoRoleListOfItsOwn(t *testing.T) {
	t.Parallel()

	// A GM and a player, both of whom clear the gate, and both of whom must reach the
	// resolver. **The player row is the interesting one**: a route that held a role list
	// would most plausibly get it wrong in the direction of refusing a player's roll, so
	// the assertion that a *player* dispatches is the one that catches a list.
	for _, testCase := range []struct {
		name      string
		requestor domain.Requestor
		wantRole  domain.Role
	}{
		{name: "the campaign's GM", requestor: gmRequestor(), wantRole: domain.RoleGM},
		{
			name: "a member with the player role", requestor: playerRequestor(),
			wantRole: domain.RolePlayer,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t).withPlugins().withHub().withSystems().applied(42)

			recorder := fixed.post(campaignPath(testSlug), map[string]string{
				"placement": "p1", "expr": "1d20", "seq": "7",
			}, testCase.requestor)

			if recorder.Code != http.StatusOK {
				t.Fatalf("the roll answered %d, want 200: %q", recorder.Code,
					truncateBody(recorder.Body.String()))
			}

			intents := fixed.resolver.recorded()
			if len(intents) != 1 {
				t.Fatalf("the resolver was called %d times, want 1; the route dispatches and "+
					"lets the system apply §7.2's rule, because a role list here would be a "+
					"second answer to a question the system owns", len(intents))
			}

			if got := intents[0].intent.Role; got != testCase.wantRole {
				t.Errorf("the resolution ran with role %q, want the membership's own %q; the "+
					"route must pass the role through unchanged", got, testCase.wantRole)
			}
		})
	}
}
