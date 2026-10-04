package content_test

// The `[!secret]` callout, through the **whole** pipeline.
//
// # Why this file exists when `internal/content/ext/secret_test.go` does not
//
// The extension's tests stop at goldmark's output. This one goes through the
// sanitiser, and that is a different question with a different failure: the policy
// is an allowlist, so an element nobody named is **silently dropped**, and a callout
// stripped of its `class` is not a visible error — it is a secret callout rendered
// as an ordinary div, indistinguishable in a screenshot from a page whose feature
// simply is not switched on. Every other element in this pipeline has a name in
// `policy.go` for exactly that reason, and this one arrived a phase later.
//
// So the assertions here are about survival, and the second one is about forging:
// the policy now allows `class="secret"` and `data-secret="revealed"` on a `div`,
// and the only reason that is safe is that goldmark drops author raw HTML before
// the sanitiser ever sees it. A test that says so is worth more than the
// reassurance in `policy.go`'s comment.

import (
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
)

// renderCallout runs one source string through the full pipeline.
func renderCallout(t *testing.T, source string) string {
	t.Helper()

	renderer := content.NewRenderer("traitor", nil)

	result, err := renderer.Render(content.Document{Body: source})
	if err != nil {
		t.Fatalf("Render() error = %v, want nil", err)
	}

	return result.HTML
}

func TestTheCalloutSurvivesTheSanitiser(t *testing.T) {
	t.Parallel()

	for name, fixture := range map[string]struct {
		source string
		want   []string
	}{
		"collapsed": {
			source: "> [!secret]-\n> The traitor is Captain Aldric.\n",
			want: []string{
				`<div class="secret secret--collapsed"`,
				`data-ext="secret"`,
				`data-secret="collapsed"`,
				// **The paragraph's opening tag, not the sentence.** Removing the
				// header's soft break is what makes this `<p>The` rather than
				// `<p>\nThe`, and asserting the sentence alone cannot tell those
				// apart -- a mutation that stopped removing the break rendered a
				// callout with a leading newline and left every other assertion
				// in this file green.
				"<p>The traitor is Captain Aldric.</p>",
			},
		},
		"revealed": {
			source: "> [!secret]+\n> He replaced the fire.\n",
			want: []string{
				`<div class="secret secret--revealed"`,
				`data-secret="revealed"`,
				"He replaced the fire.",
			},
		},
		"with a title": {
			source: "> [!secret]+ Revealed to the party  ^traitor\n> The fire.\n",
			want:   []string{`<div class="secret secret--revealed"`, "Revealed to the party"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := renderCallout(t, fixture.source)

			for _, want := range fixture.want {
				if !strings.Contains(got, want) {
					t.Errorf("rendered:\n%s\nwant it to contain %q. The policy is an "+
						"allowlist, so a value it does not name is dropped silently "+
						"and the callout renders unstyled rather than wrong", got, want)
				}
			}

			if strings.Contains(got, "[!secret]") {
				t.Errorf("rendered:\n%s\nthe marker survived to the response", got)
			}
		})
	}
}

// TestAnAuthorCannotForgeACallout is the other half of widening the policy.
//
// `policy.go` now allows `class="secret"` and `data-secret="revealed"` on a `div`,
// which reads like an invitation: an author who could write `<div
// data-secret="revealed">` would be able to style their own prose as a revealed
// callout, and the styling is what tells a reader the block was hidden from them.
//
// It is safe because goldmark **drops raw HTML** — the policy is the second of two
// controls, not the only one, and this test holds the first. It is also worth
// holding, because ADR 0028's rule is that raw HTML in a vault is attacker-reachable
// and a second control silently widening is exactly how a first control stops
// mattering.
func TestAnAuthorCannotForgeACallout(t *testing.T) {
	t.Parallel()

	for name, source := range map[string]string{
		"the div itself": "<div class=\"secret secret--revealed\" " +
			"data-secret=\"revealed\">A lie.</div>\n",
		"on a span":      "<span class=\"secret\" data-secret=\"revealed\">A lie.</span>\n",
		"on an image":    "![x](y.png \"secret\")\n",
		"inside a fence": "```html\n<div class=\"secret secret--revealed\">A lie.</div>\n```\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := renderCallout(t, source)

			if strings.Contains(got, "data-secret") || strings.Contains(got, `class="secret`) {
				t.Errorf("rendered:\n%s\nan author's own markup reached the response "+
					"carrying the callout's class or state", got)
			}
		})
	}
}
