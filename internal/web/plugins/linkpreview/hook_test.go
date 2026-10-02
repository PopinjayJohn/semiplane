package linkpreview_test

// The render hook's own claims: what it renders, what it degrades to, and the vocabulary it
// is allowed to use.
//
// # The hook is the plugin's whole registered surface
//
// §10.6's link-preview row is "read-only; the rule data itself comes from a gameplay module,
// never from the UI", and `Declaration()` returns a `RenderHook` and nothing else — no page
// type, no observer, no emitted operations. So the hook's behaviour *is* the plugin's
// behaviour, and this file is most of what can be said about it without a pipeline.
//
// # Why the degradation is the interesting claim
//
// A hook that rendered an empty box when it was handed no preview would **remove a link from
// a page** depending on whether the pipeline was wired — and a page with fewer links depending
// on a wiring decision nobody reading it can see is a worse failure than a page with a plain
// link. So every un-previewed state must render the plain link, and the tests below are mostly
// about that.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/web/components/ui"
	webplugins "github.com/semiplane/semiplane/internal/web/plugins"
	"github.com/semiplane/semiplane/internal/web/plugins/linkpreview"
)

// aPreview is the fixture, with all three fields set so a card renders in full.
var aPreview = linkpreview.Preview{
	URL:         "https://example.com/notes/the-road-north",
	Title:       "The road north",
	Description: "Iron and rust, and the road north.",
	Image:       "https://example.com/static/road.png",
}

// renderHook renders the registered hook for one payload, which is the shape
// `webplugins.RenderHook.Render` has.
//
// **Through the declaration's own function rather than `linkpreview.Hook` directly**, because
// the declaration is what a build registers and the function it holds is what the pipeline's
// owner calls. A test that reached past it would not notice a declaration pointing at the
// wrong function.
func renderHook(t *testing.T, text string, payload rules.Payload) string {
	t.Helper()

	registry := webplugins.New(nil)
	if err := registry.Register(linkpreview.Declaration()); err != nil {
		t.Fatalf("registering the link preview: %v", err)
	}

	hook, declared := registry.RenderHook(linkpreview.HookName)
	if !declared {
		t.Fatalf("the link preview's hook is not registered under %q", linkpreview.HookName)
	}

	var buffer bytes.Buffer

	component := hook.Render(text, payload)
	if component == nil {
		t.Fatalf("the hook rendered nothing for %q; a component that renders nothing is a "+
			"panic in whichever template engine rendered it", text)
	}

	if err := component.Render(t.Context(), &buffer); err != nil {
		t.Fatalf("rendering the hook: %v", err)
	}

	return buffer.String()
}

// TestTheHookDeclaresOneReadOnlyHook is the registration claim, and it is here rather than in
// the route's tests because the hook is the plugin's whole surface.
//
// **Three absences and one presence**, and the absences are the claim: `Emits` empty (a
// read-only plugin dispatches nothing, and §10.6's row for it says so), `PageTypes` empty (a
// page type would be a route serving a document with one link on it), `Observers` empty (a live
// region whose content is a paragraph).
func TestTheHookDeclaresOneReadOnlyHook(t *testing.T) {
	t.Parallel()

	declared := linkpreview.Declaration()

	if declared.Name != linkpreview.PluginName {
		t.Errorf("the declaration is named %q, want %q", declared.Name, linkpreview.PluginName)
	}

	if declared.Title == "" {
		t.Errorf("the declaration has no title; webplugins.ErrNoTitle refuses one, and a " +
			"blank entry in a status page's list is what it prevents")
	}

	if len(declared.Emits) != 0 {
		t.Errorf("the link preview emits %v; its row in §10.6's table is read-only", declared.Emits)
	}

	if len(declared.PageTypes) != 0 || len(declared.Observers) != 0 {
		t.Errorf("the link preview declares %d page types and %d observers; it renders where "+
			"a link already is, and a route serving one link would be a page whose entire "+
			"content is a preview",
			len(declared.PageTypes), len(declared.Observers))
	}

	if len(declared.RenderHooks) != 1 {
		t.Fatalf("the link preview declares %d render hooks, want 1", len(declared.RenderHooks))
	}

	if hook := declared.RenderHooks[0]; hook.Name != linkpreview.HookName {
		t.Errorf("the hook is named %q, want %q", hook.Name, linkpreview.HookName)
	} else if hook.Kind != "" {
		t.Errorf("the hook is scoped to kind %q; an external link appears in prose, in a stat "+
			"block and in a journal entry", hook.Kind)
	}
}

// TestTheHookRendersThePlainLinkWhenItIsHandedNothing is the degradation claim, and it is the
// test that matters most in this file.
//
// **Four states, and none of them is the page's fault.** The zero payload (the pipeline is
// wired and has no preview), a payload of another plugin's shape (two plugins registered), a
// payload carrying nil, and a preview whose URL is **not external** — the last being the one a
// call site that mixed two URLs up would produce, and the one that would otherwise render a
// campaign path with a remote site's title beside it.
//
// All four must render the plain link. A hook that hid a link would make a page's content
// depend on a fetch that failed.
func TestTheHookRendersThePlainLinkWhenItIsHandedNothing(t *testing.T) {
	t.Parallel()

	foreign, err := rules.NewPayload("some-other-plugin", map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("building the foreign payload: %v", err)
	}

	// A preview whose URL is a **relative path** — a campaign page, not a remote one. The
	// payload carries a plausible title and description, so a hook that checked the wrong
	// thing would render a full card.
	campaignPreview, err := rules.NewPayload("link-preview", linkpreview.Preview{
		URL:         "/c/greyhaven/wiki/Vault",
		Title:       "The vault",
		Description: "Iron and rust.",
	})
	if err != nil {
		t.Fatalf("building the campaign-preview payload: %v", err)
	}

	// The text is **itself the URL**, which is the state `Hook` reaches when the pipeline has
	// the link's target and nothing else — the hook's signature carries no separate `href`.
	const linkText = "https://example.com/notes/the-road-north"
	const target = "https://example.com/notes/the-road-north"

	for _, testCase := range []struct {
		name    string
		payload rules.Payload
	}{
		{name: "the zero payload", payload: rules.Payload{}},
		{name: "a payload carrying another plugin's value", payload: foreign},
		{
			name: "a payload carrying nothing",
			payload: func() rules.Payload {
				payload, payloadErr := rules.NewPayload(linkpreview.HookName, nil)
				if payloadErr != nil {
					t.Fatalf("building the nil-valued payload: %v", payloadErr)
				}

				return payload
			}(),
		},
		{
			name:    "a preview for a URL that is not external",
			payload: campaignPreview,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// The hook is called through the plugin's own `PreviewLink`, which is what a caller
			// with a read preview uses; and with **no** preview at all through `Render`, which
			// is what a caller with nothing uses. Both must produce the plain link.
			for _, rendered := range []string{
				renderHook(t, linkText, testCase.payload),
				renderLink(t, linkText, target, nil),
			} {
				if strings.Contains(rendered, `data-testid="link-preview"`) {
					t.Errorf("the hook rendered a preview card where it should have "+
						"degraded to the plain link: %q", rendered)
				}

				if !strings.Contains(rendered, `data-testid="link-external"`) {
					t.Errorf("the hook did not render the plain link; a page with a link "+
						"whose preview could not be fetched must still have the link: %q",
						rendered)
				}

				if !strings.Contains(rendered, target) {
					t.Errorf("the plain link does not carry its href %q: %q", target, rendered)
				}
			}
		})
	}
}

// renderLink renders the plugin's `PreviewLink` component, which is the path a caller with an
// already-read preview takes.
func renderLink(t *testing.T, text, target string, preview *linkpreview.Preview) string {
	t.Helper()

	var buffer bytes.Buffer

	component := linkpreview.PreviewLink(text, target, previewOrEmpty(preview))
	if component == nil {
		t.Fatalf("PreviewLink rendered nothing for %q", target)
	}

	if err := component.Render(t.Context(), &buffer); err != nil {
		t.Fatalf("rendering the link: %v", err)
	}

	return buffer.String()
}

// previewOrEmpty is the zero preview for a caller that has none — which is how `PreviewLink`
// is exercised in its degraded state, and the reason the zero `Preview` must not produce a
// card.
func previewOrEmpty(preview *linkpreview.Preview) linkpreview.Preview {
	if preview == nil {
		return linkpreview.Preview{}
	}

	return *preview
}

// TestTheHookRendersTheCardFromAPreview is the positive half, and every field is asserted.
//
// **The image is the interesting one.** It is rendered with `alt=""` — decorative — because the
// card's title beside it is what a screen reader reads, and an image with a description in its
// `alt` reads the same sentence twice. `referrerpolicy="no-referrer"` is the security-relevant
// attribute: an image loaded from a remote host would otherwise send the campaign's page URL as
// the `Referer`, which names a private campaign to somebody else.
func TestTheHookRendersTheCardFromAPreview(t *testing.T) {
	t.Parallel()

	rendered := renderLink(t, "the road north", aPreview.URL, &aPreview)

	for _, required := range []string{
		`data-testid="link-preview"`,
		aPreview.Title,
		aPreview.Description,
		`data-target="` + aPreview.URL + `"`,
		`src="` + aPreview.Image + `"`,
		`referrerpolicy="no-referrer"`,
		`rel="noopener noreferrer"`,
		`alt=""`,
	} {
		if !strings.Contains(rendered, required) {
			t.Errorf("the card carries no %s: %q", required, rendered)
		}
	}

	// **And nothing that would navigate this tab away.** `target` is absent deliberately: a
	// preview card that opened a remote page in the reader's tab has taken over their
	// navigation without being asked.
	// **Over the parsed tree and not over the bytes**, because `data-target=` is not the
	// `target` attribute: a substring check would find one where the attribute is absent, which
	// is a test that fails for the wrong reason and gets "fixed" in the markup.
	if anchors := focusStopsOf(mustParse(t, rendered)); len(anchors) > 0 {
		for _, anchor := range anchors {
			if attr(anchor, "target") != "" {
				t.Errorf("the card's anchor carries target=%q; a link that opens a remote "+
					"page in the reader's tab has taken over their navigation",
					attr(anchor, "target"))
			}
		}
	}
}

// TestTheCardCarriesTheTargetClass is §10.6's rule on plugin output, over the preview's own
// markup rather than the route's document.
//
// **This is the rule that a UI plugin decides and nothing else catches.** The route's audits
// read the plugin's markup inside the document, but this is the fragment on its own — which is
// what a Datastar patch would put in the page — and a fragment is exactly what an audit that
// only looks at documents misses. So the class is asserted here, and the anchor is the only
// focus stop the card has.
func TestTheCardCarriesTheTargetClass(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		rendered string
	}{
		{name: "the card", rendered: renderLink(t, "the road north", aPreview.URL, &aPreview)},
		{name: "the plain link", rendered: renderLink(t, "the road north", aPreview.URL, nil)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root, err := html.Parse(strings.NewReader(testCase.rendered))
			if err != nil {
				t.Fatalf("parse the rendered fragment: %v", err)
			}

			anchors := focusStopsOf(root)
			if len(anchors) == 0 {
				t.Fatalf("the fragment has no focus stop: %q", testCase.rendered)
			}

			for _, anchor := range anchors {
				// **Over the class tokens and not over the attribute's text.** `strings.Contains`
				// would pass on `class="untargeted"`, so a rename of the styling classes around
				// it could leave this green with the minimum target size no longer applied — which
				// is the one thing the class is for.
				if !slices.Contains(strings.Fields(attr(anchor, "class")), ui.TargetClass) {
					t.Errorf("the anchor carries class=%q, want it to carry .%s; §7.3 "+
						"enforces the --target-min minimum by construction and §10.6 audits "+
						"for it **including plugin output**", attr(anchor, "class"), ui.TargetClass)
				}
			}
		})
	}
}

// TestTheImageIsNotAFocusStop is the other half of §10.6's element list, and it is a rule
// about what the audit must *not* demand.
//
// An `<img>` is not focusable, so it needs no `tabindex` and no target class — and an audit
// that treated it as a focus stop would demand both, which is a rule no correct card could
// satisfy. The test states the negative so that a future change adding `tabindex` to the image
// fails here rather than being justified later.
func TestTheImageIsNotAFocusStop(t *testing.T) {
	t.Parallel()

	root, err := html.Parse(strings.NewReader(renderLink(t, "x", aPreview.URL, &aPreview)))
	if err != nil {
		t.Fatalf("parse the rendered fragment: %v", err)
	}

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "img" {
			if tabindex := attr(node, "tabindex"); tabindex != "" {
				t.Errorf("the preview's image carries tabindex=%q; an image is not focusable "+
					"and adding it puts a stop in the tab order that goes nowhere", tabindex)
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)
}

// TestTheHooksMarkupNamesNoRetiredEntity is UI §1.2 over the plugin's own fragment, in both
// states.
//
// **Over the parsed tree**, for the reason AGENTS.md states: a substring check passes while
// the word sits in a comment or a `data-` attribute, and the card's copy is exactly where a
// retired noun would be typed.
func TestTheHooksMarkupNamesNoRetiredEntity(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		rendered string
	}{
		{name: "the card", rendered: renderLink(t, "x", aPreview.URL, &aPreview)},
		{name: "the plain link", rendered: renderLink(t, "x", aPreview.URL, nil)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			root, err := html.Parse(strings.NewReader(testCase.rendered))
			if err != nil {
				t.Fatalf("parse the rendered fragment: %v", err)
			}

			assertNoRetiredEntity(t, testCase.name, root)
		})
	}
}

// assertNoRetiredEntity requires neither retired word anywhere in a parsed fragment — text
// nodes, comments, attribute values and attribute names.
func assertNoRetiredEntity(t *testing.T, where string, root *html.Node) {
	t.Helper()

	report := func(kind, text string) {
		lowered := strings.ToLower(text)

		for _, retired := range []string{"world", "session"} {
			if strings.Contains(lowered, retired) {
				t.Errorf("%s: %s contains %q; neither entity exists, and §1.2 makes the words "+
					"appear nowhere in the interface", where, kind, retired)
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

	walk(root)
}

// TestTheHookIsTotalOverEveryPayload is §14's "renders for an empty output" applied to a
// render hook, over four payload shapes.
//
// The four are: the zero payload, a payload of another plugin's shape, a payload carrying nil,
// and a payload carrying the preview's **zero value** — which is the case the degraded path
// turns on, and the one a nil-pointer would be hiding behind.
//
// **Totality is "the reader's text survives", not "there is an anchor"**, and the two rows are
// both needed to say it. Text that is a URL must reach the reader as a link, because that is
// the state the pipeline is in when it has the link's target and no preview. Text that is not a
// URL must reach the reader as text, because there is nothing to point an anchor at — and an
// `<a href="">` is a focus stop that announces a sentence and goes nowhere, which is worse than
// the plain span because it *looks* like a link.
func TestTheHookIsTotalOverEveryPayload(t *testing.T) {
	t.Parallel()

	zeroValued, err := rules.NewPayload("link-preview", linkpreview.Preview{})
	if err != nil {
		t.Fatalf("building the zero-valued payload: %v", err)
	}

	foreign, err := rules.NewPayload("some-other-plugin", []int{1, 2, 3})
	if err != nil {
		t.Fatalf("building the foreign payload: %v", err)
	}

	for _, testCase := range []struct {
		name    string
		text    string
		payload rules.Payload
		want    string
	}{
		{
			name: "a url in the text",
			text: "https://example.com/notes/the-road-north",
			want: `data-testid="link-external"`,
		},
		{
			name: "text that is not a url",
			text: "the road north",
			want: `data-testid="link-plain"`,
		},
	} {
		for _, payload := range []rules.Payload{
			{},
			foreign,
			zeroValued,
		} {
			rendered := renderHook(t, testCase.text, payload)

			if !strings.Contains(rendered, testCase.want) {
				t.Errorf("the %s with the payload %q rendered %q, want it to render %s; a hook "+
					"that renders nothing is a panic in whichever template engine rendered it",
					testCase.name, payload.View(), rendered, testCase.want)
			}

			if !strings.Contains(rendered, testCase.text) {
				t.Errorf("the %s with the payload %q dropped the reader's text: %q",
					testCase.name, payload.View(), rendered)
			}

			if strings.Contains(rendered, `data-testid="link-preview"`) {
				t.Errorf("the %s with the payload %q rendered a preview card from a payload "+
					"carrying no readable preview: %q", testCase.name, payload.View(), rendered)
			}
		}
	}
}

// TestTheHookDoesNotEchoAnUntrustedTargetAsMarkup is the escaping claim, and the fixture is
// the shape a substring scan would not notice.
//
// **A URL is a place a reader's input reaches the document**, and a hook renders it into an
// `href` and a `data-target`. The payload is a quote that closes the attribute, a focus handler
// and a tag — and the assertion is over the parsed tree, because a byte check would pass on a
// document where the escaping happened for one attribute and not another.
func TestTheHookDoesNotEchoAnUntrustedTargetAsMarkup(t *testing.T) {
	t.Parallel()

	const hostile = `https://example.com/" autofocus onfocus="alert(1)"><script>alert(2)</script>`

	rendered := renderLink(t, hostile, hostile, &aPreview)

	root, err := html.Parse(strings.NewReader(rendered))
	if err != nil {
		t.Fatalf("parse the rendered fragment: %v", err)
	}

	if scripts := elementsNamed(root, "script"); len(scripts) != 0 {
		t.Errorf("the fragment carries %d <script> elements: %q", len(scripts), rendered)
	}

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			for _, attribute := range node.Attr {
				if strings.HasPrefix(strings.ToLower(attribute.Key), "on") {
					t.Errorf("%s carries %s=%q; the hook did not escape its values, so a "+
						"reader who follows a hostile link could run script",
						node.Data, attribute.Key, truncate(attribute.Val))
				}
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	// And the positive half: the value arrived and was rendered, escaped — otherwise the three
	// assertions above would be satisfied by a template that rendered nothing.
	if !strings.Contains(rendered, "onfocus") {
		t.Errorf("the hostile URL does not appear at all: %q; the field discarded its input, "+
			"which would make the escaping assertions vacuous", rendered)
	}
}

// TestAPreviewForOneURLIsNeverRenderedBesideAnother is the mixed-up-call-site case, and it is
// the last of the degradation states.
//
// **A caller that read a preview for one URL and rendered it for another** is a plausible
// mistake — a loop over links with the preview in a variable — and the result would be a
// campaign path carrying a remote site's title, which is the exact confusion the SSRF refusals
// exist to prevent from being possible. So a preview is rendered only when the **target** it
// would sit beside is external too, and the two rows differ in which half is wrong: one leaves
// a plain link to the target, the other leaves a plain link to the *preview's* address because
// the target is not this hook's to render.
func TestAPreviewForOneURLIsNeverRenderedBesideAnother(t *testing.T) {
	t.Parallel()

	const campaignPath = "/c/greyhaven/wiki/Vault"

	// Both halves of the mistake, because either could be the bug: a preview for a remote URL
	// rendered against a campaign path, and a preview for a campaign path rendered against a
	// remote URL.
	for _, testCase := range []struct {
		name   string
		text   string
		target string
		shown  linkpreview.Preview
		want   string
	}{
		{
			name:   "a remote preview beside a campaign path",
			text:   "the vault",
			target: campaignPath,
			shown:  aPreview,
			want:   `data-testid="link-plain"`,
		},
		{
			name:   "a campaign preview beside a remote URL",
			text:   "the road north",
			target: aPreview.URL,
			shown: linkpreview.Preview{
				URL:   campaignPath,
				Title: "The vault",
			},
			want: `data-testid="link-external"`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			rendered := renderLink(t, testCase.text, testCase.target, &testCase.shown)

			if strings.Contains(rendered, `data-testid="link-preview"`) {
				t.Errorf("the hook previewed a link whose target and preview name different "+
					"addresses; a campaign path carrying a remote site's title is the confusion "+
					"the SSRF refusals exist to prevent. Rendered: %q", rendered)
			}

			if !strings.Contains(rendered, testCase.want) {
				t.Errorf("the hook did not degrade to %s: %q", testCase.want, rendered)
			}

			if !strings.Contains(rendered, testCase.text) {
				t.Errorf("the degraded render dropped the reader's text %q: %q",
					testCase.text, rendered)
			}
		})
	}
}

// TestTheDeclaredPathIsAPatternTheGatewayAccepts is the mount claim, and it is here because
// the path is the plugin's declaration.
//
// **`net/http` panics on an ambiguous pattern** at registration, so a plugin whose path named
// a wildcard badly would take the process down at boot rather than at a request — which makes
// this worth checking before the composition root ever calls `Mount`.
func TestTheDeclaredPathIsAPatternTheGatewayAccepts(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.Handle("GET "+linkpreview.PreviewPath, http.NotFoundHandler())

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		strings.Replace(linkpreview.PreviewPath, "{slug}", "greyhaven", 1), http.NoBody))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("a GET through the declared pattern answered %d, want 404 from the fixture "+
			"handler; the pattern registered nothing no request could reach", recorder.Code)
	}
}

// --- The helpers -------------------------------------------------------------

// focusStopsOf returns the fragment's anchors, which are its only focus stops.
func focusStopsOf(root *html.Node) []*html.Node {
	anchors := make([]*html.Node, 0)

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "a" {
			anchors = append(anchors, node)
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return anchors
}

// elementsNamed returns every element with the given tag.
func elementsNamed(root *html.Node, tag string) []*html.Node {
	found := make([]*html.Node, 0)

	var walk func(*html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == tag {
			found = append(found, node)
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return found
}

// mustParse parses a rendered fragment, aborting the test when it is not HTML.
func mustParse(t *testing.T, fragment string) *html.Node {
	t.Helper()

	root, err := html.Parse(strings.NewReader(fragment))
	if err != nil {
		t.Fatalf("parse the rendered fragment: %v", err)
	}

	return root
}

// attr returns an element's attribute value.
func attr(node *html.Node, name string) string {
	for _, attribute := range node.Attr {
		if attribute.Key == name {
			return attribute.Val
		}
	}

	return ""
}
