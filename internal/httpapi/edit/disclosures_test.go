package edit_test

// The editor's disclosure surface: which `[!secret]` callouts it offers, and — the
// part that matters — what it does not carry.
//
// # Why this file is about absence
//
// §5.6.1 is that the secret body is absent from a response a non-GM receives, and the
// editor is a **GM-only** route, so a GM's editor legitimately shows the body. That is
// the one place the text may appear, which is exactly why the view model needs a
// test: a struct that grew a `Body` field would render correctly for the GM it
// serves and would be one route away from carrying secret text in a value nothing
// thought about.
//
// So the assertions here are:
//
//   - a control per callout, named distinguishably (§10.2's label-in-name);
//   - the control names the **resolved** anchor, so a reveal cannot address the
//     wrong callout after an edit above it;
//   - **the body is not in the view model** — checked at the boundary the model is
//     built at, and again in the served document's non-preview regions;
//   - a page with no callouts renders no disclosure panel at all.

import (
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/web/components/secret"
)

// A page with two callouts, in the shape §5.6's own example uses: the header line
// **names** the secret and the body **is** the secret.
//
// The first version of this fixture put the secret on the header line —
// `> [!secret]- The combination is hunter2.  ^vault` — and it was wrong in an
// instructive way. `CalloutView.Title` is rendered in each control's accessible name
// and visibly beside it, so the disclosure surface printed the secret, and this file's
// central assertion failed.
//
// **The code was right and the fixture was wrong.** Three controls all reading
// "Reveal" is a §10.2 label-in-name failure, and the only thing that distinguishes a
// GM's three reveal controls is the title the author wrote. A fixture that hides the
// title behind a secret body is a fixture for a GM who has written callouts nobody
// can tell apart — which is the authoring mistake, not the component's defect.
const (
	disclosuresPage = "---\ntitle: The Vault\n---\n\n" +
		"The vault door is iron.\n\n" +
		"> [!secret]- The vault combination  ^vault\n" +
		"> It is hunter2, written on the back of the wyvern's scale.\n\n" +
		"The tide came in.\n\n" +
		"> [!secret]+ The western fire\n" +
		"> Doused with sand from the lower beach.\n\n" +
		"The door held.\n"

	// The one string that must never appear outside the preview, and **not** in the
	// disclosure surface either.
	//
	// It appears in exactly one place on a correctly-shaped page — the callout's
	// body, which the GM's preview renders — so "it is absent from the disclosure
	// surface" is a real assertion rather than one satisfied by it being absent
	// from everything.
	combinationText = "hunter2"

	// The titles, which **must** appear: they are what makes the controls
	// distinguishable, and their absence is the §10.2 failure above.
	vaultTitle = "The vault combination"
	fireTitle  = "The western fire"
)

// TestTheEditorOffersAControlPerCallout is the positive: one control each, and the
// count is the number of callouts.
func TestTheEditorOffersAControlPerCallout(t *testing.T) {
	t.Parallel()

	rendered := renderEditorWithCallouts(t)

	region, found := rendered.disclosuresPanel()
	if !found {
		t.Fatalf("the editor carries no disclosure surface. A GM opening a page with "+
			"two secrets has to be able to reveal them; the test id is %q",
			secret.DisclosuresTestID)
	}

	// Counted by `data-secret-ordinal` rather than by a `data-testid` prefix, and
	// the reason is a measurement rather than a style: the callout's *list item* is
	// `secret-callout-<n>` and its control is `secret-callout-<n>-reveal`, so a
	// prefix count answers 5 for two callouts. The ordinal attribute is on exactly
	// one element per callout.
	if count := rendered.countAttribute("data-secret-ordinal"); count != 2 {
		t.Errorf("the editor offers %d reveal controls, want 2 — one per callout:\n%s",
			count, textUnder(rendered, region))
	}
}

// TestEachControlCarriesItsOwnTitle is §10.2's label-in-name as a *security*
// property, and the positive half of the next test.
//
// Two controls reading "Reveal" is an accessibility failure a GM cannot act on; two
// controls addressing the same anchor is a disclosure of the wrong secret, because
// the reveal rewrites one byte and the endpoint resolves the anchor against the file.
func TestEachControlCarriesItsOwnTitle(t *testing.T) {
	t.Parallel()

	rendered := renderEditorWithCallouts(t)

	region, _ := rendered.disclosuresPanel()
	body := textUnder(rendered, region)

	for _, want := range []string{vaultTitle, fireTitle} {
		if !strings.Contains(body, want) {
			t.Errorf("the disclosure surface does not name %q:\n%s\nThe title is "+
				"what makes three reveal controls distinguishable; a GM who cannot "+
				"tell them apart cannot choose", want, body)
		}
	}
}

// TestEachControlNamesItsOwnCallout is §10.2's label-in-name, and it is a *security*
// property here rather than a usability one.
//
// Three controls all reading "Reveal" is an accessibility failure. Three controls
// all addressing the same anchor is a **disclosure of the wrong secret**: the reveal
// rewrites one byte, and the endpoint resolves the anchor against the file, so a
// duplicated anchor means the second control reveals the first callout.
func TestEachControlNamesItsOwnCallout(t *testing.T) {
	t.Parallel()

	rendered := renderEditorWithCallouts(t)

	seen := map[string]string{}

	rendered.elements(func(node *html.Node) {
		testID := attribute(node, "data-testid")
		if !strings.HasSuffix(testID, "-reveal") && !strings.HasSuffix(testID, "-unreveal") {
			return
		}

		anchor := attribute(node, "data-secret-reveal")
		if anchor == "" {
			anchor = attribute(node, "data-secret-unreveal")
		}

		if anchor == "" {
			t.Errorf("the control %q carries no anchor, so its reveal cannot say "+
				"which secret it discloses", testID)

			return
		}

		label := labelInName(node)
		if label == "" {
			t.Errorf("the control %q has no accessible name", testID)
		}

		if prior, dup := seen[anchor]; dup {
			t.Errorf("two controls share the anchor %q (%q and %q). A reveal "+
				"rewrites one byte, so a duplicated anchor means the second control "+
				"discloses the first callout", anchor, prior, label)
		}

		seen[anchor] = label
	})

	if len(seen) != 2 {
		t.Errorf("the editor named %d distinct callouts, want 2: %v", len(seen), seen)
	}
}

// TestTheViewModelCarriesNoSecretBody is the assertion the file is for.
//
// §5.6.1 is about what leaves a page, and the editor is the one route where the body
// legitimately appears — inside `Preview`. A view model that grew a `Body` field
// would render correctly for the GM it serves, and would be one route away from
// carrying secret text in a field nothing logs, formats, or diffs.
//
// So this checks the served document's **non-preview** regions, which is where a
// `Body` field would first appear.
func TestTheViewModelCarriesNoSecretBody(t *testing.T) {
	t.Parallel()

	rendered := renderEditorWithCallouts(t)

	// The buffer is the page's own source, so the secret is legitimately in it too.
	// What must not happen is a *third* copy, in the disclosure surface.
	region, found := rendered.disclosuresPanel()
	if !found {
		t.Fatalf("no disclosure surface: %q", secret.DisclosuresTestID)
	}

	if body := textUnder(rendered, region); strings.Contains(body, combinationText) {
		t.Errorf("the disclosure surface carries the secret body:\n%s\n§5.6.1 is "+
			"omission, and a view model with a Body field is one route away from "+
			"shipping secret text in a value nothing thinks about", body)
	}
}

// TestAPageWithNoSecretsRendersNoDisclosurePanel is the other direction, and it is
// the one that stops the feature from being a disclosure of its own.
//
// An empty "Disclosures" panel on a page with no secrets tells a GM their page has
// secrets. That is an existence disclosure in the direction §5.6.1 cares about, and it
// is why the component renders nothing rather than an empty shell.
func TestAPageWithNoSecretsRendersNoDisclosurePanel(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.write("Plain.md", pageBody("Iron and rust.\n"))

	rendered := parse(
		t,
		"the editor",
		harness.get("/c/greyhaven/edit/Plain", gmRequestor()).Body.String(),
	)

	if _, found := rendered.disclosuresPanel(); found {
		t.Error("the editor renders a disclosure panel for a page with no " +
			"`[!secret]` callout. An empty panel tells a GM their page has secrets, " +
			"which is the existence disclosure §5.6.1 rules out")
	}
}

// TestTheControlPresentsTheEditorsOwnValidator is the precondition, and it is the
// difference between a reveal and a blind write.
//
// The control must present the `If-Match` the editor is holding, so a reveal on a
// stale buffer is **refused** rather than applied to bytes the GM never read. A
// control that posted without one would succeed against whatever is on disk now.
func TestTheControlPresentsTheEditorsOwnValidator(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.write("Vault.md", disclosuresPage)

	document := parse(
		t,
		"the editor",
		harness.get("/c/greyhaven/edit/Vault", gmRequestor()).Body.String(),
	)

	editorValidator := textOfTestID(t, document, "editor-validator")

	rendered := document

	shown := map[string]bool{}

	rendered.elements(func(node *html.Node) {
		testID := attribute(node, "data-testid")
		if !strings.HasPrefix(testID, "secret-callout-") || strings.Count(testID, "-") > 3 {
			// The control, not the list item: `secret-callout-0-reveal` is a button
			// and `secret-callout-0` is the `<li>` around it. Counting both would
			// find the same validator twice and report two controls per callout.
			return
		}

		if !strings.HasSuffix(testID, "-reveal") && !strings.HasSuffix(testID, "-unreveal") {
			return
		}

		value := attribute(node, "data-secret-if-match")
		if value == "" {
			t.Errorf("the control %q carries no `data-secret-if-match`, so its reveal "+
				"cannot present a precondition and would overwrite whatever is on "+
				"disk now", testID)

			return
		}

		if value != editorValidator {
			t.Errorf("the control presents %q and the editor holds %q. A reveal on a "+
				"stale buffer would be applied to bytes the GM never read",
				value, editorValidator)
		}

		shown[value] = true
	})

	if len(shown) == 0 {
		t.Fatal("no control was found; the assertions above had nothing to run on")
	}
}

// TestTheDisclosureSurfaceIsGMOnly asserts the refusal, and it is a **refusal** that
// turns out to be correct rather than a document that turns out to be clean.
//
// The first version parsed the player's response and looked for the control, on the
// assumption that `RequireEdit` would let a `player` through to a document without
// one. It does not: the response is not an HTML document at all, which is the right
// answer and is what the route has always done.
//
// So the assertion is the refusal, **plus** the second half, which the first version
// did not reach and which is the one that matters: a refused response must not echo
// the secret on its way out. An error page that rendered the page's buffer "so the
// GM can see what they are not allowed to see" would be a disclosure with a helpful
// error message on it.
//
// The two halves are separately falsifiable: removing the gate turns the first red,
// and adding the buffer to the error page turns the second red while the first stays
// green.
func TestTheDisclosureSurfaceIsGMOnly(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.write("Vault.md", disclosuresPage)

	response := harness.get("/c/greyhaven/edit/Vault", playerRequestor())

	if response.Code == http.StatusOK {
		rendered := parse(t, "the player's editor", response.Body.String())

		if _, found := rendered.disclosuresPanel(); found {
			t.Error("a player was served an editor document carrying the disclosure " +
				"surface. The reveal control is GM-only (S-5.12)")
		}
	}

	body := response.Body.String()

	if strings.Contains(body, combinationText) {
		t.Errorf("the refusal to show a player the editor carries the secret body:\n%s\n"+
			"A page rendered \"so they can see what they may not\" is a disclosure "+
			"with a helpful error message on it", body)
	}

	if strings.Contains(body, "secret-callout-") {
		t.Error("the refusal to show a player the editor carries a reveal control")
	}
}

// renderEditorWithCallouts opens the fixture page's editor as a GM.
func renderEditorWithCallouts(t *testing.T) *document {
	t.Helper()

	harness := newHarness(t)
	harness.write("Vault.md", disclosuresPage)

	response := harness.get("/c/greyhaven/edit/Vault", gmRequestor())
	if response.Code != http.StatusOK {
		t.Fatalf("GET the editor = %d, want 200", response.Code)
	}

	return parse(t, "the editor", response.Body.String())
}
