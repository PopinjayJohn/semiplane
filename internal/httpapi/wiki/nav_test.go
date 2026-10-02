package wiki_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/domain"
)

// The campaign navigation (UI §4.3), asserted on the parsed document.
//
// # Why parsed and not matched
//
// Everything below is a question about structure — how many `nav` landmarks there
// are, what one of them is *called*, which rows it holds — and none of them is
// answerable by looking for a substring. `strings.Count(document, "nav-table")`
// counts the *word* in a class name, in a comment, and in a text node, and reports
// the same number for a navigation with one Table link and for one whose author
// wrote the word three times in a comment. The parser is what separates them.
//
// # The Table assertion, and why it is not "one row called Table"
//
// UI §1.2 and §4.3 both make "a campaign has at most one live tabletop" a hard
// rule, and the way that gets broken in practice is not a list of games — it is
// adding a second destination beside the first, so that "Table" becomes a
// category and the reader has to guess which of them is live. So the assertions
// are about the *set* of destinations: exactly one Table, and no other element in
// the navigation whose label mentions a collection of them.

// navLandmarks returns every element in the document whose role is `navigation`.
func navLandmarks(root *html.Node) []*html.Node {
	return elementsWithRole(root, "navigation")
}

// navigation returns the campaign navigation landmark, failing when there is not
// exactly one.
//
// A helper rather than `findFirst` because "the navigation" is the whole
// assertion: §10.2 requires landmarks to be *distinctly* labelled precisely so
// that two of them cannot be confused, and a helper that returned the first match
// would report on a document with two navigations as though it had one.
func navigation(t *testing.T, root *html.Node) *html.Node {
	t.Helper()

	found := navLandmarks(root)
	if len(found) != 1 {
		t.Fatalf("the document has %d navigation landmarks, want exactly 1 "+
			"(UI §7.2: landmarks must be distinct, and a duplicate makes landmark "+
			"navigation useless)", len(found))
	}

	return found[0]
}

// navRowLabels returns the visible text of every link and button in the
// navigation, in document order.
//
// Buttons as well as links because the sections that have no rows render a
// disclosure button instead — `chrome.navDisclosure` — and a test that read only
// the links would see a navigation with no Wiki section at all and call it empty
// rather than wrong.
func navRowLabels(root *html.Node) []string {
	var labels []string

	eachElement(root, func(node *html.Node) {
		if node.Data != "a" && node.Data != "button" {
			return
		}

		if !hasAncestor(node, "nav") {
			return
		}

		// The skip links are anchors outside the landmark, so `hasAncestor` already
		// excludes them; this is the label span, filtered so the icon's
		// `aria-hidden` svg contributes nothing.
		if label := strings.TrimSpace(textOf(node)); label != "" {
			labels = append(labels, label)
		}
	})

	return labels
}

// navHrefs returns every `href` inside the navigation, in document order.
func navHrefs(root *html.Node) []string {
	var hrefs []string

	eachElement(root, func(node *html.Node) {
		if node.Data != "a" || !hasAncestor(node, "nav") {
			return
		}

		if href := attribute(node, "href"); href != "" {
			hrefs = append(hrefs, href)
		}
	})

	return hrefs
}

// countLabels returns how many of labels appear in the rendered navigation.
//
// Case-insensitively, because the alternative is a test that has to know which
// capitalisation the template happens to use — and a template that changed the
// case of a label would then fail a test about the *count*, which is the wrong
// failure and one somebody would "fix" by editing the test.
func countLabels(root *html.Node, label string) int {
	found := 0

	for _, row := range navRowLabels(root) {
		if strings.EqualFold(row, label) {
			found++
		}
	}

	return found
}

// TestEveryRouteStateFillsTheCampaignShellGrid is the change this
// work item exists to make: UI §4.6 says the left navigation is present inside a
// campaign and absent before one, and this route is always inside one.
//
// Asserted as the *absence* of the pre-campaign variant as well as the presence
// of the campaign one, because `shell--pre-campaign` is what the stylesheet keys
// the two-column form on (UI §3.6) and a document rendering three columns in a
// two-column sheet is a bug no other assertion here can see.
func TestEveryRouteStateFillsTheCampaignShellGrid(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	document := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String()
	root := parseDocument(t, document)

	shell := elementByTestID(t, root, "shell")

	if classes := strings.Fields(attribute(shell, "class")); !slices.Contains(classes, "shell") {
		t.Errorf("the grid's classes are %v, want it to carry shell", classes)
	}

	if classes := strings.Fields(
		attribute(shell, "class"),
	); slices.Contains(
		classes,
		"shell--pre-campaign",
	) {
		t.Errorf("the grid carries shell--pre-campaign; this route is inside a " +
			"campaign, so UI §4.6 requires the three-column form")
	}

	// The landmarks themselves, by role, because §7.2's contract is written as
	// roles and the structural test is a grep that cannot see an implication.
	for _, landmark := range []struct{ role, testID string }{
		{role: "banner", testID: "shell-header"},
		{role: "navigation", testID: "shell-nav"},
		{role: "main", testID: "shell-main"},
		{role: "complementary", testID: "shell-rail"},
		{role: "contentinfo", testID: "shell-footer"},
	} {
		found := elementsWithRole(root, landmark.role)
		if len(found) != 1 {
			t.Errorf("the document has %d elements with role=%q, want exactly 1",
				len(found), landmark.role)
		}

		if got := attribute(
			elementByTestID(t, root, landmark.testID),
			"role",
		); got != landmark.role {
			t.Errorf("%s has role=%q, want %q", landmark.testID, got, landmark.role)
		}
	}
}

// TestTheCampaignShellCarriesExactlyOneH1AndItIsThePageTitle is UI §4.4 and
// §7.2's first structural rule, and the reason the centre slot owns the heading.
//
// Exactly one, not at least one: two `<h1>`s tell a screen reader the page has
// two titles, and none tell it the page has none while looking complete to a
// sighted reader. The count is over parsed elements rather than over the text
// `<h1`, because a page whose *body* contains the characters `<h1` — which goldmark
// escapes, so a body that says so arrives as text — must not be able to add to the
// count, and a substring test cannot tell the two apart.
func TestTheCampaignShellCarriesExactlyOneH1AndItIsThePageTitle(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	root := parseDocument(t, fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String())

	headings := findElements(root, "h1")
	if len(headings) != 1 {
		t.Fatalf("the document has %d <h1> elements, want exactly 1 (UI §7.2); "+
			"found: %s", len(headings), strings.Join(headingLocations(root), ", "))
	}

	// It must be the page's own name, and inside the centre slot. A heading in the
	// navigation would be read before the page's own and would make the outline
	// describe the chrome.
	if got := strings.TrimSpace(textOf(headings[0])); got != "Vault" {
		t.Errorf("the <h1> reads %q, want the page's name %q", got, "Vault")
	}

	if !hasAncestor(headings[0], "main") {
		t.Errorf("the <h1> is not inside <main>; the centre slot owns the page's " +
			"heading and the navigation must not add one (UI §4.4)")
	}
}

// headingLocations names every heading in a document, for a failure message.
func headingLocations(root *html.Node) []string {
	var found []string

	eachElement(root, func(node *html.Node) {
		if strings.HasPrefix(node.Data, "h") && len(node.Data) == 2 {
			found = append(found, node.Data+" "+strings.TrimSpace(textOf(node)))
		}
	})

	return found
}

// TestEveryRouteCarriesDistinctlyLabelledLandmarks is §10.2's landmark rule and
// §7.2's, and the two are the same rule seen from two ends: the labels are exact
// strings the record fixes, and they must be distinct from one another.
//
// The distinctness half is the substantive one. §7.2 says the compact bottom bar
// is a second `nav`, so "both must carry distinguishing labels" — and a document
// whose two navigation landmarks are both called "Campaign" satisfies every
// assertion that checks for *a* navigation and none that checks for *the right*
// one. So this asserts the pair, not the presence of two labels.
func TestEveryRouteCarriesDistinctlyLabelledLandmarks(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	root := parseDocument(t, fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String())

	want := map[string]string{
		"navigation":    "Campaign",
		"complementary": "Utilities",
	}

	seen := map[string]string{}

	for role, label := range want {
		found := elementsWithRole(root, role)
		if len(found) != 1 {
			t.Errorf("the document has %d landmarks with role=%q, want exactly 1 "+
				"(UI §7.2)", len(found), role)

			continue
		}

		got := attribute(found[0], "aria-label")
		if got != label {
			t.Errorf("the %s landmark has aria-label=%q, want %q (UI §4.3, §4.5)",
				role, got, label)
		}

		seen[role] = got
	}

	if seen["navigation"] != "" && seen["navigation"] == seen["complementary"] {
		t.Errorf("the navigation and the rail are both labelled %q; §7.2 calls "+
			"identically labelled landmarks a gate failure", seen["navigation"])
	}
}

// TestAllThreeSkipLinksResolve is §7.2's skip-link rule in the form that can fail.
//
// Three claims, and the third is the one a substring test cannot make:
//
//  1. The three links are present, in §7.2's order, pointing at `#main`, `#nav`
//     and `#rail`.
//  2. Every one of those fragments resolves to an element in this document — a
//     link to a landmark that is not here moves focus nowhere, which costs a
//     reader a keypress and teaches them the skip links are unreliable.
//  3. Each target carries `tabindex="-1"`, so focus lands somewhere a screen
//     reader announces rather than at the top of a scroll container.
func TestAllThreeSkipLinksResolve(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	root := parseDocument(t, fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String())

	wantOrder := []string{"#main", "#nav", "#rail"}

	var links []*html.Node

	eachElement(root, func(node *html.Node) {
		if node.Data == "a" && strings.Contains(attribute(node, "class"), "skip-link") {
			links = append(links, node)
		}
	})

	if len(links) != len(wantOrder) {
		t.Fatalf("the document has %d skip links, want %d (UI §7.2); found: %s",
			len(links), len(wantOrder), strings.Join(hrefsOf(links), ", "))
	}

	for at, want := range wantOrder {
		href := attribute(links[at], "href")
		if href != want {
			t.Errorf("skip link %d points at %q, want %q — §7.2 fixes their order: "+
				"content, navigation, utilities", at, href, want)
		}

		target := elementByID(root, strings.TrimPrefix(href, "#"))
		if target == nil {
			t.Errorf("skip link %d points at %q, which resolves to no element in "+
				"this document", at, href)

			continue
		}

		if got := attribute(target, "tabindex"); got != "-1" {
			t.Errorf("skip link %d targets %s with tabindex=%q, want \"-1\" so "+
				"focus lands somewhere announced (UI §7.2)", at, href, got)
		}
	}
}

// TestTheNavSkipLinkExistsExactlyWhereTheNavExists is §7.2's "only where the nav
// exists" and §4.6's "the navigation is absent before a campaign exists", and it
// is asserted in **both** directions across every state this route can answer.
//
// One direction is the easy one — the nav is present, so the link must be — and
// the other is the failure that ships: a shell variant that rendered the link
// always, or the nav always, would satisfy a single-state audit and produce a
// document with a skip link pointing at nothing on every pre-campaign route in the
// product. So the states vary: three tiers, and the 404 as well as the 200,
// because the failure state is built by a different branch of the handler and a
// change to one is not a change to the other.
func TestTheNavSkipLinkExistsExactlyWhereTheNavExists(t *testing.T) {
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

			root := parseDocument(t, fixed.get(testCase.path, testCase.requestor).Body.String())

			hasNav := len(navLandmarks(root)) > 0
			hasLink := hasSkipLinkTo(root, "#nav")

			if hasNav != hasLink {
				t.Errorf("the navigation is %s but a skip link to #nav is %s; "+
					"§7.2 puts that link in the document only where the navigation "+
					"exists, and a link to an absent landmark moves focus nowhere",
					presence(hasNav), presence(hasLink))
			}

			if !hasNav {
				t.Errorf("this route is inside a campaign, so §4.6 requires the "+
					"navigation; the %s document rendered none",
					testCase.name)
			}
		})
	}
}

// hasSkipLinkTo reports whether a skip link with this fragment href exists.
func hasSkipLinkTo(root *html.Node, fragment string) bool {
	found := false

	eachElement(root, func(node *html.Node) {
		if node.Data == "a" &&
			strings.Contains(attribute(node, "class"), "skip-link") &&
			attribute(node, "href") == fragment {
			found = true
		}
	})

	return found
}

// presence renders a boolean as a phrase a failure message can use.
func presence(value bool) string {
	if value {
		return "present"
	}

	return "absent"
}

// hrefsOf renders a set of anchors' hrefs, for a failure message.
func hrefsOf(nodes []*html.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, attribute(node, "href"))
	}

	return out
}

// TestTheNavigationOffersTableOnceAndNeverAListOfThem is UI §1.2 and §4.3's
// "Table is a single link, not a list", asserted as an exact count.
//
// One and not zero: a navigation with no Table link is the §4.7 `system_id` row
// and is correct for a campaign with no gameplay system, but *this* harness's
// campaign has one and the harness wires it, so a Table that is missing is a bug
// rather than a design. And exactly one: the failure this rule exists to prevent
// is a second destination beside the first, so an assertion of "at least one"
// would pass on the document it was written about.
func TestTheNavigationOffersTableOnceAndNeverAListOfThem(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).withGameplaySystem()
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	root := parseDocument(t, fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String())

	if got := countLabels(root, "Table"); got != 1 {
		t.Errorf("the navigation offers %d rows called Table, want exactly 1 — a "+
			"campaign has at most one live tabletop, so Table is a destination and "+
			"not a category (UI §1.2, §4.3)", got)
	}

	// And it is a link to that one tabletop, not a button that opens a menu of
	// them: a `<button>` here is how "Table" becomes a category.
	for _, node := range navRowsNamed(root, "Table") {
		if node.Data != "a" {
			t.Errorf("the Table row is a <%s>, want an <a>: a control that opens "+
				"something is a destination list by another name (UI §4.3)", node.Data)
		}

		if href := attribute(node, "href"); href != "/c/greyhaven/play" {
			t.Errorf("the Table row points at %q, want %q", href, "/c/greyhaven/play")
		}
	}

	// The vocabulary half: a "Games" list is the shape §1.2 forbids by name, so
	// it is checked by name as well as by count. Any label mentioning games in any
	// case is a finding.
	for _, row := range navRowLabels(root) {
		if strings.Contains(strings.ToLower(row), "game") {
			t.Errorf("the navigation has a row reading %q; §1.2 forbids a games "+
				"list, and the entity does not exist", row)
		}
	}
}

// navRowsNamed returns the navigation's rows whose text is exactly label.
func navRowsNamed(root *html.Node, label string) []*html.Node {
	var found []*html.Node

	eachElement(root, func(node *html.Node) {
		if node.Data != "a" && node.Data != "button" {
			return
		}

		if !hasAncestor(node, "nav") {
			return
		}

		if strings.EqualFold(strings.TrimSpace(textOf(node)), label) {
			found = append(found, node)
		}
	})

	return found
}

// TestTheNavigationOmitsWhatThisReaderMayNotReach is §4.3's "edit affordances
// are absent, not disabled" and S-8.1's membership rule for play, in one table.
//
// Each case asserts the **absence** for the readers who may not have the
// destination and the **presence** for the one who may, because an assertion of
// absence alone passes on a navigation that renders nothing at all — which is the
// failure mode `TestTheNavigationOffersTableOnceAndNeverAListOfThem` guards
// against for Table and this one has to guard against for itself.
func TestTheNavigationOmitsWhatThisReaderMayNotReach(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name       string
		requestor  domain.Requestor
		wantTable  bool
		wantAdmin  bool
		wantSearch bool
	}{
		{name: "gm", requestor: gmRequestor(), wantTable: true, wantAdmin: true, wantSearch: true},
		{name: "player", requestor: playerRequestor(), wantTable: true, wantSearch: true},
		{name: "anonymous", requestor: anonymousRequestor(), wantSearch: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			fixed := newHarness(t).withGameplaySystem()
			fixed.write("Vault.md", pageBody("Iron and rust.\n"))

			root := parseDocument(t,
				fixed.get("/c/greyhaven/wiki/Vault", testCase.requestor).Body.String())

			for _, want := range []struct {
				label string
				href  string
				shown bool
			}{
				{label: "Table", href: "/c/greyhaven/play", shown: testCase.wantTable},
				{label: "Admin", href: "/c/greyhaven/settings", shown: testCase.wantAdmin},
				{label: "Search", href: "/c/greyhaven/search", shown: testCase.wantSearch},
			} {
				rows := navRowsNamed(root, want.label)
				hrefs := hrefsIn(root, want.href)

				if want.shown {
					if len(rows) != 1 {
						t.Errorf("%s should see the %s destination, and the "+
							"navigation has %d rows reading %q", testCase.name,
							want.label, len(rows), want.label)
					}

					continue
				}

				if len(rows) != 0 || len(hrefs) != 0 {
					t.Errorf("%s should not see the %s destination; the navigation "+
						"has %d rows reading %q and %d links to %s. UI §4.3 wants "+
						"them absent rather than disabled",
						testCase.name, want.label, len(rows), want.label,
						len(hrefs), want.href)
				}
			}
		})
	}
}

// hrefsIn counts the navigation links pointing at one address.
func hrefsIn(root *html.Node, href string) []string {
	var found []string

	for _, candidate := range navHrefs(root) {
		if candidate == href {
			found = append(found, candidate)
		}
	}

	return found
}

// TestTheNavigationOmitsTableForACampaignWithNoGameplaySystem is UI §4.7's
// `system_id` row: a campaign with no system installed has no tabletop, so the
// destination is absent rather than a link that answers 404.
//
// Separate from the table above because it is the *campaign's* half of the
// condition rather than the reader's, and a test that only varies the reader
// cannot see it.
func TestTheNavigationOmitsTableForACampaignWithNoGameplaySystem(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	recorder := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", recorder.Code, recorder.Body)
	}

	root := parseDocument(t, recorder.Body.String())

	if rows := navRowsNamed(root, "Table"); len(rows) != 0 {
		t.Errorf("a campaign with no gameplay system has %d Table rows, want none; "+
			"§4.7's system_id row makes the destination absent rather than a link "+
			"that answers 404", len(rows))
	}

	// And the same document does offer the destinations it legitimately can, so
	// the absence above is not just an empty navigation.
	if rows := navRowsNamed(root, "Search"); len(rows) != 1 {
		t.Errorf("a campaign with no gameplay system has %d Search rows, want 1; "+
			"the absence of Table must not be an empty navigation", len(rows))
	}
}

// TestTheNavigationListsPagesByNameAndNotByTheirFrontMatterTitle is the security
// property of the tree's labels, and it is the reason `pageName` is used rather
// than `domain.Page.Title`.
//
// The index stores the front-matter title **unredacted** — `content.Indexer` writes
// `doc.FrontMatter.Title` into `pages.title`, and S-5.11's guarantee covers
// `body_plain` only — so a title in the navigation would put text a non-GM
// response must not carry into a non-GM response. A GM who writes a secret into a
// page's front matter, or who simply names a page after one, gets that name in
// every reader's navigation.
//
// Asserted with a marker, because "the row says `Goblin`" is also what a
// navigation reading the title would say when the two happen to agree. The second
// half — the marker absent from a player's document — is the one that would fail
// if the title were used.
func TestTheNavigationListsPagesByNameAndNotByTheirFrontMatterTitle(t *testing.T) {
	t.Parallel()

	const marker = "SECRET-IN-TITLE"

	fixed := newHarness(t)
	fixed.pages = pageListing{pages: []domain.Page{
		{CampaignID: testCampID, Path: "Goblin.md", Title: marker},
	}}
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	recorder := fixed.get("/c/greyhaven/wiki/Vault", playerRequestor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", recorder.Code, recorder.Body)
	}

	document := recorder.Body.String()
	if strings.Contains(document, marker) {
		t.Errorf("a player's response carries %q from the page index's title "+
			"column; that column is unredacted, so no non-GM response may render "+
			"it (S-5.6, S-14.1)", marker)
	}

	// And the page is *listed*, under its own name. A navigation that dropped the
	// row would satisfy the assertion above by having nothing to leak, so the
	// positive half is here.
	root := parseDocument(t, document)

	if rows := navRowsNamed(root, "Goblin"); len(rows) != 1 {
		t.Errorf("the navigation has %d rows reading Goblin, want 1; the page is "+
			"listed under its base name", len(rows))
	}
}

// TestTheNavigationListsAFolderOnceForEveryPageInIt is the tree's one-row-per-
// directory property, and the reason the folders exist at all.
//
// Without it, `notes/Goblin.md` and `notes/Orc.md` would render as two rows with
// the same icon and no indication that they are siblings, and a vault with
// `campaigns/a/`, `campaigns/b/` and `campaigns/c/` would flatten into nothing a
// reader could navigate. Asserted as a *count* of rows labelled `notes`, because
// the bug is duplication and duplication is invisible in a set.
func TestTheNavigationListsAFolderOnceForEveryPageInIt(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.indexed("notes/Goblin.md", "notes/Orc.md", "notes/deep/Rat.md", "Vault.md")
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	root := parseDocument(t, fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String())

	if rows := navRowsNamed(root, "notes"); len(rows) != 1 {
		t.Errorf("the navigation has %d rows reading notes, want 1: a directory "+
			"is one folder however many pages it holds", len(rows))
	}

	// The folder's disclosure must point at a region that exists, or pressing it
	// reveals nothing while claiming otherwise. §10.2's reference rule is the same
	// claim from the accessibility side.
	for _, disclosure := range navRowsNamed(root, "notes") {
		region := attribute(disclosure, "aria-controls")
		if region == "" {
			continue // A link, not a disclosure: a directory with no page of its own.
		}

		if elementByID(root, region) == nil {
			t.Errorf("the notes folder's aria-controls=%q resolves to no element",
				region)
		}
	}

	// Three leaves: two in `notes` and one in `notes/deep`. `Vault` is the page
	// being read, and it is in the listing too, so it counts.
	for _, leaf := range []string{"Goblin", "Orc", "Rat", "Vault"} {
		if rows := navRowsNamed(root, leaf); len(rows) != 1 {
			t.Errorf("the navigation has %d rows reading %s, want 1", len(rows), leaf)
		}
	}
}

// TestTheNavigationCapsTheTreeDepth is UI §4.3's "a long tab sequence, mitigated
// by the skip link and a capped depth".
//
// Both directions, because a cap that is only ever asserted by its absence is a
// cap that has never been observed to stop anything: `Deep` sits at the cap and
// must be listed, `Deeper` one level past it must not be. A mutation that removed
// the cap fails on the second and a mutation that capped at zero fails on the
// first, which is the pair that makes the number mean something.
func TestTheNavigationCapsTheTreeDepth(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	// Four folders deep, one folder past the cap of three.
	fixed.indexed("a/b/c/Deep.md", "a/b/c/d/Deeper.md", "a/b/c/Shallow.md")
	fixed.write("Shallow.md", pageBody("Iron and rust.\n"))

	root := parseDocument(t, fixed.get("/c/greyhaven/wiki/Shallow", gmRequestor()).Body.String())

	if rows := navRowsNamed(root, "Deep"); len(rows) != 1 {
		t.Errorf("the navigation has %d rows reading Deep, want 1: it sits at the "+
			"cap, not past it", len(rows))
	}

	if rows := navRowsNamed(root, "Deeper"); len(rows) != 0 {
		t.Errorf("the navigation has %d rows reading Deeper, want 0: it is one "+
			"folder past the cap, and §4.3's answer to a long tab sequence is a "+
			"capped depth", len(rows))
	}
}

// TestTheNavigationIsEmptyForACampaignWithNoPages is §4.6's "a section with
// nothing in it is absent", on the section rather than on the landmark.
//
// The landmark is still there — §7.2 puts the navigation in the document inside a
// campaign whatever it contains — and what is asserted is that the Wiki *section*
// is not. The empty-state copy belongs in the centre slot (UI §4.7's first run),
// and a navigation section above no links is a promise of a destination it does
// not have.
func TestTheNavigationIsEmptyForACampaignWithNoPages(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	root := parseDocument(t, fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String())

	nav := navigation(t, root)

	if rows := navRowsNamed(root, "Wiki"); len(rows) != 0 {
		t.Errorf("a campaign whose index is empty has %d Wiki rows, want none; "+
			"§4.6 makes a section with nothing in it absent rather than empty",
			len(rows))
	}

	// The landmark still carries its label and its collapse control, so the
	// document is not the pre-campaign shell wearing a nav element.
	if got := attribute(nav, "aria-label"); got != "Campaign" {
		t.Errorf("the navigation landmark has aria-label=%q, want Campaign", got)
	}

	if got := len(elementsWithTestID(nav, "nav-collapse")); got != 1 {
		t.Errorf("the navigation has %d collapse controls, want 1", got)
	}
}

// TestTheCampaignsSectionListsTheReadersOwnCampaigns is UI §4.3's first section,
// and the query behind it is the one this work item added a dependency for.
//
// Asserted in three directions, because each one alone is satisfiable by a bug: the
// GM's own campaign is listed; the section is **absent** for an anonymous reader
// rather than a heading above no links; and the lister is **not asked** for an
// anonymous reader, which is the difference between "no memberships" and "no
// query" — a route that asked would be a route that asked about a stranger's
// campaigns on every page view.
func TestTheCampaignsSectionListsTheReadersOwnCampaigns(t *testing.T) {
	t.Parallel()

	t.Run("a member sees the section", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t)
		fixed.campaigns = &campaignLister{campaigns: []domain.Campaign{
			{ID: testCampID, Slug: testSlug, Name: "Greyhaven"},
			{ID: 2, Slug: "redmarsh", Name: "Redmarsh"},
		}}
		fixed.write("Vault.md", pageBody("Iron and rust.\n"))

		root := parseDocument(t, fixed.get("/c/greyhaven/wiki/Vault", gmRequestor()).Body.String())

		if rows := navRowsNamed(root, "Redmarsh"); len(rows) != 1 {
			t.Errorf("the navigation has %d rows reading Redmarsh, want 1: the "+
				"Campaigns section switches between the reader's own campaigns",
				len(rows))
		}

		// Its address, not just its name — a row reading the right name at the
		// wrong href is a switcher that switches nowhere.
		found := false

		for _, href := range navHrefs(root) {
			if href == "/c/redmarsh" {
				found = true
			}
		}

		if !found {
			t.Errorf("the Campaigns section has no link to /c/redmarsh; it links "+
				"to %v", navHrefs(root))
		}
	})

	t.Run("an anonymous reader gets no section and no query", func(t *testing.T) {
		t.Parallel()

		lister := &campaignLister{campaigns: []domain.Campaign{
			{ID: testCampID, Slug: testSlug, Name: "Greyhaven"},
		}}

		fixed := newHarness(t)
		fixed.campaigns = lister
		fixed.write("Vault.md", pageBody("Iron and rust.\n"))

		recorder := fixed.get("/c/greyhaven/wiki/Vault", anonymousRequestor())
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body:\n%s", recorder.Code, recorder.Body)
		}

		root := parseDocument(t, recorder.Body.String())

		if rows := navRowsNamed(root, "Campaigns"); len(rows) != 0 {
			t.Errorf("an anonymous reader has %d Campaigns rows, want none: a "+
				"switcher with nothing to switch between is a heading above no "+
				"links (UI §4.6)", len(rows))
		}

		if asked := lister.asked.Load(); asked != 0 {
			t.Errorf("the Campaigns lister was asked %d times for an anonymous "+
				"reader, want 0: there is no membership to list", asked)
		}
	})

	t.Run("an unreadable list costs the section and not the page", func(t *testing.T) {
		t.Parallel()

		fixed := newHarness(t)
		fixed.campaigns = &campaignLister{err: errListing}
		fixed.write("Vault.md", pageBody("Iron and rust.\n"))

		recorder := fixed.get("/c/greyhaven/wiki/Vault", gmRequestor())
		if recorder.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; a switcher that cannot be listed is "+
				"a smaller loss than a page that cannot be read. Body:\n%s",
				recorder.Code, recorder.Body)
		}

		root := parseDocument(t, recorder.Body.String())

		if rows := navRowsNamed(root, "Campaigns"); len(rows) != 0 {
			t.Errorf("the navigation has %d Campaigns rows after the listing "+
				"failed, want none", len(rows))
		}

		// The page is still there. An assertion that only counted the Campaigns rows
		// would pass on a document that had lost the page as well.
		if rows := navRowsNamed(root, "Search"); len(rows) != 1 {
			t.Errorf("the navigation has %d Search rows after the listing failed, "+
				"want 1", len(rows))
		}
	})
}
