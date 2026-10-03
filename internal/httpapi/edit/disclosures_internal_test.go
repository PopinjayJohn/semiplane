package edit

// `disclosuresFor`, the mapping from a page's source to the editor's controls.
//
// # An internal test, and the reason is the layer
//
// The marker decision is §4.10.4's per-campaign setting resolved for the editor, and
// it is made in `disclosuresFor` — not in a template and not on the served document.
// Asserting it from `edit_test` would have to go through a route, and the marker is
// not in the served document at all: it belongs to the *preview's* callout chrome,
// which is a templ component over sanitised HTML, and asserting "a marker appears on
// the page" tests the preview rather than the decision.
//
// So the claim is asserted where it is made. That is the honest place for a view-model
// mapping's test, and it is the difference between testing a decision and testing that
// some other component happens to reflect it.

import (
	"net/url"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/web/components/secret"
)

// Two callouts: one collapsed with a block id, one revealed and derived.
const internalDisclosuresPage = "---\ntitle: The Vault\n---\n\n" +
	"> [!secret]- The vault combination  ^vault\n> It is hunter2.\n\n" +
	"> [!secret]+ The western fire\n> Doused with sand.\n"

func TestDisclosuresForShowsTheMarkerOnTheEditor(t *testing.T) {
	t.Parallel()

	handler := &Handler{}
	campaign := domain.Campaign{ID: 7, Slug: "greyhaven"}

	view := handler.disclosuresFor(
		campaign, "lore/Vault.md", internalDisclosuresPage, "a-digest")

	if view.Marker != secret.MarkerShown {
		t.Errorf("Marker = %v, want MarkerShown. §4.10.4 resolves D15 as: the "+
			"editor shows the marker and the published page does not, because the "+
			"editor is where a GM decides whether a public callout should be",
			view.Marker)
	}

	// The endpoint, page and validator are one value each, and they are the three
	// a control needs and nothing more.
	if view.Endpoint != "/c/greyhaven/secrets/lore%2FVault.md" &&
		view.Endpoint != "/c/greyhaven/secrets/lore/Vault.md" {
		t.Errorf("Endpoint = %q, want the reveal route for this page", view.Endpoint)
	}

	if view.Page != "lore/Vault.md" {
		t.Errorf("Page = %q, want the page's path", view.Page)
	}

	if view.Validator == "" {
		t.Error("Validator is empty. A control that presents no precondition would " +
			"overwrite whatever is on disk now rather than refusing a stale buffer")
	}
}

func TestDisclosuresForNamesEachCalloutByItsResolvedAnchor(t *testing.T) {
	t.Parallel()

	handler := &Handler{}
	campaign := domain.Campaign{ID: 7, Slug: "greyhaven"}

	view := handler.disclosuresFor(
		campaign, "lore/Vault.md", internalDisclosuresPage, "a-digest")

	if len(view.Callouts) != 2 {
		t.Fatalf("built %d controls, want 2", len(view.Callouts))
	}

	// The block id is the resolved name for the first, verbatim.
	if got := view.Callouts[0].Anchor; got != "vault" {
		t.Errorf("the first control names %q, want %q — the block id is §5.6.3's "+
			"first resolution and the endpoint matches against exactly this value",
			got, "vault")
	}

	// And the second is derived, so it is twelve hex characters and **not** empty.
	derived := view.Callouts[1]
	if derived.Anchor == "" {
		t.Error("the second control is named the empty string. An empty anchor " +
			"matches every other empty anchor, so two secrets in one campaign would " +
			"share a ledger row")
	}

	if len(derived.Anchor) != 12 {
		t.Errorf("the derived anchor is %d characters, want 12", len(derived.Anchor))
	}

	if derived.State != secret.StateRevealed {
		t.Errorf("the second callout's state is %v, want revealed", derived.State)
	}

	if view.Callouts[0].State != secret.StateHidden {
		t.Errorf("the first callout's state is %v, want hidden", view.Callouts[0].State)
	}
}

func TestDisclosuresForCarriesNoBody(t *testing.T) {
	t.Parallel()

	handler := &Handler{}
	campaign := domain.Campaign{ID: 7, Slug: "greyhaven"}

	view := handler.disclosuresFor(
		campaign, "lore/Vault.md", internalDisclosuresPage, "a-digest")

	// The titles are present; the bodies are not reachable through any field.
	for _, callout := range view.Callouts {
		if callout.Title == "" {
			t.Error("a control has no title, so three of them would all read " +
				"`Reveal` and a GM could not tell them apart (§10.2)")
		}
	}

	// The secret text is reachable only through the titles, and the titles do not
	// contain it — that is the fixture\'s shape, asserted here because it is the
	// assumption the whole "no body" claim rests on.
	for _, callout := range view.Callouts {
		if strings.Contains(callout.Title, "hunter2") {
			t.Errorf("a title carries the secret body %q. A title is author text "+
				"the GM wrote to *name* the callout; a secret put in the header line "+
				"is rendered beside every control", "hunter2")
		}
	}
}

func TestDisclosuresForAPageWithNoCalloutsIsEmpty(t *testing.T) {
	t.Parallel()

	handler := &Handler{}
	campaign := domain.Campaign{ID: 7, Slug: "greyhaven"}

	view := handler.disclosuresFor(
		campaign, "lore/Plain.md", "---\ntitle: Plain\n---\n\nIron and rust.\n", "d")

	if len(view.Callouts) != 0 {
		t.Errorf("built %d controls for a page with no callouts, want 0", len(view.Callouts))
	}
}

// TestRevealEndpointEscapesThePagePath is §S-3.5's rule reached from the other
// direction, and it is an internal test because the router cannot carry the fixture.
//
// The page path reaches `revealEndpoint` from the request line, so it is
// attacker-reachable in the sense that matters: a control's `Endpoint` is a URL the
// browser will POST a disclosure to. A path holding a space, a `?`, a `#` or a `%`
// would otherwise produce an endpoint addressing a different resource or none.
//
// **Escaping `/` is safe here, and that was measured rather than assumed.** The
// obvious worry is that `url.PathEscape("lore/deep/vault.md")` produces
// `lore%2Fdeep%2Fvault.md`, and a `{path...}` wildcard that refused an escaped
// separator would mean every page in a subdirectory had a reveal button pointing at
// nothing. Probed against `net/http` directly: `ServeMux` matches on the escaped path
// and `r.PathValue("path")` returns `lore/deep/vault.md` for **both** the escaped and
// the unescaped form. So the escaped form is not merely tolerated, it is preserved in
// `RawPath` for anything that needs the original.
//
// **Through the router this still cannot be tested end-to-end**, and the reason is the
// fixture rather than the code: a path holding a space or a `?` is not a filename the
// harness wrote, so the editor route would 404 before the control ever rendered. An
// end-to-end test would then be asserting on a 404, which proves nothing about the
// string that gets built. The function is the decision; the function is where the
// assertion belongs.
func TestRevealEndpointEscapesThePagePath(t *testing.T) {
	t.Parallel()

	for name, rel := range map[string]string{
		"a space":        "lore/vault notes.md",
		"a question":     "lore/vault?.md",
		"a hash":         "lore/vault#.md",
		"a percent":      "lore/100%.md",
		"a subdirectory": "lore/deep/vault.md",
		"an apostrophe":  "lore/vault's.md",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			endpoint := revealEndpoint("greyhaven", rel)

			if !strings.HasPrefix(endpoint, "/c/greyhaven/secrets/") {
				t.Fatalf("endpoint %q does not address the reveal route", endpoint)
			}

			// **The raw form must be absent**, and that is the half that matters: an
			// implementation that escaped some segments and not others, or that
			// escaped and then re-decoded, passes a `Contains` on the good form.
			addressed, err := url.PathUnescape(
				strings.TrimPrefix(endpoint, "/c/greyhaven/secrets/"))
			if err != nil {
				t.Fatalf("the endpoint %q is not a valid escaped path: %v", endpoint, err)
			}

			if addressed != rel {
				t.Errorf("the endpoint addresses %q, want %q. It is a URL a browser "+
					"will POST a disclosure to, and its target must be exactly the "+
					"page the GM is editing", addressed, rel)
			}

			// **`%` is deliberately absent from this list**, and that is worth a
			// sentence because it looks like an oversight and is the opposite: `%` is
			// the escape character, so a correctly escaped path is *mostly*
			// percent signs. Asserting their absence would fail every correct
			// implementation. What is forbidden is a percent sign that is not
			// introducing an escape, and the round-trip above already proves the
			// whole segment is escaped — so the list below is about the characters
			// that change a URL's *meaning* rather than its spelling.
			for _, forbidden := range []string{" ", "?", "#"} {
				if strings.Contains(strings.TrimPrefix(endpoint, "/c/greyhaven/secrets/"),
					forbidden,
				) {
					t.Errorf("endpoint %q carries a raw %q. Everything after the "+
						"route prefix is one escaped path segment, so the browser "+
						"resolves it as one filename", endpoint, forbidden)
				}
			}
		})
	}
}
