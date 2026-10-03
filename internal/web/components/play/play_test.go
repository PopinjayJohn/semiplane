package play_test

// UI §10.2's audits for the token list, §10.6's target-size claim, and the
// keyboard-operability claim phase 9's DoD is written in.
//
// Four conventions govern every assertion here, and each was learned from a gate
// that passed for the wrong reason:
//
//   - **Parse the DOM; never substring-match the markup.** It is the only way "no
//     `world`" passes while the word sits in an HTML comment, an `aria-label` or
//     an attribute *name*. The vocabulary audit walks every text node, comment,
//     attribute value and attribute name.
//   - **Every audit must be able to fail.** `controls_test.go` runs the same audit
//     bodies over documents built to violate exactly one rule each, and requires a
//     finding from each. An audit that fires for an unrelated reason looks
//     identical to one that works.
//   - **An audit must also be capable of reporting nothing.** A red suite in the
//     first week is how an audit gets switched off rather than fixed.
//   - **The document, not the fragment.** Every audit runs over a whole document
//     built through `components.CampaignShell`, because the rules are about the
//     document a reader receives: landmarks, heading levels and reference
//     integrity are properties of the whole, and an audit over a lone `<ul>`
//     could not see a skipped heading level caused by the rail around it.
//
// The audits take an interface rather than a `*testing.T`, so `controls_test.go`
// can run the same bodies over a fixture and assert that they objected. A copy of
// an audit for the controls is the failure this file's structure exists to
// prevent.

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/web/components"
	"github.com/semiplane/semiplane/internal/web/components/chrome"
	"github.com/semiplane/semiplane/internal/web/components/play"
)

// retiredEntities is UI §1.2's two words, read from one place.
var retiredEntities = []string{"world", "session"}

// --- Fixtures ----------------------------------------------------------------------

// shellView is the chrome the play document is rendered with.
func shellView() components.ShellView {
	return components.ShellView{
		Title:       "Greyhaven",
		Instance:    components.InstanceView{Name: "Greyhaven", Version: "0.3.0"},
		Account:     components.AccountView{Username: "mira"},
		SignOutHref: "/logout",
		StatusHref:  "/status",
		Campaign:    chrome.CampaignRef{Slug: "greyhaven", Name: "Greyhaven"},
	}
}

// navView is the campaign navigation, with the tabletop's own destinations.
//
// **Not a nil nav.** §7.2 puts a skip link to `#nav` in the document exactly where
// the navigation is, so a campaign document with no navigation is a fixture that
// cannot exercise the skip-link audit.
func navView() chrome.NavView {
	return chrome.NavView{
		Campaigns:  []chrome.CampaignRef{{Name: "Greyhaven", Slug: "greyhaven"}},
		TableHref:  "/c/greyhaven/play",
		SearchHref: "/c/greyhaven/search",
		AdminHref:  "/c/greyhaven/settings",
	}
}

// populatedList is the token list with every branch this package renders at once:
// two rows with conditions, a row with none, a row whose layer nobody supplied, a
// row whose name nobody supplied, and unsorted conditions with a duplicate.
//
// The unsorted duplicate is deliberate: `realtime.Placement.Conditions` keeps that
// invariant in memory, and a rendering that trusted the caller would produce a row
// whose bytes depend on the order two clients happened to act in.
func populatedList() play.TokenListView {
	return play.TokenListView{Placements: []play.PlacementView{
		{
			ID: "goblin-1", Name: "Goblin", Kind: "Token",
			HP: 7, MaxHP: 7, Conditions: []string{"prone", "poisoned", "prone"},
			Layer: "Tokens",
		},
		{
			ID: "ogre-1", Name: "Ogre", Kind: "Token",
			HP: 0, MaxHP: 59,
			Layer: "Tokens",
		},
		{
			ID: "torch-1", Name: "Torch", Kind: "Scene",
			HP: 1, MaxHP: 0, Conditions: []string{"  "},
		},
		{
			ID: "unknown-1", Kind: "Token",
			HP: 3, MaxHP: 3, Layer: "",
		},
	}}
}

// emptyList is a table with nothing placed on it.
func emptyList() play.TokenListView {
	return play.TokenListView{}
}

// otherPanel is the initiative panel's body.
//
// **A distinct component, not a second `play.TokenList`.** The token panel
// renders a fixed `id` on its heading and a fixed `data-testid`, so using it twice
// in one document would put the same id in the document twice — and the audits
// below are about a document that is correct, so a fixture that is not is a
// fixture that hides the bugs. The initiative tracker is C3's file.
func otherPanel(text string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, out io.Writer) error {
		_, err := io.WriteString(out, `<p class="panel-note">`+text+`</p>`)
		if err != nil {
			return fmt.Errorf("write the panel: %w", err)
		}

		return nil
	})
}

// railView is the rail: the token list and the initiative tracker.
func railView() play.RailView {
	return play.RailView{
		Label:    "Table panels",
		Campaign: "greyhaven",
		Tabs: []play.Tab{
			play.TokensTab(play.TokenList(populatedList())),
			{ID: play.InitiativeTabID, Label: "Initiative", Content: otherPanel("Turn order.")},
		},
	}
}

// document renders the whole play page: the campaign shell, the navigation, a
// centre slot with its one `<h1>`, and the rail.
func document(list play.TokenListView) templ.Component {
	rail := railView()
	rail.Tabs[0] = play.TokensTab(play.TokenList(list))

	return components.CampaignShell(
		shellView(),
		navView(),
		heading("Greyhaven"),
		play.Rail(rail),
	)
}

// heading is the centre slot's `<h1>`.
func heading(text string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, out io.Writer) error {
		_, err := io.WriteString(out, `<h1 data-testid="page-title">`+text+`</h1>`)
		if err != nil {
			return fmt.Errorf("write the heading: %w", err)
		}

		return nil
	})
}

// render parses one component's output into a document node.
func render(t *testing.T, component templ.Component) *html.Node {
	t.Helper()

	var out strings.Builder

	if err := component.Render(context.Background(), &out); err != nil {
		t.Fatalf("render: %v", err)
	}

	parsed, err := html.Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("parse the rendered document: %v", err)
	}

	return parsed
}

// populated and empty are the two documents every audit runs over.
func populated(t *testing.T) *html.Node {
	t.Helper()

	return render(t, document(populatedList()))
}

func empty(t *testing.T) *html.Node {
	t.Helper()

	return render(t, document(emptyList()))
}

// both returns the two documents under a name.
func both(t *testing.T) map[string]*html.Node {
	t.Helper()

	return map[string]*html.Node{"populated": populated(t), "empty": empty(t)}
}

// --- The audits -------------------------------------------------------------------

// TestThePlaySurfaceSatisfiesTheStructuralContract is UI §10.2, as one test with a
// subtest per rule so a failure names the rule rather than "the a11y test failed".
//
// The rules are §10.2's own list plus the two it implies and does not spell out —
// heading levels and reference integrity — because a document can have a correct
// landmark and a broken outline, and an `aria-labelledby` that resolves to nothing
// is invisible in a rendering.
func TestThePlaySurfaceSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	t.Run("landmarks are present and distinguishing", func(t *testing.T) {
		t.Parallel()

		for name, parsed := range both(t) {
			auditLandmarksArePresentAndDistinguishing(t, name, parsed)
		}
	})

	t.Run("heading levels never skip", func(t *testing.T) {
		t.Parallel()

		for name, parsed := range both(t) {
			auditHeadingLevelsNeverSkip(t, name, parsed)
		}
	})

	t.Run("every reference resolves", func(t *testing.T) {
		t.Parallel()

		for name, parsed := range both(t) {
			auditEveryReferenceResolves(t, name, parsed)
		}
	})

	t.Run("no aria-hidden on a focus stop", func(t *testing.T) {
		t.Parallel()

		for name, parsed := range both(t) {
			auditNoAriaHiddenOnAFocusStop(t, name, parsed)
		}
	})

	t.Run("exactly one h1", func(t *testing.T) {
		t.Parallel()

		for name, parsed := range both(t) {
			auditExactlyOneH1(t, name, parsed)
		}
	})

	t.Run("no positive tabindex", func(t *testing.T) {
		t.Parallel()

		for name, parsed := range both(t) {
			auditNoPositiveTabindex(t, name, parsed)
		}
	})
}

// TestTheSurfaceHasNoRoleApplication is UI §7.6's prohibition and phase 9's DoD.
//
// `role="application"` tells a screen reader to stop interpreting keystrokes and
// pass them to the application. §7.6 prohibits it for the map, and the reason
// generalises to every surface in this package: the token list's whole design is
// that the browser's own keyboard handling — `Tab` reaching every row, `Enter`
// activating one — is what makes it operable, and `application` is the switch that
// turns that off.
//
// Asserted over the whole document rather than over the component, because the
// role would be just as wrong on the shell around it.
func TestTheSurfaceHasNoRoleApplication(t *testing.T) {
	t.Parallel()

	for name, parsed := range both(t) {
		auditNoRoleApplication(t, name, parsed)
	}
}

// TestEveryFocusStopCarriesTheTargetClass is UI §10.6, and it is the reason the
// markup carries the class rather than the script.
//
// §10.6 makes the target size "enforced by construction", and §7.3's construction
// is a shared utility plus a walk over every focus stop in the **served** document.
// A class applied from the script that arrives with the behaviour would be
// invisible to this walk — the same failure `internal/content/target.go` documents
// for rendered page bodies.
func TestEveryFocusStopCarriesTheTargetClass(t *testing.T) {
	t.Parallel()

	for name, parsed := range both(t) {
		auditEveryFocusStopCarriesTheTargetClass(t, name, parsed)
	}
}

// TestTheTokenListIsOperableByTheKeyboardAlone is phase 9's DoD item, and it is
// the load-bearing test in this file.
//
// The claim is deliberately the *strong* one: **the list is operable by keyboard
// with no JavaScript at all.** Every placement is a real `<button type="button">`,
// in document order, carrying no `tabindex` — so `Tab` reaches every one of them
// in turn and `Enter`/`Space` activates it, and nothing about that depends on a
// script being allowed to run. The arrow keys are an addition over that, not the
// thing making it work, and a roving-tabindex listbox would have taken the rows out
// of the tab order and made this claim false.
//
// Four assertions, each of which a specific defect breaks:
//
//   - every row is a `<button>` — a `<div role="button">` or a `<li>` fails it,
//     and so does a row whose control is a link to nowhere;
//   - every row's button is `type="button"` — a `<button>` with no type is
//     `type="submit"` inside a form and `type="reset"` outside one;
//   - **no row carries a `tabindex`** — this is the one that holds the rows in the
//     tab order, and `tabindex="-1"` on a row makes that row unreachable by `Tab`;
//   - **the number of focus stops inside the list is the number of placements** —
//     so a row that grew a second control (a nested link, a checkbox) fails rather
//     than quietly adding a tab stop the record does not mention.
func TestTheTokenListIsOperableByTheKeyboardAlone(t *testing.T) {
	t.Parallel()

	rows := elementsWith(t, populated(t), play.RowChromeHook)

	auditRowsAreKeyboardOperable(t, rows, len(populatedList().Placements))
}

// auditRowsAreKeyboardOperable is the body of the DoD claim, over already-located
// rows.
//
// Separate from the test so `controls_test.go` can run **this** body over a
// document built to break it. A copy for the control would be a second answer to
// the same question, and it is the copy that would keep passing.
func auditRowsAreKeyboardOperable(t auditer, rows []*html.Node, want int) {
	t.Helper()

	if len(rows) != want {
		t.Fatalf("the list renders %d rows for %d placements; the keyboard "+
			"operability claim is about every placement and a row that did not "+
			"render is one no key reaches", len(rows), want)
	}

	for index, row := range rows {
		if value := attributeOr(row, play.RowChromeHook); value != "" {
			t.Errorf("row %d carries %s=%q; the template writes it valueless, and "+
				"an HTML parser normalises a valueless attribute to the empty "+
				"string, so a non-empty value means somebody started using it as "+
				"data", index, play.RowChromeHook, value)
		}

		if row.Type != html.ElementNode || row.DataAtom != atomButton {
			t.Errorf("row %d is a <%s>, want <button>; only a real button is "+
				"activated by Enter and reachable by Space", index, row.Data)
		}

		if kind, _ := attribute(row, "type"); kind != "button" {
			t.Errorf("row %d is type=%q, want \"button\"; a button with no type "+
				"submits a form or resets one", index, kind)
		}

		if value, present := attribute(row, "tabindex"); present {
			t.Errorf("row %d carries tabindex=%q. §7.4 permits -1 and nothing "+
				"else, and this list has a stronger property to keep: no row "+
				"carries any tabindex, so the rows are in the tab order in "+
				"document order and Tab alone reaches every placement",
				index, value)
		}

		if id, present := attribute(row, play.RowIDAttribute); !present || id == "" {
			t.Errorf("row %d carries no %s; a row no client can identify is a row "+
				"no intent can be about", index, play.RowIDAttribute)
		}

		inside := focusStops(row)
		if len(inside) != 1 {
			t.Errorf("row %d contains %d focus stops, want exactly 1. A row with a "+
				"second control adds a tab stop the record does not mention, and a "+
				"row with none is not a control at all", index, len(inside))
		}
	}
}

// TestTheRowsCarryTheFourFactsTheRecordNames is §7.6's content claim: each row
// names the placement, its hit points, its conditions and its layer.
//
// **Asserted as the button's accessible name**, which is the concatenation of
// everything inside it. Asserting the four `data-` attributes instead would pass
// on a button whose visible text said something else, and asserting four separate
// text nodes would pass on a row whose name a screen reader never reads. The name
// is what a reader gets, so the name is what is asserted.
func TestTheRowsCarryTheFourFactsTheRecordNames(t *testing.T) {
	t.Parallel()

	rows := elementsWith(t, populated(t), play.RowChromeHook)

	if len(rows) == 0 {
		t.Fatal("the populated fixture renders no rows")
	}

	goblin := rows[0]

	for _, want := range []string{"Goblin", "Token", "7 of 7 hit points", "prone", "poisoned", "Layer: Tokens"} {
		if !strings.Contains(textOf(goblin), want) {
			t.Errorf("the first row's accessible name is %q, which does not "+
				"contain %q. §7.6 requires each row to name the placement, its hit "+
				"points, its conditions and its layer", textOf(goblin), want)
		}
	}

	// The duplicate condition is removed and the set sorted, so two clients that
	// added the same condition in opposite orders render identical bytes.
	if got := textOf(goblin); strings.Count(got, "prone") != 1 {
		t.Errorf("the first row's name is %q, which mentions `prone` %d times; the "+
			"condition set is de-duplicated during rendering so the row's bytes do "+
			"not depend on the order two clients acted in", got, strings.Count(got, "prone"))
	}

	if got, want := textOf(goblin), "Conditions: poisoned, prone"; !strings.Contains(got, want) {
		t.Errorf("the first row's name is %q, which does not contain %q. The "+
			"conditions are sorted so the row is deterministic", got, want)
	}

	// A placement with no conditions says nothing about them rather than
	// announcing an absence on every row.
	ogre := rows[1]

	if strings.Contains(textOf(ogre), "Condition") {
		t.Errorf("the second row's name is %q; a placement with no conditions "+
			"should say nothing about them, because most placements have none and "+
			"a line saying so on every row buries the rows that have some",
			textOf(ogre))
	}

	// A layer nobody supplied is stated as missing rather than omitted.
	if got := textOf(rows[3]); !strings.Contains(got, "Layer: "+play.LayerUnknown) {
		t.Errorf("the fourth row's name is %q, which does not say %q. §7.6's row "+
			"names the layer, and a row that silently has none reads as though the "+
			"placement has none", got, "Layer: "+play.LayerUnknown)
	}

	// A name nobody supplied falls back to the id rather than rendering an empty
	// subject with three facts attached to it.
	if got := textOf(rows[3]); !strings.Contains(got, "unknown-1") {
		t.Errorf("the fourth row's name is %q, which does not contain the "+
			"placement id; a row whose first word is empty announces three facts "+
			"with no subject", got)
	}
}

// TestEveryRowCarriesTheSelectionStateAndTheServerSelectsNothing is the selection
// contract.
//
// Every row writes `aria-pressed` explicitly, and **every one of them is `false`
// in a server-rendered document** — because selection is client state (§7.6: "Enter
// selects and moves the camera"), and a server rendering it as selected would be
// asserting something the reader did not do.
//
// The attribute is written rather than omitted on purpose: a toggle button with no
// `aria-pressed` is not a toggle button at all.
func TestEveryRowCarriesTheSelectionStateAndTheServerSelectsNothing(t *testing.T) {
	t.Parallel()

	rows := elementsWith(t, populated(t), play.RowChromeHook)
	if len(rows) == 0 {
		t.Fatal("the populated fixture renders no rows")
	}

	for index, row := range rows {
		value, present := attribute(row, "aria-pressed")
		if !present {
			t.Errorf("row %d carries no aria-pressed; a toggle button with no "+
				"aria-pressed is not a toggle button, and the client maintains "+
				"exactly this attribute", index)

			continue
		}

		if value != "false" {
			t.Errorf("row %d is aria-pressed=%q in a server-rendered document. "+
				"Selection is client state, so the server claims none — writing "+
				"%q here would assert a selection the reader did not make",
				index, value, value)
		}
	}

	// And the selected case is reachable, so `aria-pressed="false"` is not the
	// only value the function can produce.
	view := populatedList()
	view.Placements[1].Selected = true

	selected := elementsWith(t, render(t, document(view)), play.RowChromeHook)

	if got, _ := attribute(selected[1], "aria-pressed"); got != "true" {
		t.Errorf("the row a caller marked selected renders aria-pressed=%q, want "+
			"\"true\"; the two values are the whole of the selection state", got)
	}
}

// TestTheTokenSurfaceNeverNamesWorldOrSession is UI §1.2 and §10.2's last clause.
//
// The scan covers every text node, every comment, every attribute value and every
// attribute name — the four places a retired entity survives a grep that reads
// markup.
func TestTheTokenSurfaceNeverNamesWorldOrSession(t *testing.T) {
	t.Parallel()

	for name, parsed := range both(t) {
		auditNoRetiredVocabulary(t, name, parsed)
	}
}

// TestTheVocabularyCarveOutIsExactlyThePlacementName is what stops the audit above
// from being toothless.
//
// **A placement's name is campaign content.** A GM whose campaign has a token
// called "World Breaker" sees their own name on their own screen, and a rule that
// failed that document would be failing a reader for their own words — the mistake
// `internal/httpapi/search` records for a page title.
//
// The carve-out is therefore a **subtree test on the name span and nothing
// wider**: the hit points, the conditions, the layer and the kind label are
// semiplane's own vocabulary, and this test requires a finding for the word in
// each of those. A carve-out on the whole list would have made the audit vacuous
// exactly where it is most useful.
func TestTheVocabularyCarveOutIsExactlyThePlacementName(t *testing.T) {
	t.Parallel()

	// Inside the carve-out: no finding, and the audit has to be shown capable of
	// reporting nothing.
	carved := &html.Node{
		Type:     html.ElementNode,
		DataAtom: atomSpan,
		Data:     "span",
		Attr: []html.Attribute{
			{Key: "class", Val: "token-row-name"},
		},
	}
	text := &html.Node{Type: html.TextNode, Data: "World Breaker"}
	carved.AppendChild(text)

	if faults := retiredVocabularyFaults(carved); len(faults) != 0 {
		t.Errorf("the audit reported %v for a placement's own name. A token named "+
			"after something in a GM's campaign is the reader's own words, and the "+
			"carve-out for it is the point of the audit rather than a hole in it",
			faults)
	}

	// One element outside it, for each fact the row does state in semiplane's own
	// words. Each of these must produce a finding.
	for _, fact := range []struct {
		class string
		text  string
	}{
		{class: "token-row-hp", text: "world hit points"},
		{class: "token-row-conditions", text: "world-shaken"},
		{class: "token-row-layer", text: "Layer: the world"},
		{class: "token-row-kind", text: "world"},
	} {
		span := &html.Node{
			Type:     html.ElementNode,
			DataAtom: atomSpan,
			Data:     "span",
			Attr:     []html.Attribute{{Key: "class", Val: fact.class}},
		}
		span.AppendChild(&html.Node{Type: html.TextNode, Data: fact.text})

		faults := retiredVocabularyFaults(span)

		if len(faults) == 0 {
			t.Errorf("the audit accepted %q in a .%s. The carve-out is the "+
				"placement's name and nothing wider: hit points, conditions, the "+
				"layer and the kind label are semiplane's own vocabulary",
				fact.text, fact.class)
		}
	}

	// And the negative control the audit needs: the word inside an attribute the
	// carve-out does not reach.
	span := &html.Node{
		Type:     html.ElementNode,
		DataAtom: atomSpan,
		Data:     "span",
		Attr: []html.Attribute{
			{Key: "class", Val: "token-row-name"},
			{Key: "title", Val: "the world"},
		},
	}

	if len(retiredVocabularyFaults(span)) == 0 {
		t.Error("the audit accepted the word in a `title` on a carved-out element; " +
			"a title is announced, so a retired entity named there is named to the " +
			"reader the rule protects")
	}
}

// --- The audit bodies -------------------------------------------------------------

// landmarkRoles are the five §10.2 requires on every route.
//
// Scoped to them deliberately: a document also carries `tab`, `tabpanel`,
// `list` and `status` roles, and those are not landmarks. An audit that treated
// every role as a landmark would demand an accessible name of a `<button
// role="tab">` — whose name is its visible label and which is named by the
// tablist it belongs to — and would fail on a document that is correct.
var landmarkRoles = []string{"banner", "navigation", "main", "complementary", "contentinfo"}

// namedLandmarkRoles are the landmarks §7.2 requires a *name* on.
//
// A page has one banner and one main, so their role distinguishes them and a
// label would be noise. `navigation` and `complementary` are the two a document
// can have more than one of, and those are the two a reader cannot otherwise tell
// apart: the shell's utilities rail and the campaign navigation are both
// `complementary`-shaped and `navigation`-shaped respectively.
var namedLandmarkRoles = []string{"navigation", "complementary"}

// auditLandmarksArePresentAndDistinguishing is §10.2's second clause.
func auditLandmarksArePresentAndDistinguishing(t auditer, name string, parsed *html.Node) {
	t.Helper()

	found := map[string][]string{}

	walkAll(parsed, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		role, present := attribute(node, "role")
		if !present || !slices.Contains(landmarkRoles, role) {
			return
		}

		found[role] = append(found[role], accessibleName(node, parsed))
	})

	for _, role := range landmarkRoles {
		switch landmarks := found[role]; len(landmarks) {
		case 1:
		case 0:
			t.Errorf("%s: no element carries role=%q; §4.6 requires the landmark "+
				"on every route, and the campaign shell renders all five", name, role)
		default:
			t.Errorf("%s: %d elements carry role=%q (%q). §7.2's rule is that "+
				"landmarks are distinguishable, and two of the same role are one "+
				"landmark to a reader and two to a machine", name, len(landmarks),
				role, landmarks)
		}
	}

	for _, role := range namedLandmarkRoles {
		for _, label := range found[role] {
			if strings.TrimSpace(label) == "" {
				t.Errorf("%s: the %q landmark has no accessible name. A landmark a "+
					"reader cannot name is one a screen reader announces as "+
					"\"region\" and skips", name, role)
			}
		}
	}
}

// accessibleName resolves an element's name: its `aria-label`, else the text of
// whatever `aria-labelledby` points at.
//
// The two are the only sources this document uses, and resolving `aria-labelledby`
// rather than ignoring it is what lets the audit see the tablist's `aria-label`
// and a panel's heading at the same time.
func accessibleName(node, document *html.Node) string {
	if label, present := attribute(node, "aria-label"); present {
		return label
	}

	ids, present := attribute(node, "aria-labelledby")
	if !present {
		return ""
	}

	var out strings.Builder

	for id := range strings.FieldsSeq(ids) {
		element := elementWithID(document, id)
		if element == nil {
			continue
		}

		out.WriteString(textOf(element))
		out.WriteString(" ")
	}

	return strings.TrimSpace(out.String())
}

// auditHeadingLevelsNeverSkip is the outline rule §7.2 states about levels.
func auditHeadingLevelsNeverSkip(t auditer, name string, parsed *html.Node) {
	t.Helper()

	previous := 0

	walkAll(parsed, func(node *html.Node) {
		if node.Type != html.ElementNode || len(node.Data) != 2 || node.Data[0] != 'h' {
			return
		}

		level := int(node.Data[1] - '0')

		if previous != 0 && level > previous+1 {
			t.Errorf("%s: a <%s> follows a <h%d>. §7.2's rule is that a heading "+
				"level may not skip: a reader navigating by level meets nothing "+
				"between the two", name, node.Data, previous)
		}

		previous = level
	})
}

// auditEveryReferenceResolves is §10.2's reference integrity: an `aria-controls`,
// `aria-labelledby`, `aria-describedby` or a same-page `href` that names nothing is
// invisible in a rendering.
func auditEveryReferenceResolves(t auditer, name string, parsed *html.Node) {
	t.Helper()

	walkAll(parsed, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		for _, attributeName := range []string{
			"aria-controls", "aria-labelledby", "aria-describedby", "aria-owns",
		} {
			value, present := attribute(node, attributeName)
			if !present {
				continue
			}

			for id := range strings.FieldsSeq(value) {
				if elementWithID(parsed, id) == nil {
					t.Errorf("%s: <%s %s=%q> names %q, which is not in the "+
						"document. A reference that resolves to nothing is a label "+
						"or a control the reader is told about and cannot find",
						name, node.Data, attributeName, value, id)
				}
			}
		}

		if href, present := attribute(node, "href"); present && strings.HasPrefix(href, "#") {
			id := strings.TrimPrefix(href, "#")

			if elementWithID(parsed, id) == nil {
				t.Errorf("%s: <%s href=%q> names %q, which is not in the document",
					name, node.Data, href, id)
			}
		}
	})
}

// auditNoAriaHiddenOnAFocusStop is §10.2's fifth clause.
func auditNoAriaHiddenOnAFocusStop(t auditer, name string, parsed *html.Node) {
	t.Helper()

	walkAll(parsed, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		if value, present := attribute(node, "aria-hidden"); !present || value != "true" {
			return
		}

		if len(focusStops(node)) > 0 {
			t.Errorf("%s: <%s> is aria-hidden=\"true\" and contains %d focus "+
				"stop(s). §7.10 prohibits hiding a focusable element from assistive "+
				"technology: it is in the tab order and not in the tree, so a "+
				"reader can reach it and cannot perceive it",
				name, node.Data, len(focusStops(node)))
		}
	})
}

// auditExactlyOneH1 is §10.2's first clause.
func auditExactlyOneH1(t auditer, name string, parsed *html.Node) {
	t.Helper()

	count := 0

	walkAll(parsed, func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "h1" {
			count++
		}
	})

	if count != 1 {
		t.Errorf("%s: the document has %d <h1> elements, want exactly 1", name, count)
	}
}

// auditNoPositiveTabindex is §7.4's numeric rule.
//
// Numeric rather than lexical, for the reason `components/chat` records: `+1` and
// ` 1 ` sort before `"0"` as strings and would pass a comparison a browser reads
// as one.
func auditNoPositiveTabindex(t auditer, name string, parsed *html.Node) {
	t.Helper()

	walkAll(parsed, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		value, present := attribute(node, "tabindex")
		if !present {
			return
		}

		index, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			t.Errorf("%s: <%s> carries tabindex=%q, which is not a number",
				name, node.Data, value)

			return
		}

		if index > 0 {
			t.Errorf("%s: <%s> carries tabindex=%d. §7.4 permits -1 and 0; a "+
				"positive value is an author re-ordering the document's tab "+
				"sequence, and a reader who has learned one route cannot predict "+
				"another", name, node.Data, index)
		}
	})
}

// auditNoRoleApplication is UI §7.6's prohibition.
func auditNoRoleApplication(t auditer, name string, parsed *html.Node) {
	t.Helper()

	walkAll(parsed, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		if role, _ := attribute(node, "role"); role == "application" {
			t.Errorf("%s: <%s> carries role=\"application\". §7.6 prohibits it, and "+
				"it is the switch that stops a screen reader interpreting "+
				"keystrokes — which is precisely what makes this list operable "+
				"without a script", name, node.Data)
		}
	})
}

// auditEveryFocusStopCarriesTheTargetClass is UI §10.6.
//
// **The node itself, not its subtree.** The rule is a statement about an element:
// "this focus stop carries `target`". Testing each element's *descendants* would
// report every ancestor of a focusable control, which is how the first version of
// this audit failed with forty findings about `<html>` and `<body>` and taught
// nothing about the token list.
func auditEveryFocusStopCarriesTheTargetClass(t auditer, name string, parsed *html.Node) {
	t.Helper()

	walkAll(parsed, func(node *html.Node) {
		if node.Type != html.ElementNode || !focusable(node) {
			return
		}

		if !hasClass(node, "target") {
			t.Errorf("%s: <%s> is a focus stop and carries no `target` class. "+
				"§10.6 makes the target size enforced by construction, and §7.3's "+
				"construction is this class on the served document — a class "+
				"applied by the script that arrives with the behaviour would be "+
				"invisible to this walk", name, node.Data)
		}
	})
}

// auditNoRetiredVocabulary is UI §1.2 and §10.2's last clause.
func auditNoRetiredVocabulary(t auditer, name string, parsed *html.Node) {
	t.Helper()

	faults := retiredVocabularyFaults(parsed)
	if len(faults) == 0 {
		return
	}

	t.Errorf("%s: the document names a retired entity: %v", name, faults)
}

// retiredVocabularyFaults reports every place the document names a retired
// entity, skipping a placement's own name.
//
// The four node kinds are all scanned because each hides the word from the
// obvious check: a comment is invisible to a reader and obvious in a grep, a
// `title` is announced, an attribute *name* is invisible to a reader and obvious
// to a developer, and a text node is the case everybody checks.
func retiredVocabularyFaults(parsed *html.Node) []string {
	var faults []string

	walk(parsed, func(node *html.Node) bool {
		// The carve-out is "the placement's **name**", and it is scoped to the
		// name span's *text*. The span's own attributes are still scanned —
		// returning `false` before the attribute loop would make a `title` on the
		// name span invisible, and a title is announced.
		//
		// The walk then stops descending, which is what makes it "and nothing
		// inside it": a nested element inside the name span is not the name.
		carved := node.Type == html.ElementNode &&
			strings.Contains(attributeOr(node, "class"), "token-row-name")

		if !carved {
			// A switch rather than two `if`s so the `exhaustive` linter sees the
			// `default` and does not report a `html.NodeType` case that is missing
			// from a list this audit will never enumerate.
			switch node.Type {
			case html.TextNode:
				faults = append(faults, describeFault(node.Data, "text")...)
			case html.CommentNode:
				faults = append(faults, describeFault(node.Data, "comment")...)
			default:
			}
		}

		if node.Type != html.ElementNode {
			return true
		}

		for _, attr := range node.Attr {
			faults = append(faults, describeFault(attr.Val, attr.Key)...)
			faults = append(faults, describeFault(attr.Key, "attribute name")...)
		}

		return !carved
	})

	return faults
}

// describeFault reports the retired words in one piece of text, if any.
func describeFault(text, where string) []string {
	lowered := strings.ToLower(text)

	var found []string

	for _, word := range retiredEntities {
		if strings.Contains(lowered, word) {
			found = append(found, where+": "+strconv.Quote(text))
		}
	}

	return found
}
