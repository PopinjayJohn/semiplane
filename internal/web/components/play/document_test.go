package play_test

// The table's *document* — `play.Document` — and the claims only this package can
// make about it.
//
// # Why these live here and not in the route's suite
//
// `internal/httpapi/play` audits the bytes the product serves, and it is the
// right place for §10.2 over a real response: four documents, a GM's and a
// player's among them, mounted through a real gate. What that suite cannot reach
// is any branch the route does not take. `documentView` fills `Scene` with the
// zero value on every request — there is no scene image anywhere in the product
// today — so **the populated map surface has never been served by anything**,
// and a claim about it (`TestTheMapSurfaceSatisfiesTheClientContract`) can only
// be held against a fixture. That is the seam `document_models.go` names when it
// says the contract is "held over a fixture that does have an image".
//
// The action bar is the other half of the same argument. §4.9 fixes four
// controls and their order, and the route renders the same markup for every
// reader, so what is worth asserting here is the *shape* of the bar rather than
// a fourth copy of its bytes.
//
// # Four conventions, from `play_test.go`
//
// Parse the DOM; never substring-match the markup. Every audit must be capable of
// failing, and `TestTheDocumentLandmarksAuditObjectsToTheViolationItClaimsTo` is
// the control for the one new audit this file adds. The document, not the
// fragment: every rule below runs over a whole `Document` rather than over
// `actionBar` or `scene` alone, because landmarks, outline and reference
// integrity are properties of the whole. And nothing here reads a tier — ADR
// 0034 makes the tier a stylesheet's decision, so a test that asserted one would
// be asserting something this document does not carry.

import (
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/semiplane/semiplane/internal/web/components/chrome"
	"github.com/semiplane/semiplane/internal/web/components/play"
)

// --- Fixtures ----------------------------------------------------------------------

// sceneWithImage is a scene that has an image, in the client's own units.
//
// Nothing in the product can produce this value today: `domain.FrontMatter`
// carries no scene field and no asset route resolves "the campaign's current
// scene". The fixture exists because `static/js/map/table.js` refuses a surface
// with no `data-map-src`, and a contract that is never exercised against a
// populated surface is a contract holding only the branch that is easy.
func sceneWithImage() play.SceneView {
	return play.SceneView{
		Source: "/c/greyhaven/assets/maps/crypt.png",
		Width:  "2048",
		Height: "1536",
		Grid:   "70",
	}
}

// rollWithNotation is a system's grammar as the die sheet renders it.
//
// Two fields of four are enough for `available()` to answer true — a notation
// *and* at least one term — which is the predicate `rules.Grammar.Valid` applies.
func rollWithNotation() play.RollView {
	return play.RollView{
		Notation: "2d6-pool",
		Summary:  "Two six-sided dice, and the pool is the total.",
		Example:  "2d6+3",
		Terms: []play.RollTerm{
			{Name: "roll", Summary: "One die in the pool."},
		},
	}
}

// tableView is the document the route assembles, field for field.
//
// The same four models `documentView` fills — banner, navigation, footer with
// `Play` set, and the action bar's grammar — because a fixture that assembled
// them differently would be auditing markup the product does not serve. `Scene`
// is the one argument, and it is the whole reason this function exists.
func tableView(scene play.SceneView) play.DocumentView {
	return play.DocumentView{
		Title:   "Table — Greyhaven — semiplane",
		Heading: "Greyhaven",
		Header: chrome.HeaderView{
			InstanceName: "semiplane",
			Account:      chrome.AccountView{Username: "mira", SignOutHref: "/logout"},
			Campaign:     chrome.CampaignRef{Name: "Greyhaven", Slug: "greyhaven"},
			Connection:   chrome.ConnectionLive,
			Theme:        chrome.ThemeAuto,
		},
		Nav: navView(),
		Footer: chrome.FooterView{
			Version:    "0.3.0",
			StatusHref: "/status",
			Play: &chrome.PlayBar{
				Connection: chrome.ConnectionLive,
				LeaveHref:  "/c/greyhaven",
			},
		},
		Scene:   scene,
		Actions: play.ActionBarView{Roll: rollWithNotation()},
	}
}

// table renders the whole document with the rail this route mounts.
func table(t *testing.T, scene play.SceneView) *html.Node {
	t.Helper()

	return render(t, play.Document(tableView(scene), play.Rail(railView())))
}

// tableDocuments is both shapes of this document, under a name.
//
// Two rather than one because the scene branch is not a styling difference: the
// empty branch renders a designed empty state and **no `data-map-surface` at
// all**, which is a different document by every measure below.
func tableDocuments(t *testing.T) map[string]*html.Node {
	t.Helper()

	return map[string]*html.Node{
		"with a scene":    table(t, sceneWithImage()),
		"without a scene": table(t, play.SceneView{}),
	}
}

// byTestID finds an element by its `data-testid` hook, failing when there is none.
//
// A hook and not an id: `ActionsTestID` and its fellows are unique in the
// document but carried in `data-testid`, and a helper that looked for one as an
// id would find nothing and report "changed nothing" — the silent failure
// `internal/httpapi/play`'s controls exist to make loud. Failing rather than
// returning nil, because every caller below needs the element.
func byTestID(t *testing.T, root *html.Node, testID string) *html.Node {
	t.Helper()

	for _, node := range elementsWith(t, root, "data-testid") {
		if attributeOr(node, "data-testid") == testID {
			return node
		}
	}

	t.Fatalf("no element carries data-testid=%q", testID)

	return nil
}

// childTestIDs returns the `data-testid` of every focus stop inside a subtree,
// in document order, skipping any nested dialog.
//
// The dialog skip is what makes "four controls" a count of the bar's own
// controls: `ui.Dialog` renders its trigger *and* its panel inside the `<nav>`,
// and the panel's Close button is a focus stop the bar does not offer.
func childTestIDs(t *testing.T, root *html.Node) []string {
	t.Helper()

	found := make([]string, 0, 4)

	walk(root, func(node *html.Node) bool {
		// The dialog *and* its subtree: `ui.Dialog` renders the panel inside the
		// bar, and the panel carries `tabindex="-1"` — so skipping only its
		// descendants would still count the panel itself as a control.
		if attributeOr(node, "role") == "dialog" {
			return false
		}

		if node.Type == html.ElementNode && focusable(node) {
			if id := attributeOr(node, "data-testid"); id != "" {
				found = append(found, id)
			}
		}

		return true
	})

	return found
}

// --- §4.9's four controls ------------------------------------------------------------

// TestTheActionBarPresentsTheFourControlsInTheOrderTheRecordGives is UI §4.9's
// control row: **Token · Roll · End turn · Chat**.
//
// The order is the assertion, and it is asserted twice — by the elements' hooks
// and by their accessible names — because either alone passes on a bar that is
// wrong in the other way: hooks survive a label change that makes the row read
// differently, and names survive a hook rename that breaks the client.
//
// Four and no more. The count is what stops a fifth control appearing without
// the record being asked; §4.9 lists four, and "there is no Move control on a
// phone" (asserted separately below) is the reason the list is closed rather
// than illustrative.
func TestTheActionBarPresentsTheFourControlsInTheOrderTheRecordGives(t *testing.T) {
	t.Parallel()

	for name, parsed := range tableDocuments(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			bar := byTestID(t, parsed, play.ActionsTestID)

			if role := attributeOr(bar, "role"); role != "navigation" {
				t.Errorf("the action bar carries role=%q, want \"navigation\"; §7.2's "+
					"landmark list is header · nav · main · aside · footer and a bar that "+
					"is not a landmark is a row of controls a screen reader cannot jump to",
					role)
			}

			if label := attributeOr(bar, "aria-label"); label != play.ActionsLabel {
				t.Errorf("the action bar is labelled %q, want %q; §7.2 requires "+
					"distinguishing labels wherever a document carries more than one "+
					"navigation, and this document carries the campaign navigation too",
					label, play.ActionsLabel)
			}

			got := childTestIDs(t, bar)

			want := []string{
				play.TokenTestID,
				// `ui.Dialog` derives the trigger's hook from the panel's id, which
				// is the coupling `RollTestID`'s comment states; spelled here so a
				// change to the derivation fails a test rather than a rendering.
				play.RollTestID + "-trigger",
				play.EndTurnTestID,
				play.ChatTestID,
			}

			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("the action bar renders %v, want %v in that order; §4.9 lists "+
					"Token · Roll · End turn · Chat and the order is what makes the bar's "+
					"primary action the one a thumb lands on", got, want)
			}

			// The names, which is what a reader sees, and the element type that
			// makes each one operable with no script at all.
			names := map[string]string{
				play.TokenTestID:             "Token",
				play.RollTestID + "-trigger": "Roll",
				play.EndTurnTestID:           "End turn",
				play.ChatTestID:              "Chat",
			}

			for _, testID := range got {
				node := byTestID(t, bar, testID)

				if node.Data != "button" {
					t.Errorf("the bar's control %q is a <%s>, want <button>; §4.9's "+
						"controls are real buttons, which is what makes them operable "+
						"with no script", testID, node.Data)
				}

				if kind := attributeOr(node, "type"); kind != "button" {
					t.Errorf("the bar's control %q is type=%q, want \"button\"; a "+
						"<button> with no type submits a form", testID, kind)
				}

				want, listed := names[testID]
				if !listed {
					continue
				}

				if got := textOf(node); got != want {
					t.Errorf("the bar's control %q reads %q, want %q; §4.9 lists the four "+
						"by the names a reader acts on", testID, got, want)
				}
			}
		})
	}
}

// TestTheActionBarSitsBetweenTheRailAndTheFooter is `document.templ`'s grid claim,
// in source order.
//
// The bar is a sibling of the landmarks rather than a child of any of them: not
// inside the navigation it replaces at compact, and not inside the footer it
// sits beside. Only the order can be checked without a browser, and the order is
// the part a template edit gets wrong — a bar moved inside `<main>` would still
// render at compact and would have put four controls inside the region a skip
// link jumps *past*.
func TestTheActionBarSitsBetweenTheRailAndTheFooter(t *testing.T) {
	t.Parallel()

	for name, parsed := range tableDocuments(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			shell := byTestID(t, parsed, "shell")

			order := make([]string, 0, 3)

			walk(shell, func(node *html.Node) bool {
				if node.Type != html.ElementNode {
					return true
				}

				switch testID := attributeOr(node, "data-testid"); testID {
				case "shell-rail", play.ActionsTestID, "shell-footer":
					order = append(order, testID)
				}

				return true
			})

			want := []string{"shell-rail", play.ActionsTestID, "shell-footer"}

			if strings.Join(order, ",") != strings.Join(want, ",") {
				t.Errorf("the shell renders %v, want %v; the action bar sits between the "+
					"rail and the footer because that is the row of the grid it occupies, "+
					"and it is neither inside the navigation nor inside the footer", order,
					want)
			}
		})
	}
}

// TestThereIsNoMoveControlOnAPhone is §4.9's "there is no Move control on a
// phone", held by absence rather than by a disabled state.
//
// The record's own reasoning: "an absent control is worse than a disabled one
// with a reason". So this is not a check that a Move button is `aria-disabled` —
// it is that no control anywhere in the document offers positional play, in the
// name it is announced under, its `title`, or (for a control whose name comes
// from its contents) its visible text. Positional interaction exists at
// fine-pointer tiers and on TV through the token list (§7.6), so a control here
// would be a control that works on no device this tier can reach.
//
// What is checked is the *name*, not the subtree's text. The rail's `<aside>`
// and the token list's `<section>` are focus stops too — `tabindex="-1"` is what
// makes activating a token land somewhere — and both contain the sentence "Up
// and down move between tokens", which is a fact about the token list rather
// than an offer. Reading subtree text as a control's name flagged both, which is
// the audit objecting to prose; resolving the name the way assistive technology
// does is what makes the rule about offers.
func TestThereIsNoMoveControlOnAPhone(t *testing.T) {
	t.Parallel()

	for name, parsed := range tableDocuments(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if offers := positionalPlayOffers(parsed); len(offers) > 0 {
				t.Errorf("the document offers positional play through %v; §4.9 states "+
					"there is no Move control on a phone, and an absent control is better "+
					"than a disabled one that suggests the gesture exists", offers)
			}
		})
	}
}

// positionalPlayOffers returns every focus stop whose name is the Move command,
// as `element:text`.
//
// Extracted so the control below runs *this* rule rather than a second copy of
// it: a control that reimplemented the walk would be checking its own reading of
// the rule and would stay green when the rule itself was loosened.
func positionalPlayOffers(parsed *html.Node) []string {
	var offers []string

	walkAll(parsed, func(node *html.Node) {
		if node.Type != html.ElementNode || !focusable(node) {
			return
		}

		for _, source := range []string{
			controlName(node, parsed),
			attributeOr(node, "title"),
		} {
			if namesPositionalPlay(source) {
				offers = append(offers, node.Data+":"+strings.TrimSpace(source))
			}
		}
	})

	return offers
}

// controlName resolves the name a focus stop is announced with: its ARIA name
// where it has one, else its contents — but only for the elements whose name
// *comes* from their contents.
//
// `button`, `a[href]` and `summary` are the three in this document; everything
// else (`aside`, `section`, `main`, any `tabindex` holder) is named by an
// attribute or not at all, and handing it `textOf` would be reading a
// container's whole subtree as though it were a label. That is precisely the
// mistake this helper exists to avoid: `textOf(aside)` is every word the rail
// renders, and one of them is "move".
func controlName(node, document *html.Node) string {
	if name := accessibleName(node, document); name != "" {
		return name
	}

	switch node.Data {
	case "button", "summary", "a":
		return textOf(node)
	default:
		return ""
	}
}

// namesPositionalPlay reports whether a control's name is the Move command.
//
// Word-matched rather than substring-matched, so a control called "Remove
// condition" is not mistaken for one: what §4.9 rules out is the verb that moves
// a placement, and the token list's rows carry sentences where that verb is a
// word in the middle of a fact about somebody else.
func namesPositionalPlay(name string) bool {
	for word := range strings.FieldsSeq(strings.ToLower(name)) {
		if word == "move" || word == "moves" || word == "moving" {
			return true
		}
	}

	return false
}

// TestTheNoMoveControlRuleObjectsToTheOfferItClaimsTo holds the absence rule the
// way the landmark rule is held: one fixture that *is* the violation, and one
// that proves the rule does not fire on prose.
//
// The second is the important one. The first version of this test read every
// focus stop's subtree text as its name, so the rail's own sentence "Up and down
// move between tokens" — a fact about the token list, rendered inside a
// `<aside tabindex="-1">` — was read as an offer and the test was red on the
// product it was written for. A rule that fires on prose is a rule that will be
// deleted rather than narrowed, so both directions get a fixture.
func TestTheNoMoveControlRuleObjectsToTheOfferItClaimsTo(t *testing.T) {
	t.Parallel()

	t.Run("a control whose name is Move", func(t *testing.T) {
		t.Parallel()

		parsed := table(t, sceneWithImage())

		bar := elementWithAttribute(parsed, "data-testid", play.ActionsTestID)
		if bar == nil {
			t.Fatal("the document has no action bar, so the fixture changed nothing")
		}

		button := &html.Node{
			Type:     html.ElementNode,
			Data:     "button",
			DataAtom: atom.Lookup([]byte("button")),
			Attr: []html.Attribute{
				{Key: "type", Val: "button"},
				{Key: "data-testid", Val: "mutant-move"},
			},
		}
		button.AppendChild(&html.Node{Type: html.TextNode, Data: "Move"})
		bar.AppendChild(button)

		offers := positionalPlayOffers(parsed)
		if len(offers) == 0 {
			t.Error("the rule accepted a document carrying a button named \"Move\"; " +
				"an absence rule that cannot see the thing it rules out is a green " +
				"light wired to nothing")
		}

		if !strings.Contains(strings.Join(offers, " "), "Move") {
			t.Errorf("the rule fired, but not about the Move control: %v", offers)
		}
	})

	t.Run("a control labelled Move by aria-label", func(t *testing.T) {
		t.Parallel()

		parsed := table(t, sceneWithImage())

		token := elementWithAttribute(parsed, "data-testid", play.TokenTestID)
		if token == nil {
			t.Fatal("the document has no Token control, so the fixture changed nothing")
		}

		setAttribute(token, "aria-label", "Move")

		offers := positionalPlayOffers(parsed)
		if len(offers) == 0 {
			t.Error("the rule accepted a control whose aria-label reads \"Move\"; a " +
				"visible label is only one of the three places a control can offer " +
				"positional play")
		}
	})

	t.Run("a rail whose prose mentions moving and no control offering it", func(t *testing.T) {
		t.Parallel()

		parsed := table(t, sceneWithImage())

		rail := elementWithAttribute(parsed, "data-testid", "shell-rail")
		if rail == nil {
			t.Fatal("the document has no rail, so the fixture's prose is missing")
		}

		// The fixture has to be what it claims: prose containing the verb, inside
		// a focus stop, with no control offering anything. Without this the
		// assertion below could pass on a document that simply mentions nothing.
		if prose := strings.ToLower(textOf(rail)); !strings.Contains(prose, "move") {
			t.Fatalf("the rail's prose does not mention moving (%q), so this fixture "+
				"tests nothing; the rule's false positive was found in exactly this "+
				"sentence", prose)
		}

		if offers := positionalPlayOffers(parsed); len(offers) != 0 {
			t.Errorf("the rule reads a container's prose as an offer: %v. §4.9's rule "+
				"is about controls, and a sentence inside a landmark that happens to "+
				"contain the verb is not one", offers)
		}
	})
}

// TestTheTwoUnavailableControlsStateTheirReasonAndRemainReachable is §4.9's
// "disabled with a stated reason", and the word *reachable* is the whole claim.
//
// A native `disabled` button is not focusable, so its `aria-describedby` is read
// by nobody: the reason would exist in the markup and nowhere else. `aria-disabled`
// keeps the control in the tab order — which is the one property that makes the
// reason reachable at all — and it *is* the ARIA disabled state, so assistive
// technology reports the control as unavailable.
//
// Four assertions per control, each breaking a different way:
//
//   - `aria-disabled` present, so the state is announced;
//   - **no `disabled` attribute**, so the control stays focusable;
//   - `aria-describedby` resolving to a node whose text is the reason, so the
//     sentence is the one `document_models.go` owns rather than a paraphrase;
//   - `title` carrying the same sentence, for a pointer user who never focuses
//     it — the cost of this design, stated rather than hidden, is that a reader
//     who cannot focus the control sees no reason at rest.
func TestTheTwoUnavailableControlsStateTheirReasonAndRemainReachable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		what     string
		testID   string
		reasonID string
		reason   string
	}{
		{
			what:     "End turn",
			testID:   play.EndTurnTestID,
			reasonID: play.EndTurnReasonID,
			reason:   play.EndTurnReason,
		},
		{
			what:     "Chat",
			testID:   play.ChatTestID,
			reasonID: play.ChatReasonID,
			reason:   play.ChatReason,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.what, func(t *testing.T) {
			t.Parallel()

			for name, parsed := range tableDocuments(t) {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					button := byTestID(t, parsed, testCase.testID)

					if state := attributeOr(button, "aria-disabled"); state != "true" {
						t.Errorf("%s carries aria-disabled=%q, want \"true\"; §4.9 asks for "+
							"the control to be disabled with a stated reason and the state is "+
							"what assistive technology announces", testCase.what, state)
					}

					if _, present := attribute(button, "disabled"); present {
						t.Errorf("%s carries a native `disabled` attribute; a disabled "+
							"button is not focusable, so its reason is read by nobody — the "+
							"one property that makes the stated reason reachable at all",
							testCase.what)
					}

					describedBy := strings.Fields(attributeOr(button, "aria-describedby"))
					if len(describedBy) != 1 || describedBy[0] != testCase.reasonID {
						t.Errorf("%s is described by %v, want exactly [%s]; a reference to "+
							"nothing is a reason the reader is told about and cannot find, and "+
							"a reference to two nodes is two reasons",
							testCase.what, describedBy, testCase.reasonID)
					}

					reasonNode := elementWithID(parsed, testCase.reasonID)
					if reasonNode == nil {
						t.Fatalf("no element carries id=%q; %s's reason is not in the "+
							"document", testCase.reasonID, testCase.what)
					}

					if got := textOf(reasonNode); got != testCase.reason {
						t.Errorf("%s's reason reads %q, want %q; the sentence is the whole "+
							"of the rule and a paraphrase is a second copy of it", testCase.what,
							got, testCase.reason)
					}

					if !hasClass(reasonNode, "visually-hidden") {
						t.Errorf("%s's reason is not `.visually-hidden`; a reason rendered "+
							"visibly would take two sentences of prose out of a 4.5rem bar "+
							"carrying four controls at 320px, and a reason left in no tree at "+
							"all would be unreadable", testCase.what)
					}

					if title := attributeOr(button, "title"); title != testCase.reason {
						t.Errorf("%s carries title=%q, want %q; a pointer user who cannot "+
							"focus the control sees no reason at rest, and `title` is where "+
							"this design puts it", testCase.what, title, testCase.reason)
					}

					if !focusable(button) {
						t.Errorf("%s is not focusable, so its reason is unreachable; that is "+
							"exactly the failure `aria-disabled` exists to avoid", testCase.what)
					}
				})
			}
		})
	}
}

// --- The map surface --------------------------------------------------------------

// TestTheMapSurfaceSatisfiesTheClientContract is `document_models.go`'s promise,
// over the fixture that has an image.
//
// The client's contract is fixed and non-negotiable: `mount` **refuses** unless
// the surface carries `aria-hidden="true"` and a non-empty `data-map-src`. Two
// claims with a different failure each — a surface that is not hidden announces
// an unlabelled graphic, and a surface with no image throws on every load of
// every table, which is indistinguishable from a renderer defect. The optional
// trio (`Width`, `Height`, `Grid`) is asserted in both directions: present when
// supplied, absent when not, because `data-map-width=""` is a size the client
// would parse rather than ignore.
func TestTheMapSurfaceSatisfiesTheClientContract(t *testing.T) {
	t.Parallel()

	parsed := table(t, sceneWithImage())

	surfaces := elementsWith(t, parsed, "data-map-surface")
	if len(surfaces) != 1 {
		t.Fatalf("a populated scene renders %d surfaces, want exactly 1; a second "+
			"surface is a second mount, and the client walks `[data-map-surface]`",
			len(surfaces))
	}

	surface := surfaces[0]

	if hidden := attributeOr(surface, "aria-hidden"); hidden != "true" {
		t.Errorf("the map surface carries aria-hidden=%q, want \"true\"; a surface "+
			"that is not hidden announces an unlabelled graphic, and the client refuses "+
			"to mount it", hidden)
	}

	if source := attributeOr(surface, "data-map-src"); source != sceneWithImage().Source {
		t.Errorf("the map surface carries data-map-src=%q, want %q; a surface with no "+
			"image is one that throws on every load, which is why the empty branch "+
			"renders no surface at all", source, sceneWithImage().Source)
	}

	for _, optional := range []struct{ attribute, want string }{
		{attribute: "data-map-width", want: sceneWithImage().Width},
		{attribute: "data-map-height", want: sceneWithImage().Height},
		{attribute: "data-map-grid", want: sceneWithImage().Grid},
	} {
		if got := attributeOr(surface, optional.attribute); got != optional.want {
			t.Errorf("the map surface carries %s=%q, want %q", optional.attribute, got,
				optional.want)
		}
	}

	// §4.9: at coarse-pointer tiers the map is a **read-only readout** — it
	// renders, it never takes a gesture. What can be checked in markup is that
	// nothing inside it is a focus stop and that no inline handler is attached:
	// a keyboard that can reach the surface is a keyboard that can act on it, and
	// an inline handler is the one pointer behaviour a server can see.
	if stops := focusStops(surface); len(stops) != 0 {
		t.Errorf("the map surface contains %d focus stop(s); §4.9 makes it a read-only "+
			"readout at coarse-pointer tiers, and a focus stop inside it is a gesture "+
			"the document offers", len(stops))
	}

	walkAll(surface, func(node *html.Node) {
		for _, attr := range node.Attr {
			if strings.HasPrefix(attr.Key, "on") {
				t.Errorf("the map surface carries %s=…; §4.9's read-only readout takes no "+
					"gesture, and an inline handler is pointer behaviour the server can see",
					attr.Key)
			}
		}
	})

	// The surface is the centre's own child, which is what makes the measure
	// override in `play.css` a statement about this element.
	if !inside(elementWithID(parsed, "main"), surface) {
		t.Error("the map surface is not inside <main>; the centre is the landmark the " +
			"measure override applies to, and a surface anywhere else is a surface the " +
			"grid does not lay out")
	}
}

// TestTheMapSurfaceIsAbsentWhenThereIsNoScene is `document.templ`'s promise about
// the empty branch, and it is the branch the product serves today.
//
// Absent rather than empty: a surface with no `data-map-src` is a surface the
// client refuses to mount, and the refusal would be an error on every table,
// every load, forever — indistinguishable from a renderer defect. So the two
// branches are "a map" and "no map", never "a map with nothing in it", and this
// test is what makes the difference a property of the markup rather than of the
// absence of a test.
func TestTheMapSurfaceIsAbsentWhenThereIsNoScene(t *testing.T) {
	t.Parallel()

	parsed := table(t, play.SceneView{})

	if surfaces := elementsWith(t, parsed, "data-map-surface"); len(surfaces) != 0 {
		t.Fatalf("a document with no scene renders %d surface(s); a surface with no "+
			"image is one the client refuses, and the honest markup is no surface at all",
			len(surfaces))
	}

	if sources := elementsWith(t, parsed, "data-map-src"); len(sources) != 0 {
		t.Errorf("a document with no scene carries data-map-src on %d element(s); the "+
			"attribute is the client's precondition and it must not be present empty",
			len(sources))
	}

	// §4.7's designed empty state, in the element that replaces the surface.
	empty := byTestID(t, parsed, "play-map-empty")

	if got := textOf(empty); !strings.Contains(got, "no scene") {
		t.Errorf("the empty state reads %q, which does not say there is no scene; "+
			"§4.7's rule is that an empty state names what is missing rather than "+
			"rendering nothing", got)
	}
}

// --- The structural contract over this document -----------------------------------

// TestTheDocumentSatisfiesTheStructuralContract is UI §10.2 over both shapes of
// this document, run through the audit bodies `play_test.go` already holds.
//
// The same functions, not copies: an audit copied here for the document's sake
// would be a second answer to "does this document satisfy §10.2", and the two
// would drift the first time one was fixed. The landmark rule is the one
// exception, and `auditDocumentLandmarks` below says why.
func TestTheDocumentSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	audits := []struct {
		name  string
		audit func(auditer, string, *html.Node)
	}{
		{name: "exactly one h1", audit: auditExactlyOneH1},
		{name: "heading levels never skip", audit: auditHeadingLevelsNeverSkip},
		{name: "every reference resolves", audit: auditEveryReferenceResolves},
		{name: "no aria-hidden on a focus stop", audit: auditNoAriaHiddenOnAFocusStop},
		{name: "no positive tabindex", audit: auditNoPositiveTabindex},
		{name: "no role application", audit: auditNoRoleApplication},
		{
			name:  "every focus stop carries the target class",
			audit: auditEveryFocusStopCarriesTheTargetClass,
		},
		{name: "no retired vocabulary", audit: auditNoRetiredVocabulary},
		{name: "landmarks are present and distinguishing", audit: auditDocumentLandmarks},
	}

	for name, parsed := range tableDocuments(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, rule := range audits {
				t.Run(rule.name, func(t *testing.T) {
					t.Parallel()

					rule.audit(t, rule.name, parsed)
				})
			}
		})
	}
}

// auditDocumentLandmarks is §10.2's landmark rule as *this document* states it:
// the five landmarks, and **two** navigation landmarks with distinct labels.
//
// # Why this is not `auditLandmarksArePresentAndDistinguishing`
//
// That body is the shell's, and it requires at most one element per landmark
// role — which is true of every document `components.CampaignShell` renders and
// false of this one by design. `Document` carries the campaign navigation
// ("Campaign") *and* UI §4.9's action bar ("Table actions"), both `navigation`,
// and §7.2's rule for that case is not "one of these" but "distinguishing
// labels". So the difference is the record's and not a divergence to reconcile:
// two audits reading one rule as one-landmark and the other as
// distinct-labels disagree only on documents neither has in common.
//
// The label set is a set rather than two literals, for the reason the route's
// own audit gives: naming only "Campaign" would fail this document the moment
// the bar landed, and naming only "Table actions" would demand a bar on a
// document that has none.
func auditDocumentLandmarks(t auditer, name string, parsed *html.Node) {
	t.Helper()

	type region struct {
		role string
		name string
	}

	var found []region

	walkAll(parsed, func(node *html.Node) {
		if node.Type != html.ElementNode {
			return
		}

		role := documentLandmarkRole(node)
		if role == "" {
			return
		}

		found = append(found, region{role: role, name: accessibleName(node, parsed)})
	})

	// One element per role, except navigation — which is exactly the pair the
	// record places here.
	want := map[string]int{
		"banner": 1, "main": 1, "complementary": 1, "contentinfo": 1,
		"navigation": 2,
	}

	counted := map[string]int{}
	navigationNames := make([]string, 0, 2)

	for _, region := range found {
		counted[region.role]++

		switch region.role {
		case "navigation":
			navigationNames = append(navigationNames, region.name)
		case "banner", "main", "complementary", "contentinfo":
		case "region", "search":
			// §10.2's rule is *distinguishing labels*, not "one of the five".
			// `region` and `search` are ARIA landmarks a document may add without
			// the record describing them — the token list's `<section
			// role="region">` named by its visible heading is this document's own —
			// so what they are held to is the same condition the route's audit puts
			// on them (`route_a11y_test.go`'s `needsName`): they exist only when
			// they carry a name. An unnamed one is announced as "region" and
			// skipped, which is worse than not being there.
			if strings.TrimSpace(region.name) == "" {
				t.Errorf("%s: the %s landmark has no accessible name; §10.2's rule is "+
					"distinguishing labels, and a landmark a reader cannot name is one a "+
					"screen reader announces as \"region\" and skips", name, region.role)
			}
		default:
			t.Errorf("%s: %s is a %s landmark, which is not one this document places; "+
				"a landmark the record does not describe is a region a reader can jump to "+
				"and nobody knows what it is for", name, region.name, region.role)
		}
	}

	for role, expected := range want {
		if counted[role] != expected {
			t.Errorf("%s: the document has %d %s landmarks, want %d (UI §10.2)", name,
				counted[role], role, expected)
		}
	}

	// Both directions of §7.2's pairing: every navigation is named, the two
	// names differ, and each is one of the two the record fixes.
	allowed := map[string]bool{"Campaign": true, play.ActionsLabel: true}
	seen := map[string]bool{}

	for _, label := range navigationNames {
		if strings.TrimSpace(label) == "" {
			t.Errorf("%s: a navigation landmark has no accessible name; §7.2 requires "+
				"distinguishing labels or landmark navigation is useless", name)

			continue
		}

		if !allowed[label] {
			t.Errorf("%s: a navigation landmark is labelled %q; §4.3 and §4.9 name them "+
				"%q (the campaign navigation) and %q (the table's action bar)", name, label,
				"Campaign", play.ActionsLabel)
		}

		if seen[label] {
			t.Errorf("%s: two navigation landmarks are both labelled %q; §7.2's rule is "+
				"that landmarks are distinguishable, and two with one name are one "+
				"landmark to a reader", name, label)
		}

		seen[label] = true
	}
}

// documentLandmarkRole is an element's effective landmark role, or "".
//
// The explicit role where there is one and the implicit role of the tag where
// there is not, because an explicit role *replaces* the implicit one — which is
// what makes `role="navigation"` on a `<div>` a navigation landmark. The tag
// list is §7.2's own five; `region` and `search` are deliberately absent from it
// because `<section>` is a region **only when it is named**, and deriving that
// from the tag would mean resolving every section's name before deciding whether
// it is a landmark at all. Both arrive through an explicit role instead, and
// `auditDocumentLandmarks` holds them to the name rather than to existence.
func documentLandmarkRole(node *html.Node) string {
	role := attributeOr(node, "role")
	if role != "" {
		// An explicit role replaces the implicit one, and only a landmark role
		// makes this element a landmark: `role="tablist"`, `role="dialog"` and
		// `role="status"` are all present in this document and none of them is a
		// region a reader jumps to. Counting them would fail a document that is
		// correct, which is the failure mode this whole file is written against.
		if slices.Contains(documentLandmarkRoles, role) {
			return role
		}

		return ""
	}

	switch node.Data {
	case "header":
		return "banner"
	case "nav":
		return "navigation"
	case "main":
		return "main"
	case "aside":
		return "complementary"
	case "footer":
		return "contentinfo"
	default:
		return ""
	}
}

// documentLandmarkRoles are the roles landmark navigation can jump to — UI §7.2's
// five plus `region` and `search`, which ARIA also treats as landmarks.
//
// `region` is in the set because the token list is a `<section>` named by its
// own visible heading, and a named section *is* `role="region"`; it is counted
// but not one of the five this document must place, so `auditDocumentLandmarks`
// requires it to be named rather than requiring it to exist. `search` is in the
// set for the same reason the route's audit lists it: the banner's search form
// would be a landmark the moment a route filled `HeaderView.Search`, and a set
// that did not know about it would silently count it as nothing.
var documentLandmarkRoles = []string{
	"banner", "navigation", "main", "complementary", "contentinfo", "region", "search",
}

// --- The control for the new audit -------------------------------------------------

// TestTheDocumentLandmarksAuditObjectsToTheViolationItClaimsTo holds the new
// audit the same way `controls_test.go` holds the others: four documents built
// to break it, and a finding from each.
//
// Four rather than one because the rule is four claims — the landmarks are
// present, the navigation pair is named, the two names differ, and the two
// landmarks this document *adds* to the five (the token list's `region`) are
// named too — and an audit that could only catch the first would be an audit
// that passes a document whose two navigations are indistinguishable. Each
// fixture mutates a document this package really renders, so the only difference
// between it and a passing one is the single change under test.
func TestTheDocumentLandmarksAuditObjectsToTheViolationItClaimsTo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		what     string
		mentions string
		breakIt  func(parsed *html.Node) bool
	}{
		{
			what:     "a navigation with no accessible name",
			mentions: "accessible name",
			breakIt: func(parsed *html.Node) bool {
				bar := elementWithAttribute(parsed, "data-testid", play.ActionsTestID)
				if bar == nil {
					return false
				}

				removeAttribute(bar, "aria-label")

				return true
			},
		},
		{
			// The conditional half of the `region` rule. The audit accepts a
			// `region` landmark because the token list is one, so the only thing
			// standing between that and an unnamed region nobody can navigate to is
			// this name check — and a fixture that drops `aria-labelledby` is what
			// proves the check is there rather than assumed.
			what:     "a region with no accessible name",
			mentions: "the region landmark has no accessible name",
			breakIt: func(parsed *html.Node) bool {
				tokenList := elementWithAttribute(parsed, "role", "region")
				if tokenList == nil {
					return false
				}

				removeAttribute(tokenList, "aria-labelledby")

				return true
			},
		},
		{
			what:     "two navigation landmarks with one label",
			mentions: "both labelled",
			breakIt: func(parsed *html.Node) bool {
				bar := elementWithAttribute(parsed, "data-testid", play.ActionsTestID)
				if bar == nil {
					return false
				}

				setAttribute(bar, "aria-label", "Campaign")

				return true
			},
		},
		{
			what:     "a missing contentinfo",
			mentions: "contentinfo",
			breakIt: func(parsed *html.Node) bool {
				footer := elementWithAttribute(parsed, "data-testid", "shell-footer")
				if footer == nil {
					return false
				}

				// An explicit role replaces the implicit one, so the footer stops
				// being `contentinfo` without leaving the document.
				setAttribute(footer, "role", "article")

				return true
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.what, func(t *testing.T) {
			t.Parallel()

			parsed := table(t, sceneWithImage())

			if !testCase.breakIt(parsed) {
				t.Fatalf("the fixture for %q changed nothing, so the audit was never "+
					"given the violation it claims to catch", testCase.what)
			}

			runControl(t, "document landmarks", testCase.mentions, func(rec *recorder) {
				auditDocumentLandmarks(rec, "control", parsed)
			})
		})
	}

	t.Run("the audit reports nothing about the document as rendered", func(t *testing.T) {
		t.Parallel()

		rec := &recorder{TB: t}

		auditDocumentLandmarks(rec, "baseline", table(t, sceneWithImage()))

		if len(rec.failures) > 0 {
			t.Errorf("the document-landmarks audit objects to the document as this "+
				"package renders it: %v. An audit that rejects its own product is green "+
				"on its fixtures and red on the thing it was written for", rec.failures)
		}
	})
}

// elementWithAttribute finds the first element whose named attribute has the
// named value, or nil.
//
// Separate from `elementsWith` for the same reason `byTestID` is: every caller
// here is a control fixture, and a fixture that found nothing would report
// "changed nothing", which is a passing test that tested nothing.
func elementWithAttribute(root *html.Node, name, value string) *html.Node {
	var found *html.Node

	walkAll(root, func(node *html.Node) {
		if found != nil || node.Type != html.ElementNode {
			return
		}

		if attributeOr(node, name) == value {
			found = node
		}
	})

	return found
}

// inside reports whether a node sits under an ancestor, the ancestor included.
func inside(ancestor, node *html.Node) bool {
	if ancestor == nil {
		return false
	}

	for current := node; current != nil; current = current.Parent {
		if current == ancestor {
			return true
		}
	}

	return false
}
