package edit_test

// The editor surface's structural contract, asserted on the parsed document.
//
// UI §10.8's editor row names five automated checks — a `<label>` on the textarea,
// `role="toolbar"` on the toolbar, `aria-live="off"` on the preview, `role="status"`
// on the save state, and the buffer surviving a 412 — and §7.4's positive-tabindex
// prohibition and §10.6's `.target` rule are gate failures in their own right. Each
// is one test here, and each is checked against the tree rather than the markup, for
// the reason AGENTS.md gives about the accessibility gate: a substring test passes on
// a document whose attribute is spelled across an interpolation boundary, and a word
// can sit in a comment where a reader would never hear it but a developer would.
//
// Every one of these assertions was mutation-checked — the line each holds was
// removed, the test was run, and it failed. The evidence is in the PR description
// rather than in a comment here, because a comment claiming a mutation was performed
// is a comment nobody can check.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/edit"
	"github.com/semiplane/semiplane/internal/httpapi/identity"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/web/components/secret"
)

// forbiddenVocabulary is the two words that name entities which do not exist.
//
// Matched case-insensitively against every text node, every comment, every
// attribute *value* and every attribute *name* — the four places the accessibility
// gate's own audit looks, and each inclusion has a reason: a comment is invisible to
// a reader and obvious to a developer, an `aria-label` is announced to the person the
// rule protects, and a `data-world` is invisible and still as retired.
var forbiddenVocabulary = []string{"world", "session"}

// TestTheEditorRendersTheSurfaceIsTheOrdinaryCase: a GM who passed the gate gets
// the editor, in the shell, with the page's own source in the field.
func TestTheEditorRendersTheSurfaceIsTheOrdinaryCase(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	recorder := fixed.get("/c/greyhaven/edit/Vault", gmRequestor())

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body:\n%s", recorder.Code, recorder.Body)
	}

	rendered := parse(t, "the editor", recorder.Body.String())

	for _, testID := range []string{
		"editor",
		"page-title",
		"editor-toolbar",
		"editor-label",
		"editor-textarea",
		"editor-hint",
		"editor-preview",
		"editor-status",
		"editor-save",
		"editor-notice-slot",
		"shell-main",
		"shell-rail",
	} {
		rendered.requireTestID(testID)
	}

	if got := attribute(rendered.byTestID("page-title"), "id"); got != "page-heading" {
		t.Errorf("page-title id = %q, want %q; §7.2's first skip link lands on #main, "+
			"and the wiki route's heading id is the one a GM's bookmark reaches", got, "page-heading")
	}

	// The buffer is the file's own bytes, not a rendering of them. A GM who opens
	// the editor and saves without typing must produce the same file, and a
	// round-tripped (normalised, entity-escaped, front-matter-reordered) buffer
	// would silently rewrite the page on a no-op save.
	area := rendered.requireTestID("editor-textarea")
	if got := text(area); got != pageBody("Iron and rust.\n") {
		t.Errorf("the buffer is not the file's bytes:\n got %q\nwant %q",
			got, pageBody("Iron and rust.\n"))
	}
}

// TestTheTextareaHasALabel is §10.8's first automated check.
//
// A `<label for>` that resolves, and not merely a `for` attribute: a label pointing
// at nothing is worse than no label, because a screen reader announces the field's
// *content* as its name — a GM would hear their own page read back at them as the
// field's label.
func TestTheTextareaHasALabel(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	editor := fixed.get("/c/greyhaven/edit/Vault", gmRequestor())
	rendered := parse(t, "the editor", editor.Body.String())

	area := rendered.requireTestID("editor-textarea")

	target := attribute(area, "id")
	if target == "" {
		t.Fatal("the textarea has no id, so a label cannot name it")
	}

	var label *html.Node

	rendered.elements(func(node *html.Node) {
		if node.Data == "label" && attribute(node, "for") == target {
			label = node
		}
	})

	if label == nil {
		t.Fatalf("no <label for=%q> in the document; §4.8 requires a real label and "+
			"forbids a placeholder-only field", target)
	}

	if got := strings.TrimSpace(text(label)); got != "Page content" {
		t.Errorf("the label reads %q, want %q; §4.8 fixes the wording", got, "Page content")
	}

	// And the description §4.8 requires: the extensions the pipeline understands,
	// described rather than hinted. Checked through `aria-describedby` resolving,
	// because a description that is in the document but not referenced is a
	// description nobody hears.
	description := attribute(area, "aria-describedby")
	if description == "" {
		t.Fatal("the textarea has no aria-describedby; §4.8 requires a description " +
			"listing the extensions")
	}

	var hint *html.Node

	rendered.elements(func(node *html.Node) {
		if node.Data == "p" && attribute(node, "id") == description {
			hint = node
		}
	})

	if hint == nil {
		t.Fatalf("aria-describedby names #%s, which is not in the document", description)
	}

	for _, extension := range []string{"[[wikilink]]", "![[embed]]", "{{statblock}}", "{{dice}}", "[!secret]"} {
		if !strings.Contains(text(hint), extension) {
			t.Errorf("the description does not mention %s; §4.8 lists all five", extension)
		}
	}
}

// TestTheToolbarIsAToolbar is §10.8's second automated check, and the button
// contract that goes with it.
//
// `role="toolbar"` with a name, real `<button>`s, and every one of them carrying
// `.target` — §10.6's audit walks interactive elements and the toolbar is where a
// fork of the editor's own buttons would first lose the class.
//
// The *absence* of `tabindex` on those buttons is asserted too, and it is the
// interesting half: §4.8 asks for a roving `tabindex` and §7.4 plus the gate forbid
// `tabindex` above `-1`, so a roving value cannot appear in served markup at all.
// Asserting "no tabindex" pins the resolution — the buttons are in natural document
// order and phase 9's script sets the roving attribute — so a later change cannot
// reintroduce `tabindex="0"` believing it is implementing §4.8.
func TestTheToolbarIsAToolbar(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	editor := fixed.get("/c/greyhaven/edit/Vault", gmRequestor())
	rendered := parse(t, "the editor", editor.Body.String())

	toolbar := rendered.requireTestID("editor-toolbar")

	if got := attribute(toolbar, "role"); got != "toolbar" {
		t.Fatalf("the toolbar's role = %q, want %q (§4.8)", got, "toolbar")
	}

	if got := attribute(toolbar, "aria-label"); got == "" {
		t.Error("the toolbar has no aria-label; a toolbar with no name is announced " +
			"as nothing in particular")
	}

	buttons := 0

	rendered.elements(func(node *html.Node) {
		if !isFocusable(node) || !isDescendantOf(node, toolbar) {
			return
		}

		buttons++

		if node.Data != "button" {
			t.Errorf("the toolbar holds a %s; §4.8 requires real <button>s so that "+
				"Enter and Space both activate them", node.Data)

			return
		}

		if attribute(node, "aria-label") == "" && strings.TrimSpace(text(node)) == "" {
			t.Errorf("a toolbar control at %s has neither an aria-label nor visible text",
				pathOf(node))
		}

		if hasAttribute(node, "tabindex") {
			t.Errorf("a toolbar control at %s carries tabindex=%q; §7.4 and the gate "+
				"permit only -1, and §4.8's roving tabindex is set by the script, not "+
				"by served markup", pathOf(node), attribute(node, "tabindex"))
		}
	})

	if buttons == 0 {
		t.Fatal("the toolbar holds no controls; §4.8 names six and this build has eight")
	}
}

// TestThePreviewIsNotALiveRegion is §10.8's third automated check.
//
// `aria-live="off"` explicitly rather than absent. The preview re-renders on a timer
// once phase 9's script exists, and a live region that re-renders on a timer is a
// screen-reader storm; an absent attribute would be correct today and would leave the
// rule to a future change that adds a live region "for feedback".
func TestThePreviewIsNotALiveRegion(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	rendered := parse(
		t,
		"the editor",
		fixed.get("/c/greyhaven/edit/Vault", gmRequestor()).Body.String(),
	)

	preview := rendered.requireTestID("editor-preview")

	if got := attribute(preview, "aria-live"); got != "off" {
		t.Errorf("the preview's aria-live = %q, want %q; §4.8 requires it off, because "+
			"a preview re-renders on a timer", got, "off")
	}

	// And the preview is the page, rendered by the publication pipeline rather than
	// by this route. A wikilink showing as an anchor with no destination is the
	// known, documented limitation — the extensions around it must be real, or the
	// preview "would lie" in §4.8's own words.
	body := rendered.requireTestID("editor-preview-body")
	if !strings.Contains(text(body), "Iron and rust") {
		t.Errorf("the preview does not carry the page's prose:\n%s", rendered.source)
	}
}

// TestTheSaveStateIsAPoliteStatus is §10.8's fourth automated check.
//
// `role="status"`, polite, and *one* of them. Two status regions on one screen
// announce everything twice, and the conflict notice is an `alert` — so a second
// status region beside it would make one refusal heard twice, once politely and
// once assertively.
func TestTheSaveStateIsAPoliteStatus(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	rendered := parse(
		t,
		"the editor",
		fixed.get("/c/greyhaven/edit/Vault", gmRequestor()).Body.String(),
	)

	status := rendered.requireTestID("editor-status")

	if got := attribute(status, "role"); got != "status" {
		t.Errorf("the save state's role = %q, want %q (§10.8)", got, "status")
	}

	if got := attribute(status, "aria-live"); got != "polite" {
		t.Errorf("the save state's aria-live = %q, want %q; §7.5 puts a save state "+
			"polite and only a 412 at assertive", got, "polite")
	}

	if got := strings.TrimSpace(text(status)); got != "Idle" {
		t.Errorf("a freshly opened editor says %q, want %q; §4.8's states are Idle, "+
			"Saving, Saved, Conflict, Error", got, "Idle")
	}

	statuses, alerts := 0, 0

	rendered.elements(func(node *html.Node) {
		switch attribute(node, "role") {
		case "status":
			statuses++
		case "alert":
			alerts++
		default:
		}
	})

	if statuses != 1 {
		t.Errorf("the editor carries %d role=status regions, want 1; two would "+
			"announce every save state twice", statuses)
	}

	// A plain `GET` is not a conflict, so the **conflict notice** must not be here.
	//
	// Asserting "zero alerts" was the old form of this claim and it is now both
	// wrong and weaker than what replaces it. §7.5's assertive list is three things —
	// a 412, a reveal, a capped reconciliation — and phase 10 put the second on this
	// page, so the count is 1 and asserting 0 would assert that the feature is
	// absent.
	//
	// **The property is emptiness, not count**, and it is what the count was a proxy
	// for: a live region present at load announces nothing, so an assertive region
	// that ships *empty* is inert, while one that ships with content would speak on
	// every page load. So this asserts both halves separately — one alert, and it is
	// the outcome region, and it is empty — which is a stronger claim than the count
	// it replaces and does not depend on how many assertive surfaces the editor grows.
	if alerts != 1 {
		t.Errorf("the editor carries %d role=alert regions on a plain GET, want 1: "+
			"the reveal outcome region and nothing else. §7.5's assertive list is a "+
			"412, a reveal and a capped reconciliation, and a plain GET is only the "+
			"second", alerts)
	}

	if !rendered.hasTestID(secret.OutcomeRegionTestID) {
		t.Error("the editor carries an assertive region but not the reveal outcome " +
			"region, so the alert that is present is one this test did not expect")
	}

	if body := rendered.textOf(secret.OutcomeRegionTestID); strings.TrimSpace(body) != "" {
		t.Errorf("the reveal outcome region ships with content on a plain GET: %q. "+
			"A live region present at load announces nothing, and that is the "+
			"property; one that arrives already populated speaks on every page load",
			body)
	}

	// And the conflict notice specifically is absent, which is the claim the count
	// used to stand in for.
	if rendered.hasTestID("conflict-notice") {
		t.Error("the editor renders a conflict notice on a plain GET. The 412's " +
			"assertive surface has to arrive with the 412, not be present-but-empty " +
			"on every page")
	}
}

// TestEveryInteractiveElementCarriesTheTargetClass is §10.6's rule, over the whole
// editor document.
//
// §10.6 says the audit must catch plugin output too, and it does so by walking the
// rendered document — which means a component that renders a button without the
// class fails it exactly as the shell's own would. This is the editor's half of
// that, and the class it checks is the same constant `ui.TargetClass` names.
func TestEveryInteractiveElementCarriesTheTargetClass(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	for _, testCase := range []struct {
		where string
		body  string
	}{
		{where: "the editor", body: fixed.get("/c/greyhaven/edit/Vault", gmRequestor()).Body.String()},
		{where: "a 428", body: fixed.put("/c/greyhaven/edit/Vault", gmRequestor(), "", "x").Body.String()},
		{where: "a 404", body: fixed.get("/c/greyhaven/edit/Nowhere", gmRequestor()).Body.String()},
	} {
		t.Run(testCase.where, func(t *testing.T) {
			t.Parallel()

			rendered := parse(t, testCase.where, testCase.body)

			rendered.elements(func(node *html.Node) {
				if !isFocusable(node) {
					return
				}

				if hasClassToken(node, "target") {
					return
				}

				t.Errorf("%s: %s is focusable and carries no .target class; §7.3 "+
					"enforces --target-min by construction and §10.6 audits for it",
					testCase.where, pathOf(node))
			})
		})
	}
}

// TestNoTabindexAboveMinusOne is §7.4's "no positive tabindex, anywhere — gate
// failure", extended to the value the gate itself refuses.
//
// The gate's rule is stricter than §7.4's wording: only `-1` is permitted, because
// `tabindex="0"` moves an element to the front of the tab order while the markup
// still reads in document order, so the two disagree. This test holds the editor to
// the gate's version, and a toolbar with a roving tabindex would fail it — which is
// the point, and is why §4.8's roving value is a script's job.
func TestNoTabindexAboveMinusOne(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	rendered := parse(
		t,
		"the editor",
		fixed.get("/c/greyhaven/edit/Vault", gmRequestor()).Body.String(),
	)

	rendered.elements(func(node *html.Node) {
		raw := attribute(node, "tabindex")
		if raw == "" {
			return
		}

		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			t.Errorf("%s carries tabindex=%q, which is not an integer", pathOf(node), raw)

			return
		}

		if value > -1 {
			t.Errorf("%s carries tabindex=%d; only -1 is permitted (§7.4, §10.2). "+
				"§4.8's roving toolbar tabindex is set by the script, not by markup",
				pathOf(node), value)
		}
	})
}

// TestTheEditorNeverNamesARetiredEntity is UI §1.2 and §10.2's last clause, over
// every string in the document.
//
// Four places are scanned, each for a reason the harness comment gives. The editor is
// where a leftover is most likely: a GM-only screen with copy written by whoever hit
// the failure, and the conflict view's own sentences are the newest words in the
// interface.
func TestTheEditorNeverNamesARetiredEntity(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))
	fixed.write("Other.md", pageBody("Unrelated.\n"))

	editorValidator := fixed.validatorFor("Vault.md")
	stale := fixed.put("/c/greyhaven/edit/Vault", gmRequestor(), editorValidator, "changed\n")
	if stale.Code != http.StatusNoContent {
		t.Fatalf("the seeding save = %d, want 204; body:\n%s", stale.Code, stale.Body)
	}

	documents := []struct {
		where string
		body  string
	}{
		{
			where: "the editor",
			body:  fixed.get("/c/greyhaven/edit/Vault", gmRequestor()).Body.String(),
		},
		{
			where: "a 412",
			body: fixed.put(
				"/c/greyhaven/edit/Vault",
				gmRequestor(),
				editorValidator,
				"x",
			).Body.String(),
		},
		{
			where: "a 428",
			body:  fixed.put("/c/greyhaven/edit/Vault", gmRequestor(), "", "x").Body.String(),
		},
		{where: "a 404", body: fixed.get("/c/greyhaven/edit/Nowhere", gmRequestor()).Body.String()},
		{
			where: "a refusal",
			body: fixed.get(
				"/c/greyhaven/edit/notes/%2e%2e%2fsecret",
				gmRequestor(),
			).Body.String(),
		},
	}

	for _, document := range documents {
		t.Run(document.where, func(t *testing.T) {
			t.Parallel()

			assertNoRetiredEntity(t, document.where, document.body)
		})
	}
}

// assertNoRetiredEntity is the vocabulary scan, over the parsed tree.
func assertNoRetiredEntity(t *testing.T, where, body string) {
	t.Helper()

	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("%s: parse: %v", where, err)
	}

	report := func(place, value string) {
		t.Helper()

		lowered := strings.ToLower(value)

		for _, forbidden := range forbiddenVocabulary {
			if strings.Contains(lowered, forbidden) {
				t.Errorf("%s: %s contains %q; neither entity exists, and §1.2 makes "+
					"the words appear nowhere in the interface (UI §1.2, §10.2)",
					where, place, forbidden)
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

	walk(root)
}

// TestTheEditorCarriesOneH1AndNoSkippedLevels is §7.2's heading contract, which the
// conflict view makes non-trivial: the editor has an `<h1>`, the conflict notice
// adds an `<h2>`, and each hunk adds an `<h3>` under it. A hunk rendered as an `<h2>`
// beside the conflict's `<h2>` would satisfy the count and break the outline.
func TestTheEditorCarriesOneH1AndNoSkippedLevels(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))
	fixed.write("Other.md", pageBody("Unrelated.\n"))

	for _, testCase := range []struct {
		where string
		body  string
	}{
		{where: "the editor", body: fixed.get("/c/greyhaven/edit/Vault", gmRequestor()).Body.String()},
		{
			where: "a 412",
			body:  fixed.put("/c/greyhaven/edit/Vault", gmRequestor(), "W/\"stale\"", "x").Body.String(),
		},
	} {
		t.Run(testCase.where, func(t *testing.T) {
			t.Parallel()

			rendered := parse(t, testCase.where, testCase.body)

			headings := 0
			previous := 0

			rendered.elements(func(node *html.Node) {
				level, ok := headingLevel(node)
				if !ok {
					return
				}

				if level == 1 {
					headings++
				}

				if previous != 0 && level > previous+1 {
					t.Errorf("%s: a heading jumps from h%d to h%d at %s; a skipped level "+
						"is announced as a missing section (§7.2)",
						testCase.where, previous, level, pathOf(node))
				}

				previous = level
			})

			if headings != 1 {
				t.Errorf("%s: the document has %d <h1> elements, want exactly 1 (§7.2)",
					testCase.where, headings)
			}
		})
	}
}

// TestEveryTestHookAppearsOnce is §10.2's well-formedness rule: a hook that appears
// twice selects nothing.
//
// Asserted over the conflict document specifically, because that is where hooks are
// generated — one table and one button pair per hunk — and a generated hook built
// from an index that resets per hunk would collide.
//
// The fixture is a page with two *distant* changes rather than one, because the
// assertion is about per-hunk generation: a single-hunk conflict would satisfy a
// duplicate check with one hook and prove nothing about the second.
func TestEveryTestHookAppearsOnce(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", twoHunkPage("iron and rust", "the tide comes in"))

	body := fixed.put(
		"/c/greyhaven/edit/Vault", gmRequestor(), "W/\"stale\"",
		twoHunkPage("iron and rust and gold", "the tide goes out"),
	).Body.String()
	rendered := parse(t, "a 412", body)

	counts := map[string]int{}

	rendered.elements(func(node *html.Node) {
		if id := attribute(node, "data-testid"); id != "" {
			counts[id]++
		}
	})

	for id, count := range counts {
		if count > 1 {
			t.Errorf("data-testid=%q appears %d times in one document; a hook that "+
				"appears twice selects nothing (§10.2)", id, count)
		}
	}

	for _, hunk := range []string{"hunk-1", "hunk-2"} {
		if counts[hunk] != 1 {
			t.Errorf("data-testid=%q appears %d times, want 1; a two-hunk conflict "+
				"renders one table per hunk", hunk, counts[hunk])
		}
	}

	// And the accept/reject pair per hunk, which is the half of §4.8 that is a
	// per-hunk *decision* rather than a per-hunk rendering.
	for _, hunk := range []string{
		"hunk-1-keep-yours", "hunk-1-keep-theirs",
		"hunk-2-keep-yours", "hunk-2-keep-theirs",
	} {
		if counts[hunk] != 1 {
			t.Errorf("data-testid=%q appears %d times, want 1; §4.8 requires a "+
				"per-hunk accept and reject", hunk, counts[hunk])
		}
	}
}

// twoHunkPage is one page whose first and last lines differ between two buffers and
// whose middle does not.
//
// Built as a fixture rather than written inline because the *distance* is the point:
// the two changed lines are separated by more than the diff's context window, so the
// conflict renders as two hunks rather than one. A fixture whose changes were
// adjacent would pass a per-hunk assertion with one hunk.
func twoHunkPage(first, last string) string {
	middle := []string{
		"", "## The wall", "", "Salt in the stone.", "The mortar is older than the town.",
		"", "### The gate", "", "Oak, banded. The band is newer.",
	}

	lines := append([]string{"---", "title: A page", "---", first}, middle...)
	lines = append(lines, "", last, "")

	return strings.Join(lines, "\n")
}

// TestTheEditorIsMountedBehindTheEditGate is ADR 0024 asserted where it can be: a
// `player`'s request is refused and an anonymous one is challenged, *before* the
// handler runs.
//
// The handler contributes nothing to that — it never asks whether the reader may
// edit — so this test is really a test that the chain refuses, and it would fail the
// day somebody mounted the route outside `RequireEdit`. A route mounted behind
// `RequireRead` instead would pass the anonymous case's status check and fail the
// player's, which is why both are here.
func TestTheEditorIsMountedBehindTheEditGate(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	validator := fixed.validatorFor("Vault.md")

	player := fixed.request(
		http.MethodPut, "/c/greyhaven/edit/Vault", playerRequestor(),
		http.Header{"If-Match": {validator}}, "a player may not write this\n",
	)
	if player.Code != http.StatusForbidden {
		t.Errorf("a player PUT = %d, want 403 (S-14.4, S-6.5); body:\n%s",
			player.Code, player.Body)
	}

	anonymous := fixed.request(
		http.MethodPut, "/c/greyhaven/edit/Vault", anonymousRequestor(),
		http.Header{"If-Match": {validator}}, "neither may an anonymous reader\n",
	)
	if anonymous.Code != http.StatusUnauthorized && anonymous.Code != http.StatusNotFound {
		t.Errorf("an anonymous PUT = %d, want 401 or 404 (S-14.4); body:\n%s",
			anonymous.Code, anonymous.Body)
	}

	// And nothing was written by either attempt. The gate is the only thing that
	// refused them, so this is the half of the authorisation claim that the status
	// codes cannot carry.
	if got := fixed.read("Vault.md"); got != pageBody("Iron and rust.\n") {
		t.Errorf("a refused write changed the file:\n%s", got)
	}
}

// TestAPrivateCampaignIs404ForAMemberOfNoOtherCampaign is the "no access is 404"
// half of ADR 0024, on this route.
//
// The editor is mounted behind `RequireEdit`, and a private campaign is 404 for
// everybody who is not a member — including a *player*, who would otherwise be
// entitled to a 403. That equality is the point: a 403 on a private campaign confirms
// it exists, and the route that mounts the tightest gate is the one where the
// distinction is most tempting to get wrong.
func TestAPrivateCampaignIs404ForAMemberOfNoOtherCampaign(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t).private()
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	for _, testCase := range []struct {
		name      string
		requestor domain.Requestor
	}{
		// A member of *this* campaign is deliberately absent from the table: a
		// member with the player role is entitled to a 403, and the rule being
		// tested is about everybody who is not a member.
		{name: "a signed-in non-member", requestor: strangerRequestor()},
		{name: "an anonymous reader", requestor: anonymousRequestor()},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			recorder := fixed.get("/c/greyhaven/edit/Vault", testCase.requestor)
			if recorder.Code != http.StatusNotFound {
				t.Errorf("a private campaign answered %d for %s, want 404; a 403 would "+
					"confirm the campaign exists", recorder.Code, testCase.name)
			}

			if !strings.Contains(recorder.Body.String(), "not found") {
				t.Errorf("the refusal body is not the same one an unmatched route gives:\n%s",
					recorder.Body)
			}
		})
	}
}

// TestTheEditorMountCarriesTheSlug is the editor's copy of the wiki route's
// `TestTheCampaignMountMustCarryTheSlug`, and it is here rather than in
// `router_test.go` for the reason that test gives: `router.go` is not this work
// item's file.
//
// `campaigns.Resolve` reads the campaign out of `r.PathValue("slug")`, and
// `net/http` sets a path value from the pattern that *matched* — not from the mux
// the handler underneath happens to be. Mounted at `/c/`, no wildcard is named, the
// tier stays `TierNone`, and `RequireEdit` answers 404 for every request under
// `/c/`, including a GM saving a page that exists. Asserted both ways, because a
// test that only asserted the working mount would still pass after somebody
// "simplified" it back to a prefix.
func TestTheEditorMountCarriesTheSlug(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write("Vault.md", pageBody("Iron and rust.\n"))

	campaignMux := http.NewServeMux()
	edit.Mount(campaignMux, fixed.handler())

	backing := campaignStore{
		campaign: domain.Campaign{
			ID: testCampID, Slug: testSlug, Name: "Greyhaven",
			Visibility: domain.VisibilityPublic,
		},
		members: map[int64]domain.Role{gmUser: domain.RoleGM},
	}

	serve := func(outerPattern string) http.Handler {
		outer := http.NewServeMux()
		outer.Handle(outerPattern,
			campaigns.Resolve(backing)(
				middleware.Chain(campaignMux, campaigns.RequireEdit),
			),
		)

		return middleware.RequestID(outer)
	}

	request := func() *http.Request {
		req := httptest.NewRequestWithContext(
			t.Context(), http.MethodGet, "/c/greyhaven/edit/Vault", http.NoBody,
		)

		return req.WithContext(identity.WithRequestor(req.Context(), gmRequestor()))
	}

	withSlug := httptest.NewRecorder()
	serve("/c/{slug}/").ServeHTTP(withSlug, request())
	if withSlug.Code != http.StatusOK {
		t.Errorf("the /c/{slug}/ mount answered %d, want 200; body:\n%s",
			withSlug.Code, withSlug.Body)
	}

	prefixOnly := httptest.NewRecorder()
	serve("/c/").ServeHTTP(prefixOnly, request())
	if prefixOnly.Code != http.StatusNotFound {
		t.Errorf("the /c/ mount answered %d, want 404; it cannot see {slug}, so "+
			"campaigns.Resolve resolves nothing and RequireEdit refuses every request",
			prefixOnly.Code)
	}
}

// isDescendantOf reports whether node is inside ancestor.
func isDescendantOf(node, ancestor *html.Node) bool {
	for current := node; current != nil; current = current.Parent {
		if current == ancestor {
			return true
		}
	}

	return false
}

// headingLevel returns an element's heading level, and whether it is a heading.
func headingLevel(node *html.Node) (int, bool) {
	if len(node.Data) != 2 || node.Data[0] != 'h' {
		return 0, false
	}

	level, err := strconv.Atoi(node.Data[1:])
	if err != nil || level < 1 || level > 6 {
		return 0, false
	}

	return level, true
}
