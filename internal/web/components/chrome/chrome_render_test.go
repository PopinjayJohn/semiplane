package chrome_test

// These tests are UI §10.2 and §10.6, run against rendered fixtures: the
// structural accessibility contract, the closed interface vocabulary, and the
// target-size audit. They are the same three gates `components_test` runs for
// the pre-campaign shell, restated for the four landmarks this package owns and
// asserted over a *whole document* rather than over one component — because
// every property that matters here is a property of a composition. One `<h1>`,
// distinct landmark labels and the compact bar's second `nav` only exist when the
// four landmarks are in the same page.
//
// Everything is parsed into a DOM with `golang.org/x/net/html` rather than
// matched as substrings, and the reason is specific rather than stylistic: a
// substring assertion is how "no world in the interface" passes while the string
// sits in an HTML comment, in an attribute nothing renders, or in a `title` no
// reader sees. A parsed document can be walked, and every text node and every
// attribute value is reachable from it.

import (
	"slices"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/web/components/chrome"
)

// render turns a component into the markup a browser would receive.
func render(t *testing.T, component templ.Component) string {
	t.Helper()

	var out strings.Builder
	if err := component.Render(t.Context(), &out); err != nil {
		t.Fatalf("render component: %v", err)
	}

	return out.String()
}

// The centre slot, which the chrome does not own, stubbed so a composition has
// something in it. A shell is a document, and these assertions are about
// documents.
//
// The stub carries `class="shell-main target"` because the target-size audit
// below walks every element with a tabindex and this one has it, which is the
// same finding the integration report makes about `components/shell.templ`:
// a skip target is in the audit's element set.
const centreSlot = `<main id="main" class="shell-main target" role="main" tabindex="-1" data-testid="shell-main">` +
	`<h1 data-testid="centre-heading">A Page</h1></main>`

// The skip links, in UI §7.2's order. They belong to the shell rather than to
// the chrome — `components/shell.templ` owns them and this work item does not
// edit it — and they are in the composition because the two properties that
// concern the chrome are that the links which point at the navigation and the
// rail *resolve*, and that nothing the chrome renders precedes them.
func skipLinks(withNav bool) templ.Component {
	links := `<a class="skip-link target" href="#main" data-testid="skip-to-content">Skip to content</a>`
	if withNav {
		links += `<a class="skip-link target" href="#nav" data-testid="skip-to-campaign">` +
			`Skip to campaign navigation</a>`
	}

	return templ.Raw(
		links + `<a class="skip-link target" href="#rail" data-testid="skip-to-utilities">` +
			`Skip to utilities</a>`,
	)
}

// oneRailPanel stands in for the rail's route-specific panels. §4.4 gives this
// slot a different set per route, and §4.5 makes a panel absent for a viewer
// below its tier by the caller omitting it, so the chrome's half is that it
// wraps whatever it is given and claims nothing.
func oneRailPanel() templ.Component {
	return templ.Raw(
		`<section class="panel" data-testid="rail-panel"><h2>Page outline</h2></section>`,
	)
}

// chromeFixture is one whole-chrome composition. The four landmarks plus the
// centre slot, which is the only shape in which "exactly one <h1>" and "distinct
// landmark labels" mean anything.
type chromeFixture struct {
	name   string
	header chrome.HeaderView
	nav    *chrome.NavView
	footer chrome.FooterView
	// wantNav is the campaign variant. False is the pre-campaign one, and the
	// difference is structural rather than a flag on the navigation: §4.6
	// removes it, so a composition without a campaign has no navigation
	// landmark at all.
	wantNav bool
	// wantLive is whether the route is live, and therefore whether the document
	// carries a live region. §7.5's search rule is the case that matters.
	wantLive bool
	// testIDs are the hooks this composition must carry, listed per fixture so a
	// state that loses its own hook fails as itself.
	testIDs []string
	// noTestID are the hooks this composition must *not* carry. Absence is
	// §4.5's rule for a panel a viewer is not entitled to and §4.3's rule for an
	// account zone nobody is signed in to, and neither is checkable by looking
	// for what is there.
	noTestID []string
}

// campaignNav is the navigation a Game Master sees: every section present.
func campaignNav() *chrome.NavView {
	return &chrome.NavView{
		Campaigns: []chrome.CampaignRef{
			{Name: "Greyhaven", Slug: "greyhaven"},
			{Name: "Saltmarsh", Slug: "saltmarsh"},
		},
		Wiki: &chrome.PageNode{
			Title: "The Vault",
			Children: []chrome.PageNode{
				{Title: "Wards", Href: "/c/greyhaven/wiki/Vault/Wards", Kind: "Location"},
				{
					Title: "Wards",
					Href:  "/c/greyhaven/wiki/Vault/Inner",
					Children: []chrome.PageNode{
						{Title: "Deep", Href: "/c/greyhaven/wiki/Vault/Inner/Deep"},
					},
				},
			},
		},
		TableHref:  "/c/greyhaven/play",
		SearchHref: "/c/greyhaven/search",
		AdminHref:  "/c/greyhaven/settings",
	}
}

// playerNav is what a player sees: the page tree, a link to the table and a link
// to search, and nothing else. The settings address is empty rather than the
// destination being disabled, which is §4.3's rule and the whole of the GM-only
// surface.
func playerNav() *chrome.NavView {
	return &chrome.NavView{
		Wiki: &chrome.PageNode{
			Title:    "The Vault",
			Children: []chrome.PageNode{{Title: "Wards", Href: "/c/greyhaven/wiki/Wards"}},
		},
		TableHref:  "/c/greyhaven/play",
		SearchHref: "/c/greyhaven/search",
	}
}

func chromeFixtures() []chromeFixture {
	campaign := chrome.HeaderView{
		InstanceName: "Greyhaven",
		Account:      chrome.AccountView{Username: "mira", SignOutHref: "/logout"},
		Campaign:     chrome.CampaignRef{Name: "Greyhaven", Slug: "greyhaven"},
		Search:       chrome.SearchForm{Action: "/c/greyhaven/search", Query: "goblin"},
		Connection:   chrome.ConnectionLive,
	}

	return []chromeFixture{
		{
			name:     "campaign/game-master",
			header:   campaign,
			nav:      campaignNav(),
			wantNav:  true,
			wantLive: true,
			footer: chrome.FooterView{
				Version:    "0.3.0",
				StatusHref: "/c/greyhaven/status",
				Latency:    "18 ms",
				Degraded: []chrome.DegradedView{
					{
						Name:   "Content watcher",
						Detail: "the campaign's directory is no longer being watched",
					},
				},
				Bar: chrome.BarDestinations{
					Wiki:   "/c/greyhaven/wiki",
					Search: "/c/greyhaven/search",
					Table:  "/c/greyhaven/play",
					More:   "/c/greyhaven/settings",
				},
			},
			testIDs: []string{
				"shell-header", "header-campaign", "header-search", "header-search-input",
				"header-connection", "header-theme", "header-account", "header-sign-out",
				"shell-nav", "nav-collapse", "nav-destinations", "nav-campaigns",
				"nav-wiki", "nav-table", "nav-search", "nav-admin",
				"shell-rail", "rail-panels",
				"shell-footer", "footer-version", "footer-status-link", "footer-latency",
				"footer-degraded", "footer-bar",
			},
		},
		{
			// The search route. §7.5: search is submit-to-navigate over ordinary
			// HTTP, not content delivery, so nothing on the surface announces
			// anything — including the connection, which is why the header's
			// live region is keyed off a value the caller leaves at its zero.
			name: "campaign/search",
			header: chrome.HeaderView{
				Account:  chrome.AccountView{Username: "mira"},
				Campaign: chrome.CampaignRef{Name: "Greyhaven", Slug: "greyhaven"},
				Search:   chrome.SearchForm{Action: "/c/greyhaven/search", Query: "goblin"},
			},
			nav: playerNav(),
			// wantLive is false and the header carries ConnectionNone: the route
			// is not live, so the connection element is not in the document.
			wantNav:  true,
			wantLive: false,
			footer: chrome.FooterView{
				Version: "0.3.0",
				Bar: chrome.BarDestinations{
					Wiki:   "/c/greyhaven/wiki",
					Search: "/c/greyhaven/search",
					Table:  "/c/greyhaven/play",
				},
			},
			testIDs: []string{
				"shell-header", "header-campaign", "header-search", "header-theme",
				"shell-nav", "nav-table", "nav-search",
				"shell-footer", "footer-bar",
			},
		},
		{
			// The table route, where the footer's four destinations are replaced
			// by the player's row and the rail holds the initiative tracker.
			name:   "campaign/table",
			header: campaign,
			nav:    campaignNav(),
			// A GM, and a Game Master: a player gets no settings destination and
			// no edit affordance anywhere in the DOM (UI §4.3).
			footer: chrome.FooterView{
				Version: "0.3.0",
				Play: &chrome.PlayBar{
					Clock:      "Day 2, 19:40",
					Connection: chrome.ConnectionLive,
					LeaveHref:  "/c/greyhaven",
				},
			},
			wantNav:  true,
			wantLive: true,
			testIDs: []string{
				"shell-header", "header-connection", "header-account",
				"shell-nav", "nav-table", "nav-admin",
				"shell-footer", "footer-play", "footer-clock", "footer-play-connection",
				"footer-leave",
			},
		},
		{
			// An unknown gameplay system (plan §10.8, UI §4.7): the wiki still
			// serves and the Table link is absent, not disabled.
			name: "campaign/system-not-installed",
			header: chrome.HeaderView{
				Account:  chrome.AccountView{Username: "mira"},
				Campaign: chrome.CampaignRef{Name: "Greyhaven", Slug: "greyhaven"},
				Search:   chrome.SearchForm{Action: "/c/greyhaven/search"},
			},
			nav: &chrome.NavView{
				Wiki: &chrome.PageNode{
					Title:    "The Vault",
					Children: []chrome.PageNode{{Title: "Wards", Href: "/c/greyhaven/wiki/Wards"}},
				},
				SearchHref: "/c/greyhaven/search",
			},
			wantNav: true,
			footer:  chrome.FooterView{Version: "0.3.0"},
			testIDs: []string{
				"shell-nav", "nav-wiki", "nav-search",
				"shell-footer", "footer-version",
			},
		},
		{
			// §4.6: before a campaign exists the navigation is absent and the
			// other three landmarks all render.
			name: "pre-campaign",
			header: chrome.HeaderView{
				InstanceName: "Greyhaven",
				Account:      chrome.AccountView{Username: "mira", SignOutHref: "/logout"},
			},
			footer: chrome.FooterView{Version: "0.3.0", StatusHref: "/status"},
			// wantNav false: there is no pre-campaign nav to render empty.
			wantNav: false,
			testIDs: []string{
				"shell-header", "header-home", "header-theme", "header-account",
				"shell-rail", "rail-panels",
				"shell-footer", "footer-version", "footer-status-link",
			},
		},
		{
			// A reader who is not signed in, on the page they reach because they
			// are not. No account zone, and the brand link is the header's only
			// identity.
			name:     "pre-campaign/anonymous",
			header:   chrome.HeaderView{},
			footer:   chrome.FooterView{},
			wantNav:  false,
			wantLive: false,
			testIDs:  []string{"shell-header", "header-home", "shell-rail", "shell-footer"},
			noTestID: []string{
				"header-account",
				"header-sign-out",
				"footer-version",
				"footer-bar",
				"header-search",
			},
		},
	}
}

// forbiddenVocabulary is the interface's closed vocabulary (UI §1.2). Neither
// entity exists, so a leftover is not a synonym — it sends a reader looking for
// a feature that is not there.
var forbiddenVocabulary = []string{"world", "session"}

// render assembles a composition: the skip links, then the four landmarks around
// the centre slot, in the order `components/shell.templ` uses.
func (fixture chromeFixture) render(t *testing.T) string {
	t.Helper()

	var out strings.Builder
	out.WriteString(`<!DOCTYPE html><html lang="en"><head><title>A Page</title></head><body>`)

	write := func(component templ.Component) {
		var piece strings.Builder
		if err := component.Render(t.Context(), &piece); err != nil {
			t.Fatalf("render composition: %v", err)
		}

		out.WriteString(piece.String())
	}

	write(skipLinks(fixture.wantNav))
	write(chrome.Header(fixture.header))
	if fixture.wantNav {
		write(chrome.CampaignNav(*fixture.nav))
	}

	write(templ.Raw(centreSlot))
	write(chrome.UtilitiesRail(oneRailPanel()))
	write(chrome.Footer(fixture.footer))
	out.WriteString(`</body></html>`)

	return out.String()
}

// parsed returns a composition as a DOM, so every assertion below reads the
// document rather than a substring of it.
func (fixture chromeFixture) parsed(t *testing.T) *html.Node {
	t.Helper()

	document, err := html.Parse(strings.NewReader(fixture.render(t)))
	if err != nil {
		t.Fatalf("parse composition: %v", err)
	}

	return document
}

// forEachElement walks every element in document order.
func forEachElement(root *html.Node, visit func(*html.Node)) {
	if root.Type == html.ElementNode {
		visit(root)
	}

	for child := root.FirstChild; child != nil; child = child.NextSibling {
		forEachElement(child, visit)
	}
}

// attribute returns a node's attribute value, or the empty string. An attribute
// present with no value parses to the empty string, so presence is asked about
// with hasAttribute and never inferred from this.
func attribute(node *html.Node, name string) string {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}

	return ""
}

// hasAttribute reports whether an attribute is present at all, which is the only
// question a boolean attribute like `data-shell-rail` can answer.
func hasAttribute(node *html.Node, name string) bool {
	for _, attr := range node.Attr {
		if attr.Key == name {
			return true
		}
	}

	return false
}

// hasClass reports whether a node carries a class, token by token rather than by
// substring: `.target` is a substring of `.targets-are-mine`, and a substring
// check is how the audit passes on a class that is not there.
func hasClass(node *html.Node, class string) bool {
	return slices.Contains(slices.Collect(strings.FieldsSeq(attribute(node, "class"))), class)
}

// byRole returns every element with an explicit ARIA role, in document order.
func byRole(root *html.Node, role string) []*html.Node {
	var found []*html.Node

	forEachElement(root, func(node *html.Node) {
		if attribute(node, "role") == role {
			found = append(found, node)
		}
	})

	return found
}

// byTestID returns the single element carrying a test hook.
func byTestID(t *testing.T, root *html.Node, id string) *html.Node {
	t.Helper()

	var found []*html.Node

	forEachElement(root, func(node *html.Node) {
		if attribute(node, "data-testid") == id {
			found = append(found, node)
		}
	})

	if len(found) != 1 {
		t.Fatalf("data-testid=%q appears %d times, want exactly 1", id, len(found))
	}

	return found[0]
}

// byID returns the single element carrying a DOM id.
func byID(t *testing.T, root *html.Node, id string) *html.Node {
	t.Helper()

	var found []*html.Node

	forEachElement(root, func(node *html.Node) {
		if attribute(node, "id") == id {
			found = append(found, node)
		}
	})

	if len(found) != 1 {
		t.Fatalf("id=%q appears %d times, want exactly 1", id, len(found))
	}

	return found[0]
}

// hasTestID reports whether a hook appears anywhere in the document.
func hasTestID(root *html.Node, id string) bool {
	var found bool

	forEachElement(root, func(node *html.Node) {
		if attribute(node, "data-testid") == id {
			found = true
		}
	})

	return found
}

// --- The closed vocabulary, UI §1.2 -----------------------------------------

// TestChromeNeverNamesWorldOrSession is the gate §1.2 asks for and §10.2 makes
// blocking, over every text node and every attribute value in a parsed
// document.
//
// Every one of the two, not only the text a reader sees. An `aria-label` is read
// aloud, a `title` is announced by some readers, and a `data-` attribute is read
// by nothing at all — which is precisely why a copy-paste from an old branch
// survives in one and not the other. Substring and case-insensitive, so the
// plural and a capitalised "World" inside a campaign name are both caught: a
// reader would be looking for a feature that does not exist.
func TestChromeNeverNamesWorldOrSession(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			forEachElement(fixture.parsed(t), func(node *html.Node) {
				if node.Type == html.TextNode {
					assertClean(t, "text", node.Data)
				}

				for _, attr := range node.Attr {
					assertClean(t, attr.Key, attr.Val)
				}
			})
		})
	}
}

// assertClean fails on a forbidden word, naming where it was found so the report
// points at an attribute rather than at a whole document.
func assertClean(t *testing.T, where, text string) {
	t.Helper()

	lowered := strings.ToLower(text)
	for _, forbidden := range forbiddenVocabulary {
		if strings.Contains(lowered, forbidden) {
			t.Errorf("%s contains %q; neither entity exists in the interface and a "+
				"leftover is a bug (UI §1.2)", where, forbidden)
		}
	}
}

// --- Structure, UI §7.2 and §10.2 -------------------------------------------

// TestChromeCompositionHasExactlyOneH1: the centre slot owns the page heading, and
// none of the four landmarks adds one.
//
// This is the assertion that catches a panel growing a heading of its own at the
// wrong level, which is the most likely way a route grows a second `<h1>`.
func TestChromeCompositionHasExactlyOneH1(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			var headings []*html.Node
			forEachElement(fixture.parsed(t), func(node *html.Node) {
				if node.Data == "h1" {
					headings = append(headings, node)
				}
			})

			if len(headings) != 1 {
				t.Errorf(
					"composition has %d <h1> elements, want exactly 1 (UI §7.2)",
					len(headings),
				)
			}
		})
	}
}

// TestChromeCompositionSkipsNoHeadingLevel: a landmark that starts a subheading at
// h3 under the centre's h1 is a level skip, and §7.2 forbids one however
// harmless the panel looks.
func TestChromeCompositionSkipsNoHeadingLevel(t *testing.T) {
	t.Parallel()

	levels := map[string]int{"h1": 1, "h2": 2, "h3": 3, "h4": 4, "h5": 5, "h6": 6}

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			previous := 0
			forEachElement(fixture.parsed(t), func(node *html.Node) {
				level, isHeading := levels[node.Data]
				if !isHeading {
					return
				}

				if previous != 0 && level > previous+1 {
					t.Errorf("<%s> follows a level %d heading, which is a skip (UI §7.2)",
						node.Data, previous)
				}

				previous = level
			})
		})
	}
}

// TestChromeCompositionHasNoPositiveTabindex: §7.4 and §7.10 prohibit a positive
// tabindex outright, and -1 is the skip links' mechanism, so the only value
// allowed anywhere in the chrome is -1.
func TestChromeCompositionHasNoPositiveTabindex(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			forEachElement(fixture.parsed(t), func(node *html.Node) {
				value := attribute(node, "tabindex")
				if value == "" {
					return
				}

				if value != "-1" {
					t.Errorf("<%s> carries tabindex=%q; only -1 is allowed (UI §7.4, §7.10)",
						node.Data, value)
				}
			})
		})
	}
}

// TestEveryFocusableElementCarriesTheTargetClass is §10.6's audit, run over the
// parsed document: every link, button, field, `<summary>` and tabindexed element
// carries `.target`, which is where §7.3's minimum inline and block size come
// from.
//
// §4.11.1 is why this is a test rather than a review note — the same audit runs
// over plugin output, and a plugin component is not exempt. An `<input
// type="hidden">` is excluded because it is not focusable and never was, and an
// `<a>` without an `href` for the same reason.
func TestEveryFocusableElementCarriesTheTargetClass(t *testing.T) {
	t.Parallel()

	elements := map[string]bool{
		"a": true, "button": true, "input": true,
		"select": true, "textarea": true, "summary": true,
	}

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			forEachElement(fixture.parsed(t), func(node *html.Node) {
				if !elements[node.Data] && attribute(node, "tabindex") == "" {
					return
				}

				if node.Data == "input" && attribute(node, "type") == "hidden" {
					return
				}

				if node.Data == "a" && attribute(node, "href") == "" {
					return
				}

				if !hasClass(node, "target") {
					t.Errorf("<%s> is focusable and does not carry the .target class, so "+
						"§7.3's minimum size is not enforced on it (UI §10.6)",
						node.Data)
				}
			})
		})
	}
}

// TestChromeCompositionCarriesItsTestHooks: every element under test has a hook,
// and a hook appears once, so "the first one" is never the answer. The absent
// list is the other half — §4.5 and §4.3 are rules about what is *not* in the
// document.
func TestChromeCompositionCarriesItsTestHooks(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := fixture.parsed(t)

			seen := map[string]int{}
			forEachElement(document, func(node *html.Node) {
				if id := attribute(node, "data-testid"); id != "" {
					seen[id]++
				}
			})

			for id, count := range seen {
				if count > 1 {
					t.Errorf(
						"data-testid=%q appears %d times; a repeated hook selects nothing",
						id,
						count,
					)
				}
			}

			for _, id := range fixture.testIDs {
				if !hasTestID(document, id) {
					t.Errorf("composition is missing data-testid=%q", id)
				}
			}

			for _, id := range fixture.noTestID {
				if hasTestID(document, id) {
					t.Errorf("composition renders data-testid=%q; it should be absent", id)
				}
			}
		})
	}
}

// --- Landmarks, UI §4.5, §4.6, §7.2 ------------------------------------------

// TestChromeCompositionCarriesItsLandmarks asserts the four roles as attributes,
// including the two HTML implies implicitly, because the audit is a read of the
// markup and a markup implication is not a markup assertion.
func TestChromeCompositionCarriesItsLandmarks(t *testing.T) {
	t.Parallel()

	required := []string{"banner", "main", "contentinfo", "complementary"}

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := fixture.parsed(t)

			for _, role := range required {
				if len(byRole(document, role)) != 1 {
					t.Errorf("composition has %d elements with role=%q, want exactly 1 "+
						"(UI §4.5, §4.6)", len(byRole(document, role)), role)
				}
			}
		})
	}
}

// TestChromeLandmarksCarryTheirExactLabels is the §10.2 half of the landmark
// rule that is easy to get wrong: "Campaign" and "Utilities" are strings the spec
// fixes, and a paraphrase is a failure even though the landmark is there.
//
// Selected by hook rather than by role, because a document has *two* navigation
// landmarks on a campaign route and only one of them is the campaign navigation —
// the other is the compact bar, and asserting a role's label without saying which
// element is meant is how "Primary" ends up asserted to equal "Campaign".
func TestChromeLandmarksCarryTheirExactLabels(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := fixture.parsed(t)

			// The navigation is checked in
			// TestChromeNavigationIsAbsentBeforeACampaign: on a pre-campaign route
			// there is none, and byID would report that as a missing element rather
			// than as the absence the spec asks for.
			if fixture.wantNav {
				nav := byTestID(t, document, "shell-nav")
				if got := attribute(nav, "role"); got != "navigation" {
					t.Errorf("the campaign navigation has role=%q, want navigation (UI §4.3)", got)
				}

				if got := attribute(nav, "aria-label"); got != "Campaign" {
					t.Errorf(
						"the campaign navigation is labelled %q, want Campaign (UI §4.3, §7.2)",
						got,
					)
				}
			}

			rail := byTestID(t, document, "shell-rail")
			if got := attribute(rail, "role"); got != "complementary" {
				t.Errorf("the utilities rail has role=%q, want complementary (UI §4.5)", got)
			}

			if got := attribute(rail, "aria-label"); got != "Utilities" {
				t.Errorf("the utilities rail is labelled %q, want Utilities (UI §4.5)", got)
			}
		})
	}
}

// TestRepeatedLandmarkRolesAreDistinctlyLabelled is §7.2's own sentence — "both
// must carry distinguishing labels" — as a property of the document rather than
// of either component.
//
// It is the assertion that catches two navigation landmarks both called
// "Navigation", which makes landmark navigation useless and which §10.2 calls a
// gate failure. The compact bar is a second `nav` at every tier (UI §4.2), so the
// pair "Campaign" and "Primary" is load-bearing in the same way the campaign
// navigation's own label is.
func TestRepeatedLandmarkRolesAreDistinctlyLabelled(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := fixture.parsed(t)
			seen := map[string]string{}

			forEachElement(document, func(node *html.Node) {
				role := attribute(node, "role")
				if role == "" {
					return
				}

				label := attribute(node, "aria-label")
				if previous, repeated := seen[role]; repeated && previous == label {
					t.Errorf("two %s landmarks are both labelled %q; unlabelled or "+
						"duplicated labels make landmark navigation useless (UI §7.2)", role, label)
				}

				seen[role] = label
			})
		})
	}
}

// TestCampaignNavigationIsAbsentBeforeACampaign is §4.6 as an assertion of
// absence rather than of emptiness: a shell that could render an empty navigation
// would make "is this the pre-campaign shell?" a question with two right answers,
// and the content model of a campaign's navigation is undefined with zero
// campaigns.
func TestCampaignNavigationIsAbsentBeforeACampaign(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		if fixture.wantNav {
			continue
		}

		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := fixture.parsed(t)

			if found := byRole(document, "navigation"); len(found) != 0 {
				t.Errorf("a pre-campaign composition has %d navigation landmarks, want none "+
					"(UI §4.6)", len(found))
			}

			if hasTestID(document, "shell-nav") {
				t.Error("a pre-campaign composition renders the campaign navigation")
			}
		})
	}
}

// TestCampaignRoutesCarryTheNavigation, so the absence above cannot pass by the
// navigation simply never rendering.
func TestCampaignRoutesCarryTheNavigation(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		if !fixture.wantNav {
			continue
		}

		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := fixture.parsed(t)

			nav := byTestID(t, document, "shell-nav")
			if got := attribute(nav, "id"); got != "nav" {
				t.Errorf("the campaign navigation has id=%q; UI §7.2's third skip link points "+
					"at #nav", got)
			}
		})
	}
}

// TestSkipLinksResolveToLandmarks is the half of §7.2 the chrome is responsible
// for: a skip link to a landmark that is not there moves focus nowhere, and a
// landmark with no skip link is a landmark a keyboard cannot reach in one stop.
//
// The links themselves are the shell's — `components/shell.templ` owns them — so
// what is asserted is that the two ids this package declares, `#nav` and `#rail`,
// exist, are unique, and are the landmarks the link names.
func TestSkipLinksResolveToLandmarks(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := fixture.parsed(t)
			var links []*html.Node

			forEachElement(document, func(node *html.Node) {
				if node.Data == "a" && strings.HasPrefix(attribute(node, "href"), "#") {
					links = append(links, node)
				}
			})

			if len(links) < 2 {
				t.Fatalf("composition has %d skip links, want at least the content and "+
					"utilities ones", len(links))
			}

			for _, link := range links {
				target := strings.TrimPrefix(attribute(link, "href"), "#")
				resolved := byID(t, document, target)

				if got := attribute(resolved, "role"); got != roleOf(target) {
					t.Errorf("the skip link to #%s resolves to an element with role=%q, want %q "+
						"(UI §7.2)", target, got, roleOf(target))
				}
			}
		})
	}
}

// TestSkipLinksPrecedeTheBanner is §7.2's ordering rule, and the property of the
// chrome it actually protects: the landmarks are what follow the skip links, so a
// focusable element emitted outside a landmark — a portal row, a stray control —
// would land ahead of them and take the first tab stop from a reader who wanted
// to skip past it.
func TestSkipLinksPrecedeTheBanner(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			var order []string
			forEachElement(fixture.parsed(t), func(node *html.Node) {
				switch {
				case node.Data == "a" && strings.HasPrefix(attribute(node, "href"), "#"):
					order = append(order, "skip:"+strings.TrimPrefix(attribute(node, "href"), "#"))
				case attribute(node, "role") == "banner":
					order = append(order, "banner")
				}
			})

			if len(order) < 3 {
				t.Fatalf("composition has %d skip links and banners, want at least three "+
					"(UI §7.2)", len(order))
			}

			// UI §7.2 fixes the order: content, then the campaign navigation where
			// it exists, then the utilities. The banner is last of the four.
			want := []string{"skip:main", "skip:rail", "banner"}
			if fixture.wantNav {
				want = []string{"skip:main", "skip:nav", "skip:rail", "banner"}
			}

			for index, expected := range want {
				if order[index] != expected {
					t.Errorf("focusable landmark %d is %q, want %q (UI §7.2's order)",
						index+1, order[index], expected)
				}
			}
		})
	}
}

// roleOf is the landmark role an id belongs to, for the skip-link resolution
// check. Named rather than inlined so a reader can see which landmark each skip
// link is expected to reach.
func roleOf(id string) string {
	switch id {
	case "main":
		return "main"
	case "nav":
		return "navigation"
	case "rail":
		return "complementary"
	default:
		return ""
	}
}

// --- Live regions, UI §4.1, §6.5, §7.5 --------------------------------------

// TestOnlyALiveRouteCarriesALiveRegion is §7.5's search rule as a property of the
// document: search is submit-to-navigate over ordinary HTTP rather than content
// delivery, so nothing on the surface announces anything. The pre-campaign
// fixtures assert the same thing for the same reason — there is no connection to
// report on a route that has none.
func TestOnlyALiveRouteCarriesALiveRegion(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			document := fixture.parsed(t)
			regions := liveRegions(document)

			if fixture.wantLive && len(regions) == 0 {
				t.Error("a live route has no live region; §4.1 puts the connection " +
					"indicator in the header")
			}

			if !fixture.wantLive && len(regions) != 0 {
				t.Errorf("a route that is not live carries %d live region(s); the search "+
					"route has none at all (UI §7.5)", len(regions))
			}
		})
	}
}

// liveRegions returns every element that announces itself, whatever the
// mechanism: `role="status"`, `role="alert"`, or an explicit `aria-live`.
func liveRegions(root *html.Node) []*html.Node {
	var found []*html.Node

	forEachElement(root, func(node *html.Node) {
		role := attribute(node, "role")
		if role == "status" || role == "alert" || attribute(node, "aria-live") != "" {
			found = append(found, node)
		}
	})

	return found
}

// TestTheConnectionIndicatorIsAPoliteStatus pins the region §4.1 specifies for the
// banner's connection state, and the one §7.5's table does *not* mark assertive.
//
// The distinction is load-bearing rather than cosmetic. A connection change is
// ambient information about a page the reader is already on, and §7.4's rule
// that a background event must never steal focus extends to the ear: an
// assertive region would interrupt whatever was being read. The *loss* of a
// connection is a different announcement and §7.5 gives it `role="alert"` — but
// that one belongs to the table surface, not to the chrome, which is why the
// indicator is a status and not an alert.
func TestTheConnectionIndicatorIsAPoliteStatus(t *testing.T) {
	t.Parallel()

	document := chromeFixture{
		name:     "live",
		header:   chrome.HeaderView{Connection: chrome.ConnectionOffline},
		wantLive: true,
		footer:   chrome.FooterView{},
	}.parsed(t)

	indicator := byTestID(t, document, "header-connection")

	if got := attribute(indicator, "role"); got != "status" {
		t.Errorf("the connection indicator has role=%q, want status (UI §4.1)", got)
	}

	if got := attribute(indicator, "aria-live"); got != "polite" {
		t.Errorf("the connection indicator has aria-live=%q, want polite (UI §4.1, §7.5)", got)
	}
}

// TestTheConnectionIndicatorNamesItsState is §6.5's "never colour alone" for the
// one status the chrome owns: the state is a word, so hue is never the only
// carrier, and a reader who cannot see the indicator's colour still reads it.
//
// All three words from §4.1, so a reworded state is a failure rather than a
// variation. The offline case is the one that matters: it is the state in which a
// GM must know their dice are not landing.
func TestTheConnectionIndicatorNamesItsState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		state chrome.Connection
		want  string
	}{
		{chrome.ConnectionLive, "Live"},
		{chrome.ConnectionReconnecting, "Reconnecting"},
		{chrome.ConnectionOffline, "Offline"},
	}

	for _, testCase := range cases {
		t.Run(testCase.want, func(t *testing.T) {
			t.Parallel()

			document := chrome.HeaderView{Connection: testCase.state}
			var out strings.Builder
			if err := chrome.Header(document).Render(t.Context(), &out); err != nil {
				t.Fatalf("render header: %v", err)
			}

			parsed, err := html.Parse(strings.NewReader(out.String()))
			if err != nil {
				t.Fatalf("parse header: %v", err)
			}

			indicator := byTestID(t, parsed, "header-connection")
			if text := textOf(indicator); text != testCase.want {
				t.Errorf("the indicator reads %q, want %q (UI §4.1, §6.5)", text, testCase.want)
			}
		})
	}
}

// TestTheFooterDoesNotAnnounceWhatTheHeaderAnnounces is §9's row — a reconnect is
// signalled by the header indicator and nothing else — expressed as an
// assertion.
//
// The footer's row on the table route carries the connection state too, because
// §4.2's table lists it, and it is plain text: a second `role="status"` for the
// same fact would read it out twice on the one page where it happens most often.
func TestTheFooterDoesNotAnnounceWhatTheHeaderAnnounces(t *testing.T) {
	t.Parallel()

	footer := chrome.FooterView{
		Play: &chrome.PlayBar{
			Clock:      "Day 2, 19:40",
			Connection: chrome.ConnectionReconnecting,
			LeaveHref:  "/c/greyhaven",
		},
	}

	var out strings.Builder
	if err := chrome.Footer(footer).Render(t.Context(), &out); err != nil {
		t.Fatalf("render footer: %v", err)
	}

	parsed, err := html.Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("parse footer: %v", err)
	}

	if regions := liveRegions(parsed); len(regions) != 0 {
		t.Errorf("the footer's table row carries %d live region(s); the header indicator is "+
			"the only connection signal (UI §4.1, §9)", len(regions))
	}

	if text := textOf(byTestID(t, parsed, "footer-play-connection")); text != "Reconnecting" {
		t.Errorf("the footer's connection text reads %q, want %q (UI §4.2)", text, "Reconnecting")
	}
}

// TestTheDegradedWarningIsNotALiveRegion: §4.2 puts the degraded banner in the
// footer permanently, and a condition that persists until an operator fixes it is
// not an event. A live region would re-announce it on every navigation, for a
// state the reader can see.
func TestTheDegradedWarningIsNotALiveRegion(t *testing.T) {
	t.Parallel()

	footer := chrome.FooterView{
		Degraded: []chrome.DegradedView{
			{Name: "Content watcher", Detail: "its directory is unwatched"},
		},
	}

	var out strings.Builder
	if err := chrome.Footer(footer).Render(t.Context(), &out); err != nil {
		t.Fatalf("render footer: %v", err)
	}

	parsed, err := html.Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("parse footer: %v", err)
	}

	if regions := liveRegions(parsed); len(regions) != 0 {
		t.Errorf("the degraded warning carries %d live region(s); it is a standing "+
			"condition, not an announcement (UI §4.2)", len(regions))
	}

	// §6.5 again: each subsystem is named, and the name is never a colour.
	warning := byTestID(t, parsed, "footer-degraded")
	if !strings.Contains(textOf(warning), "Content watcher") {
		t.Errorf("the degraded warning does not name the subsystem:\n%s", textOf(warning))
	}

	if !strings.Contains(textOf(warning), "its directory is unwatched") {
		t.Errorf("the degraded warning drops the detail clause:\n%s", textOf(warning))
	}
}

// --- The header, UI §4.1 ----------------------------------------------------

// TestTheHeaderNamesTheCurrentLocation: a campaign route's banner names the
// campaign, and a pre-campaign one names the instance. §8.3's rank 1, and the
// reason the brand link does not render twice — inside a campaign the campaign
// name already answers "where am I?".
func TestTheHeaderNamesTheCurrentLocation(t *testing.T) {
	t.Parallel()

	t.Run("inside a campaign", func(t *testing.T) {
		t.Parallel()

		document := header(t, chrome.HeaderView{
			InstanceName: "Greyhaven",
			Campaign:     chrome.CampaignRef{Name: "Greyhaven", Slug: "greyhaven"},
		})

		link := byTestID(t, document, "header-campaign")
		if got := attribute(link, "href"); got != "/c/greyhaven" {
			t.Errorf("the campaign link points at %q, want /c/greyhaven", got)
		}

		if hasTestID(document, "header-home") {
			t.Error("the banner renders the instance brand inside a campaign; §8.3's rank 1 " +
				"is the campaign name")
		}
	})

	t.Run("before a campaign", func(t *testing.T) {
		t.Parallel()

		document := header(t, chrome.HeaderView{InstanceName: "Greyhaven"})

		link := byTestID(t, document, "header-home")
		if got := attribute(link, "href"); got != "/" {
			t.Errorf("the brand link points at %q, want /", got)
		}

		if hasTestID(document, "header-campaign") {
			t.Error("a pre-campaign banner names a campaign")
		}
	})

	t.Run("an unnamed instance still names itself", func(t *testing.T) {
		t.Parallel()

		// An operator reaches an unconfigured instance before any name is set, and
		// a blank slot in the header is the first thing a new install looks like.
		document := header(t, chrome.HeaderView{})

		if text := textOf(byTestID(t, document, "header-home")); text != "semiplane" {
			t.Errorf("the brand reads %q, want semiplane", text)
		}
	})
}

// TestTheSearchZoneIsAFormOverOrdinaryHTTP is §7.5's submit-to-navigate and §4.1's
// `role="search"` in one assertion, plus the two rules that make a header form
// correct: a `GET`, so the query is a URL and the response is cacheable, and a
// label rather than a placeholder.
func TestTheSearchZoneIsAFormOverOrdinaryHTTP(t *testing.T) {
	t.Parallel()

	document := header(t, chrome.HeaderView{
		Campaign: chrome.CampaignRef{Name: "Greyhaven", Slug: "greyhaven"},
		Search:   chrome.SearchForm{Action: "/c/greyhaven/search", Query: "goblin"},
	})

	form := byTestID(t, document, "header-search")

	if got := attribute(form, "role"); got != "search" {
		t.Errorf("the search form has role=%q, want search (UI §4.1)", got)
	}

	// A named landmark: the centre slot of the search route is also a search
	// form, and two identically-labelled search landmarks fail §7.2 the same way
	// two identically-labelled navigations do.
	if got := attribute(form, "aria-label"); got != "Search pages" {
		t.Errorf("the search form is labelled %q, want a label distinct from the centre "+
			"slot's own (UI §7.2)", got)
	}

	if got := attribute(form, "method"); got != "get" {
		t.Errorf("the search form uses method=%q, want get; search is a URL, not a fetch "+
			"(UI §7.5)", got)
	}

	if got := attribute(form, "action"); got != "/c/greyhaven/search" {
		t.Errorf("the search form posts to %q, want /c/greyhaven/search (UI §4.1)", got)
	}

	field := byTestID(t, document, "header-search-input")
	if got := attribute(field, "type"); got != "search" {
		t.Errorf("the field has type=%q, want search (UI §4.1)", got)
	}

	if got := attribute(field, "name"); got != "q" {
		t.Errorf("the field is named %q, want q (UI §4.1)", got)
	}

	// §7.7: a real `<label for>`, which is why the header needs a visually-hidden
	// label utility and not a placeholder.
	if !hasLabel(document, attribute(field, "id")) {
		t.Errorf("the search field has no <label for=%q>; a placeholder is not a label "+
			"(UI §7.7)", attribute(field, "id"))
	}

	if got := attribute(field, "value"); got != "goblin" {
		t.Errorf("the field carries %q, want the submitted query echoed back", got)
	}
}

// TestTheSearchZoneIsAbsentWhereThereIsNothingToSearch: before a campaign exists
// there is no campaign to search, and a field that searches nothing is a focus
// stop in the one place a reader looks for the answer.
func TestTheSearchZoneIsAbsentWhereThereIsNothingToSearch(t *testing.T) {
	t.Parallel()

	document := header(t, chrome.HeaderView{InstanceName: "Greyhaven"})

	if hasTestID(document, "header-search") {
		t.Error("a pre-campaign banner renders a search form")
	}

	if found := byRole(document, "search"); len(found) != 0 {
		t.Errorf("a pre-campaign banner has %d search landmarks, want none", len(found))
	}
}

// TestTheThemeButtonNamesTheResult: §4.1's rule for this control, which is a
// disclosure of state and not an action. A button reading "Switch theme" tells a
// reader nothing about where they are, and a cycling button has no other way to
// say.
//
// `aria-pressed` is false for auto and true for an explicit choice, because auto
// is the absence of a choice: what is "on" in the auto state is the operating
// system's polarity, which the reader did not set here.
func TestTheThemeButtonNamesTheResult(t *testing.T) {
	t.Parallel()

	cases := []struct {
		theme   chrome.Theme
		want    string
		pressed string
	}{
		{chrome.ThemeAuto, "Theme: auto", "false"},
		{chrome.ThemeLight, "Theme: light", "true"},
		{chrome.ThemeDark, "Theme: dark", "true"},
	}

	for _, testCase := range cases {
		t.Run(testCase.want, func(t *testing.T) {
			t.Parallel()

			button := byTestID(
				t,
				header(t, chrome.HeaderView{Theme: testCase.theme}),
				"header-theme",
			)

			if got := textOf(button); got != testCase.want {
				t.Errorf("the theme button reads %q, want %q (UI §4.1)", got, testCase.want)
			}

			if got := attribute(button, "aria-pressed"); got != testCase.pressed {
				t.Errorf("the theme button has aria-pressed=%q, want %q (UI §4.1)", got,
					testCase.pressed)
			}
		})
	}
}

// TestTheAccountZoneIsPresentOnlyForASignedInReader: a sign-out button on the
// page a reader reaches precisely because they are not signed in is a control
// that cannot help.
func TestTheAccountZoneIsPresentOnlyForASignedInReader(t *testing.T) {
	t.Parallel()

	t.Run("signed in", func(t *testing.T) {
		t.Parallel()

		document := header(t, chrome.HeaderView{
			Account: chrome.AccountView{Username: "mira", SignOutHref: "/logout"},
		})

		zone := byTestID(t, document, "header-account")
		if !strings.Contains(textOf(zone), "mira") {
			t.Errorf("the account zone does not name the reader:\n%s", textOf(zone))
		}

		form := ancestorWithTag(byTestID(t, document, "header-sign-out"), "form")
		if form == nil {
			t.Fatal("the sign-out control is not inside a form")
		}

		// A POST, not a link: signing out changes state, and a link that does is a
		// prefetch a browser and a proxy may both follow.
		if got := attribute(form, "method"); got != "post" {
			t.Errorf("the sign-out form uses method=%q, want post", got)
		}

		if got := attribute(form, "action"); got != "/logout" {
			t.Errorf("the sign-out form posts to %q, want /logout", got)
		}
	})

	t.Run("signed out", func(t *testing.T) {
		t.Parallel()

		document := header(t, chrome.HeaderView{})

		if hasTestID(document, "header-account") {
			t.Error("a banner for an anonymous reader renders an account zone")
		}
	})

	t.Run("signed in with nowhere to sign out to", func(t *testing.T) {
		t.Parallel()

		// A button that posts nowhere is a focus stop that does nothing, which is
		// worse than no button: the identity is still shown.
		document := header(t, chrome.HeaderView{
			Account: chrome.AccountView{Username: "mira"},
		})

		byTestID(t, document, "header-account")

		if hasTestID(document, "header-sign-out") {
			t.Error("a banner with no sign-out address renders a sign-out button")
		}
	})
}

// --- The navigation, UI §4.3 ------------------------------------------------

// TestTableIsOneLinkAndNeverAList: a campaign has at most one live tabletop, so
// there is nothing to enumerate, and UI §1.2 names the absence of a Games list as
// a hard rule. The assertion is structural — one link, and a list element that
// contains it — so a second tabletop added to the model would fail here rather
// than render.
func TestTableIsOneLinkAndNeverAList(t *testing.T) {
	t.Parallel()

	document := renderNav(t, campaignNav())

	var tables []*html.Node
	forEachElement(document, func(node *html.Node) {
		if hasClass(node, "nav-link") && attribute(node, "href") == "/c/greyhaven/play" {
			tables = append(tables, node)
		}
	})

	if len(tables) != 1 {
		t.Fatalf(
			"the navigation has %d links to the table, want exactly 1 (UI §1.2, §4.3)",
			len(tables),
		)
	}

	row := ancestorWithTag(tables[0], "li")
	if row == nil {
		t.Fatal("the table link is not a list row, so the navigation is not a list at all")
	}

	forEachElement(row, func(node *html.Node) {
		if node.Data == "ul" {
			t.Error("the table link's row contains a list; it is a single destination, and a " +
				"list is what a list of tables would need (UI §1.2, §4.3)")
		}
	})
}

// TestTheNavigationRendersNoEditAffordanceForANonGM: §4.3's rule is "absent, not
// disabled", so the assertion is about what a player is *not* given — a settings
// destination, an address to a GM-only route, and any control at all in the
// navigation beyond a link and a disclosure.
//
// This is the shell's half of §4.5's server-side absence, and it is why the
// model's field is an address rather than a flag: a template cannot disable what
// it was never given.
func TestTheNavigationRendersNoEditAffordanceForANonGM(t *testing.T) {
	t.Parallel()

	document := renderNav(t, playerNav())

	if hasTestID(document, "nav-admin") {
		t.Error("a player is given the settings destination")
	}

	forEachElement(document, func(node *html.Node) {
		if node.Data == "a" && strings.Contains(attribute(node, "href"), "/settings") {
			t.Errorf("a player is given a link to a GM-only route: %q", attribute(node, "href"))
		}
	})
}

// TestEveryNavigationDisclosureStartsOpen is the package's one rule about
// controls, asserted: nothing in the chrome renders in a state that needs the
// client layer to become useful.
//
// A disclosure rendered collapsed over a list that is not there is a promise the
// template cannot keep until phase 9, so every one of them starts expanded and
// the list is in the document either way. The second half of the assertion is
// that `aria-controls` names an element that exists — a disclosure pointing at
// nothing reveals nothing while claiming otherwise.
func TestEveryNavigationDisclosureStartsOpen(t *testing.T) {
	t.Parallel()

	document := renderNav(t, campaignNav())

	disclosures := 0
	forEachElement(document, func(node *html.Node) {
		if attribute(node, "data-chrome") != "disclosure" {
			return
		}

		disclosures++

		if got := attribute(node, "aria-expanded"); got != "true" {
			t.Errorf("a disclosure has aria-expanded=%q; the chrome renders the state that "+
				"works without a client (UI §4.3)", got)
		}

		byID(t, document, attribute(node, "aria-controls"))
	})

	if disclosures == 0 {
		t.Fatal("no disclosure rendered; the Wiki section is a folder and the fixture has one")
	}
}

// TestTheCollapseControlStartsExpanded, for the same reason, plus the width
// contract it stands for: at the icon rail the destinations do not disappear, and
// an expanded control over hidden destinations is a reader with no way back.
func TestTheCollapseControlStartsExpanded(t *testing.T) {
	t.Parallel()

	document := renderNav(t, campaignNav())
	control := byTestID(t, document, "nav-collapse")

	if got := attribute(control, "aria-expanded"); got != "true" {
		t.Errorf("the collapse control has aria-expanded=%q, want true (UI §4.3)", got)
	}

	byID(t, document, attribute(control, "aria-controls"))
}

// TestEveryNavigationRowKeepsItsName is what makes the icon rail affordable:
// §4.3 collapses the navigation to 3.5rem of icons, and if the name lived only in
// an `aria-label` that the stylesheet hid there would be a name gone. Every row
// keeps its name as a text node, so the stylesheet can hide the pixels and never
// the word.
func TestEveryNavigationRowKeepsItsName(t *testing.T) {
	t.Parallel()

	document := renderNav(t, campaignNav())

	forEachElement(document, func(node *html.Node) {
		if !hasClass(node, "nav-link") && !hasClass(node, "nav-disclosure") &&
			!hasClass(node, "nav-collapse") {
			return
		}

		if strings.TrimSpace(textOf(node)) == "" {
			t.Errorf("a navigation row has no text name; the icon rail would take it away " +
				"(UI §4.3)")
		}
	})
}

// TestTheNavigationRendersRegistryDrivenKindLabels: the badge on a wiki row is the
// label the gameplay system's registry resolved, so a UI plugin registering a new
// kind needs no shell change (UI §4.11.3).
//
// Both halves matter. The fixture carries two different labels and neither is one
// this package would have chosen, and a page with no kind renders no badge at all
// rather than an empty one.
func TestTheNavigationRendersRegistryDrivenKindLabels(t *testing.T) {
	t.Parallel()

	nav := campaignNav()
	nav.Wiki.Children = []chrome.PageNode{
		{Title: "Wards", Href: "/c/greyhaven/wiki/Wards", Kind: "Encounter"},
		{Title: "Plain", Href: "/c/greyhaven/wiki/Plain"},
	}

	document := renderNav(t, nav)
	rendered := textOf(document)

	if !strings.Contains(rendered, "Encounter") {
		t.Errorf("the navigation does not render the registry's kind label:\n%s", rendered)
	}

	badges := 0
	forEachElement(document, func(node *html.Node) {
		if hasClass(node, "nav-kind") {
			badges++
		}
	})

	if badges != 1 {
		t.Errorf("the navigation rendered %d kind badges, want 1; a page with no kind gets "+
			"none rather than an empty one (UI §4.11.3)", badges)
	}
}

// TestTheNavigationSectionsFollowTheSpecifiedOrder: Campaigns, Wiki, Table,
// Search, Admin — §4.3 fixes the order and it is not alphabetical by accident.
func TestTheNavigationSectionsFollowTheSpecifiedOrder(t *testing.T) {
	t.Parallel()

	document := renderNav(t, campaignNav())
	destinations := byTestID(t, document, "nav-destinations")

	var order []string
	forEachElement(destinations, func(node *html.Node) {
		switch attribute(node, "data-testid") {
		case "nav-campaigns", "nav-wiki", "nav-table", "nav-search", "nav-admin":
			order = append(order, attribute(node, "data-testid"))
		}
	})

	want := []string{"nav-campaigns", "nav-wiki", "nav-table", "nav-search", "nav-admin"}
	if len(order) != len(want) {
		t.Fatalf("the navigation rendered %v, want %v (UI §4.3)", order, want)
	}

	for index, section := range want {
		if order[index] != section {
			t.Errorf("section %d is %s, want %s (UI §4.3)", index+1, order[index], section)
		}
	}
}

// TestASectionWithNothingInItIsAbsent: a section above no links is a promise of a
// destination it does not have, which is the same failure as an empty navigation
// before a campaign exists.
func TestASectionWithNothingInItIsAbsent(t *testing.T) {
	t.Parallel()

	document := renderNav(t, &chrome.NavView{
		// No campaigns, and a tree whose root has no children — which is what a
		// campaign with no pages looks like, and §4.7 puts that message in the
		// centre rather than in a section above no links.
		Wiki:       &chrome.PageNode{Title: "The Vault"},
		SearchHref: "/c/greyhaven/search",
	})

	if hasTestID(document, "nav-campaigns") {
		t.Error("the navigation renders a Campaigns section with no campaigns")
	}

	if hasTestID(document, "nav-wiki") {
		t.Error("the navigation renders a Wiki section over a tree with no pages")
	}

	if hasTestID(document, "nav-table") {
		t.Error("the navigation renders a table link with no address (UI §4.7)")
	}

	byTestID(t, document, "nav-search")
}

// --- The footer's compact bar, UI §4.2 and §7.2 ----------------------------

// TestTheCompactBarIsASecondNavigationLandmark: §4.2's bottom bar is a navigation,
// §7.2 requires the two to be distinctly labelled, and the footer keeps
// `role="contentinfo"` so the persistent footer requirement is never violated.
//
// A footer whose role is `navigation` would stop being contentinfo, which is why
// the bar is a `<nav>` inside it rather than the footer itself. The label is
// "Primary" against the navigation's "Campaign", and §10.2's audit checks the
// pair.
func TestTheCompactBarIsASecondNavigationLandmark(t *testing.T) {
	t.Parallel()

	footer := chrome.FooterView{
		Bar: chrome.BarDestinations{
			Wiki:   "/c/greyhaven/wiki",
			Search: "/c/greyhaven/search",
			Table:  "/c/greyhaven/play",
			More:   "/c/greyhaven/settings",
		},
	}

	var out strings.Builder
	if err := chrome.Footer(footer).Render(t.Context(), &out); err != nil {
		t.Fatalf("render footer: %v", err)
	}

	parsed, err := html.Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("parse footer: %v", err)
	}

	if got := len(byRole(parsed, "contentinfo")); got != 1 {
		t.Errorf("the compact bar leaves %d contentinfo landmarks, want 1; §4.6's persistent "+
			"footer requirement is never traded for the bar", got)
	}

	bar := byTestID(t, parsed, "footer-bar")
	if got := attribute(bar, "role"); got != "navigation" {
		t.Errorf("the compact bar has role=%q, want navigation (UI §4.2)", got)
	}

	if got := attribute(bar, "aria-label"); got != "Primary" {
		t.Errorf("the compact bar is labelled %q, want Primary, which is distinct from the "+
			"campaign navigation's Campaign (UI §7.2)", got)
	}
}

// TestTheCompactBarOmitsTheDestinationsItDoesNotHave: Table is absent for an
// anonymous reader, and "More" is absent for an instance with nothing more. Both
// omissions leave the bar shorter rather than leaving a dead control in it.
func TestTheCompactBarOmitsTheDestinationsItDoesNotHave(t *testing.T) {
	t.Parallel()

	parsed := renderFooter(t, chrome.FooterView{
		Bar: chrome.BarDestinations{Wiki: "/c/greyhaven/wiki"},
	})

	bar := byTestID(t, parsed, "footer-bar")
	if got := len(linksWithDestination(bar)); got != 1 {
		t.Errorf("the compact bar has %d destinations, want 1", got)
	}
}

// TestTheCompactBarIsAbsentWithNothingToNavigateTo: a bar with no destinations is
// a 56px strip of nothing at the bottom of every page, which is exactly the
// weight the compact-short tier removes the bar to avoid.
func TestTheCompactBarIsAbsentWithNothingToNavigateTo(t *testing.T) {
	t.Parallel()

	parsed := renderFooter(t, chrome.FooterView{Version: "0.3.0"})

	if hasTestID(parsed, "footer-bar") {
		t.Error("the footer renders a bar with no destinations")
	}

	byTestID(t, parsed, "footer-version")
}

// TestTheTableRowReplacesTheDestinations: §4.2's rule for /play, and the
// exclusivity is the point — four navigation links on the one page whose footer
// must be the player's actions.
func TestTheTableRowReplacesTheDestinations(t *testing.T) {
	t.Parallel()

	parsed := renderFooter(t, chrome.FooterView{
		Bar: chrome.BarDestinations{
			Wiki:   "/c/greyhaven/wiki",
			Search: "/c/greyhaven/search",
			Table:  "/c/greyhaven/play",
		},
		Play: &chrome.PlayBar{Clock: "Day 2, 19:40", LeaveHref: "/c/greyhaven"},
	})

	if hasTestID(parsed, "footer-bar") {
		t.Error("the table route renders the four destinations as well as the player's row " +
			"(UI §4.2)")
	}

	row := byTestID(t, parsed, "footer-play")
	if !strings.Contains(textOf(row), "Day 2, 19:40") {
		t.Errorf("the player's row does not carry the clock:\n%s", textOf(row))
	}

	if got := attribute(byTestID(t, parsed, "footer-leave"), "href"); got != "/c/greyhaven" {
		t.Errorf("leaving the table points at %q, want /c/greyhaven", got)
	}
}

// --- The utilities rail, UI §4.5 -------------------------------------------

// TestTheRailIsTheUtilitiesLandmark and its trigger: the rail persists from 1280
// and collapses first, so it needs a trigger that is reachable while it is
// closed. The trigger is exported rather than inlined for that reason, and it
// renders collapsed, which is the state the rail's own stylesheet starts in at
// every tier below 1280.
func TestTheRailIsTheUtilitiesLandmark(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			rail := byTestID(t, fixture.parsed(t), "shell-rail")

			if got := attribute(rail, "role"); got != "complementary" {
				t.Errorf("the rail has role=%q, want complementary (UI §4.5)", got)
			}

			if got := attribute(rail, "aria-label"); got != "Utilities" {
				t.Errorf("the rail is labelled %q, want Utilities (UI §4.5)", got)
			}

			// The client re-parents this element into the sheet host at compact,
			// and it finds it by role — so the role is the hook that matters, and
			// a landmark that lost it would stop being a sheet and stay a column.
			if !hasAttribute(rail, "data-shell-rail") {
				t.Error("the rail carries no data-shell-rail marker; the stylesheet's tier " +
					"rules target it (UI §3.7)")
			}
		})
	}
}

func TestTheUtilitiesTriggerStartsClosed(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	if err := chrome.UtilitiesTrigger("rail").Render(t.Context(), &out); err != nil {
		t.Fatalf("render trigger: %v", err)
	}

	parsed, err := html.Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("parse trigger: %v", err)
	}

	trigger := byTestID(t, parsed, "rail-trigger")

	if got := attribute(trigger, "aria-expanded"); got != "false" {
		t.Errorf("the trigger has aria-expanded=%q, want false; the rail starts closed at "+
			"every tier below 1280 (UI §3.1)", got)
	}

	if got := attribute(trigger, "aria-controls"); got != "rail" {
		t.Errorf("the trigger controls %q, want rail", got)
	}
}

// TestTheRailHidesNothingItCarries: §4.5 makes a sheet modal by setting `inert` on
// the page behind it, and the rail's panels are the route's — so the rail's half
// is to carry exactly what it is given and add no landmark of its own around it.
// A second `complementary` inside the rail would break §7.2's contract for every
// route that uses it.
func TestTheRailAddsNoLandmarkOfItsOwn(t *testing.T) {
	t.Parallel()

	document := chromeFixture{
		name:     "rail",
		header:   chrome.HeaderView{},
		footer:   chrome.FooterView{},
		wantNav:  false,
		wantLive: false,
	}.parsed(t)

	for _, role := range []string{"banner", "main", "navigation", "contentinfo", "complementary"} {
		if got := len(byRole(document, role)); got > 1 {
			t.Errorf("the composition has %d %s landmarks, want 1 (UI §7.2)", got, role)
		}
	}
}

// TestNothingHidesAFocusableElement: §7.10 prohibits `aria-hidden` on a focusable
// element, and a combination this package has exactly one reason to express — the
// navigation's icons. Asserted on the combination rather than on the attribute,
// because the attribute itself is legitimate here and only the combination is not.
func TestNothingHidesAFocusableElement(t *testing.T) {
	t.Parallel()

	for _, fixture := range chromeFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			forEachElement(fixture.parsed(t), func(node *html.Node) {
				if attribute(node, "aria-hidden") != "true" {
					return
				}

				if isFocusable(node) {
					t.Errorf("<%s> is focusable and hidden from assistive technology "+
						"(UI §7.10)", node.Data)
				}
			})
		})
	}
}

// --- Rendering helpers for the single components ----------------------------

// header renders the banner alone and parses it, for the assertions that are
// about the banner's own branches rather than about a whole document.
func header(t *testing.T, view chrome.HeaderView) *html.Node {
	t.Helper()

	return parseComponent(t, chrome.Header(view))
}

// renderNav renders the navigation alone and parses it.
func renderNav(t *testing.T, view *chrome.NavView) *html.Node {
	t.Helper()

	return parseComponent(t, chrome.CampaignNav(*view))
}

// renderFooter renders the footer alone and parses it.
func renderFooter(t *testing.T, view chrome.FooterView) *html.Node {
	t.Helper()

	return parseComponent(t, chrome.Footer(view))
}

// parseComponent renders one component into a document. The landmarks are valid
// in a body, and a fragment is parsed in an implicit one.
func parseComponent(t *testing.T, component templ.Component) *html.Node {
	t.Helper()

	document, err := html.Parse(strings.NewReader(render(t, component)))
	if err != nil {
		t.Fatalf("parse component: %v", err)
	}

	return document
}

// textOf is an element's text, with the whitespace between elements collapsed so
// a multi-line template is not compared against a run-on string.
func textOf(node *html.Node) string {
	var out strings.Builder

	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			out.WriteString(current.Data)
		}

		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(node)

	return strings.Join(strings.Fields(out.String()), " ")
}

// ancestorWithTag walks up to the nearest element with a tag name, which is how a
// control is shown to sit inside a form rather than merely near one.
func ancestorWithTag(node *html.Node, tag string) *html.Node {
	for current := node; current != nil; current = current.Parent {
		if current.Type == html.ElementNode && current.Data == tag {
			return current
		}
	}

	return nil
}

// hasLabel reports whether some `<label for>` names a field, which is §7.7's rule
// and the reason a header needs a visually-hidden label rather than a placeholder.
func hasLabel(document *html.Node, id string) bool {
	found := false

	forEachElement(document, func(node *html.Node) {
		if node.Data == "label" && attribute(node, "for") == id {
			found = true
		}
	})

	return found
}

// linksWithDestination returns the links of a list that carry a `data-nav-dest`,
// which is how the compact bar's four destinations are counted without counting
// any other link inside the footer.
func linksWithDestination(node *html.Node) []*html.Node {
	var found []*html.Node

	forEachElement(node, func(child *html.Node) {
		if child.Data == "a" && attribute(child, "data-nav-dest") != "" {
			found = append(found, child)
		}
	})

	return found
}

// isFocusable reports whether an element is in the tab order's element set: a
// link with an address, a form control, a summary, or anything carrying a
// non-negative tabindex. It is deliberately the *narrow* reading — §7.10
// prohibits hiding a focusable element, and an icon carrying `aria-hidden` is the
// legitimate case the prohibition is not about.
func isFocusable(node *html.Node) bool {
	switch node.Data {
	case "button", "select", "textarea", "summary", "input":
		return attribute(node, "type") != "hidden"
	case "a":
		return attribute(node, "href") != ""
	default:
		return false
	}
}
