package linkpreview

// The render hook: what a previewed external link looks like on a page, and what the
// plugin declares to the UI tier.
//
// # It is a hook on every kind, which is what makes `RenderHook.Kind` optional
//
// §10.6's link-preview row is "read-only; the rule data itself comes from a gameplay
// module, never from the UI". An external link appears in prose, in a stat block's
// flavour text and in a journal entry, so the hook declares **no** kind: scoping it to
// one would be scoping it to the one kind where links happen to be common rather than
// the set where they occur.
//
// # It reads a `Preview` from the payload, and the payload is not this package's
//
// `RenderHook.Render` receives `(text string, payload rules.Payload)`. The payload is
// whatever the caller had, and the plugin **type-asserts** rather than reaching into
// it: a `rules.Payload` holds an opaque `any` and S-12.3 says semiplane does not
// interpret payloads. So the hook answers with a preview when it is handed one and
// **degrades to the plain external link** when it is not, which is what makes it safe
// to register before the pipeline's owner wires it (ADR 0042's stated gap).
//
// The degradation is the interesting half. A hook that rendered an empty box when it
// was handed nothing would be a hook that removes a link from a page depending on
// whether the pipeline wired it up — and a page with fewer links depending on a wiring
// decision nobody reading it can see.

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"strings"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/web/components/ui"
	webplugins "github.com/semiplane/semiplane/internal/web/plugins"
)

// The plugin's identity.
//
// **No `PageType` and no `Observer`.** The preview is rendered where a link is — inside
// a page somebody else owns — so a page type would be a route serving a document with
// one link on it, and an observer would be a Datastar fragment patching a live region
// whose content is a paragraph. The render hook is the whole of what this plugin adds,
// which is §10.6's third registration and the right one for this row of its table.
const (
	// PluginName identifies this plugin within the build.
	PluginName = "link-preview"

	// PluginTitle is what a person is shown.
	PluginTitle = "Link preview"

	// HookName is the hook's name within this build, and what `Registry.RenderHook`
	// answers for.
	HookName = "link-preview.external"

	// PreviewPath is where the plugin's own read is mounted.
	//
	// **A pattern, not a path**, and the reason is the same as `dice.PagePath`'s: the
	// campaign access gate reads its `{slug}` wildcard from the pattern that matched, so a
	// pattern without the placeholder would leave every request under `/c/` at `TierNone`
	// and answer 404 for all of them. That is not hypothetical — it was the state of
	// `router.go` until the wiki route landed, and the only test covering it passed, because
	// 404-for-everything is indistinguishable from correct while no campaign route is
	// registered.
	//
	// Declared **here, by the plugin**, and mounted by `internal/httpapi/plugins`. A route
	// written in both places is a route that can be changed in one of them — the
	// stylesheet-import failure, in a URL.
	PreviewPath = "/c/{slug}/plugins/link-preview"
)

// Declaration is what this plugin adds to the interface: one render hook.
//
// **`Emits` is empty and it is not an oversight.** This plugin is read-only, and
// `webplugins.Registry.checkEmits` refuses a declaration whose operations no system
// resolves — so an `Emits` entry here would need a gameplay system to back it, and
// §10.6's link-preview row is explicitly the row that dispatches nothing. The absence
// is the declaration.
func Declaration() webplugins.Declaration {
	return webplugins.Declaration{
		Name:  PluginName,
		Title: PluginTitle,
		RenderHooks: []webplugins.RenderHook{{
			Name:   HookName,
			Render: Hook,
		}},
	}
}

// externalLink reports whether a URL is one this package will preview.
//
// **Two rules and no more.** Absolute, and `http` or `https` — which is
// `checkURL`'s first two checks, and it is called rather than restated so a link that
// is a campaign path, a `file://` URL or a `javascript:` URL is not previewed by this
// plugin *and* is refused by `Unfurl` for the same reason. Two copies of the rule would
// be two answers, and the one that matters is the one at the fetch.
//
// The third rule — the address — is **not** here and could not be: it needs the
// resolved address, and `externalLink` is called while the page is being rendered, one
// or more fetches away from any dial. A link that passes this check and is then refused
// at the dial renders as a plain link, which is the correct outcome for a host this
// process will not reach.
func externalLink(target string) bool {
	if strings.TrimSpace(target) == "" {
		return false
	}

	_, err := checkURL(target)

	return err == nil
}

// hookData is what the hook's template sees.
type hookData struct {
	// Text is the link's own text, as the author wrote it.
	Text string

	// Target is the URL the link points at, verbatim.
	Target string

	// HasPreview reports whether a `Preview` was carried in the payload.
	HasPreview bool

	// Title is the preview's title.
	Title string

	// Description is the preview's description.
	Description string

	// Image is the preview's image, or empty.
	Image string

	// TargetClass is §7.3's class, read from the package that owns the spelling.
	TargetClass string
}

// hookMarkup is what a previewed link renders.
//
// **`rel="noopener noreferrer"` on every external link, previewed or not.** The
// `target` attribute is deliberately absent, so `noopener` is redundant today — and
// `referrerpolicy` is the one that matters for this package: a preview card that
// loaded an image would otherwise send the campaign's page URL as the `Referer`, which
// names a private campaign to a remote host. The link is `target`-less on purpose so a
// reader's browser stays on the page they are reading.
//
// `referrerpolicy` is on the image and **not** on the anchor, because the anchor is not
// fetched until it is followed, and a reader who follows it should still send a
// referrer — this product has no reason to hide a reader's navigation from a site they
// chose to visit. The image, by contrast, is fetched without the reader asking, which is
// the case the policy exists for.
//
// The `aria-labelledby` pair is what gives the image a name: an `<img>` with an empty
// `alt` is decorative and announced as nothing, which is right, and the card's text
// beside it is what a screen reader reads. §10.6's audit walks this markup like any
// other, so the anchor carries `ui.TargetClass` and the image is not a focus stop.
const hookMarkup = `{{- if .HasPreview -}}
<span class="card" data-testid="link-preview"
	data-target="{{.Target}}"
	data-image="{{.Image}}">
	<a class="card-link {{.TargetClass}}" href="{{.Target}}" rel="noopener noreferrer">{{.Title}}</a>
	{{- if .Image}}
	<img src="{{.Image}}" alt="" referrerpolicy="no-referrer" loading="lazy">
	{{- end}}
	<p class="card-meta">{{.Description}}</p>
</span>
{{- else if .Target -}}
<a class="{{.TargetClass}}" href="{{.Target}}" rel="noopener noreferrer"
	data-testid="link-external">{{.Text}}</a>
{{- else -}}
<span data-testid="link-plain">{{.Text}}</span>
{{- end -}}
`

// Hook renders one external link, with a preview when it is handed one.
//
// The degradation is in `hookData`: a payload with no `Preview` in it renders the plain
// link when the text is a URL, and the text alone when it is not. A hook that hid a link
// would make a page's content depend on a fetch that failed.
func Hook(text string, payload rules.Payload) templ.Component {
	return component("render the hook", newHookData(text, "", payload))
}

// PreviewLink renders one external link, with the preview read for it.
//
// **The preview is supplied, not fetched.** `Render` is the hook the registry holds and
// it cannot fail — a `templ.Component` has no error return — so a hook that fetched
// would have to swallow the refusal and render something. Splitting the two is what
// keeps "the hook is total" and "a refusal is visible" both true: `Unfurl` returns an
// error and this function takes an already-read preview, so a caller with no preview
// calls `Hook` and a caller with one calls this.
//
// **The `target` is checked against the preview's own URL**, and the rule is that they must
// both be external for a card to render: a caller that read a preview for one URL and rendered
// it for another would otherwise produce a campaign path wearing a remote site's title, which
// is the confusion the SSRF refusals exist to prevent from being possible.
func PreviewLink(text, target string, preview Preview) templ.Component {
	return component("render the link", newHookData(text, target, previewPayload(preview)))
}

// previewPayload wraps a preview in the payload the hook reads.
//
// **A payload, not a struct field**, and the reason is that the hook's signature is
// `func(string, rules.Payload) templ.Component` — the payload is the only channel
// through which data reaches a render hook at all. `Preview` in a package-level `any`
// would be a global, and this is a value passed per render.
func previewPayload(preview Preview) rules.Payload {
	payload, err := rules.NewPayload("link-preview", preview)
	if err != nil {
		// Unreachable: the view name is a constant and `NewPayload` refuses only the
		// empty one. Returning the zero payload rather than panicking degrades to the
		// plain link, which is what a total hook must do.
		return rules.Payload{}
	}

	return payload
}

// newHookData derives the template's data, and is where the degradation lives.
//
// **Three states, and only the first two have a target to link to.** The hook's signature
// gives it the block's *text* and a payload; a URL is not among them, so a caller that has
// one passes it as `target` and a caller that does not passes the empty string:
//
//   - **A preview in the payload** names its own URL, and that is the anchor's `href` unless
//     the caller named a different one — the preview and the link are one value, so they
//     cannot disagree.
//   - **No preview, and the text is itself an external URL**: the anchor points at the text.
//     This is the state a page with the hook registered and no fetch performed reaches.
//   - **No preview and the text is not a URL**: there is nothing to link to, so the text is
//     rendered as text. An `<a href="">` would be a focus stop that announces text and goes
//     nowhere, which is worse than no link at all — and a hook that *invented* an href would
//     be the one place in this project where a URL comes from nowhere.
//
// The third case is the honest one and it is why `Hook` cannot promise a link: it is handed a
// block's text, and only sometimes is that text a URL.
//
// **A target that is not external ends the same way, whichever entry point named it.** The
// hook renders *external* links — that is what a preview is — so a campaign path, a relative
// link or a `javascript:` URL yields no anchor and no card. That is not a gap: the pipeline
// that will own the call site renders an internal link itself, and this hook's job is the one
// it cannot do.
func newHookData(text, target string, payload rules.Payload) hookData {
	data := hookData{Text: strings.TrimSpace(text), TargetClass: ui.TargetClass}

	preview, isPreview := payload.Value().(Preview)

	// A payload carrying a preview for a URL that is **not** external is not previewed. A
	// caller may have read a preview from one address and attached it to another — and
	// rendering a campaign path with a remote site's title beside it is exactly the confusion
	// the SSRF refusals exist to prevent from being possible.
	if isPreview && externalLink(preview.URL) {
		data.Title = preview.Title
		data.Description = preview.Description
		data.Image = preview.Image
		data.HasPreview = true
		data.Target = preview.URL
	}

	// The caller named a target, and it wins for the anchor's `href`: it is the URL the
	// author wrote, and the preview's URL is a URL the *reader* of that link would rather
	// not be sent to by a redirect hop they did not choose.
	if named := strings.TrimSpace(target); named != "" {
		data.Target = named
	} else if !data.HasPreview && externalLink(data.Text) {
		data.Target = data.Text
	}

	if !externalLink(data.Target) {
		data.Target = ""
		data.HasPreview = false
		data.Title = ""
		data.Description = ""
		data.Image = ""
	}

	return data
}

// component wraps derived data in the `templ.Component` the registry's signature returns.
//
// `Render` and `Hook` share it rather than repeating the closure, so a change to the error
// prefix cannot land on one entry point and not the other.
func component(what string, data hookData) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, out io.Writer) error {
		if err := renderHook(data, out); err != nil {
			return fmt.Errorf("linkpreview: %s: %w", what, err)
		}

		return nil
	})
}

// renderHook executes the hook's template, and is the one place the parse happens.
func renderHook(data hookData, out io.Writer) error {
	parsed, err := template.New("link-preview").Parse(hookMarkup)
	if err != nil {
		// Unreachable: the markup is a constant in this file. Panicking is
		// `html/template`'s own `Must` choice, and a panic in a component is caught by
		// the router's Recoverer rather than rendered to a reader as an empty page.
		panic("linkpreview: the hook's template does not parse: " + err.Error())
	}

	if err := parsed.Execute(out, data); err != nil {
		return fmt.Errorf("linkpreview: execute the hook: %w", err)
	}

	return nil
}

// Render is the hook's markup as bytes, for a caller that composes it into a document
// it owns rather than rendering a component.
//
// Exported for the same reason `dice.Render` is: the route package owns the document,
// and one template with two entry points is cheaper to reason about than two
// templates that could disagree. A nil `preview` is the degraded path and takes the
// same route through the same constructor as `Hook` does.
func Render(text, target string, preview *Preview) ([]byte, error) {
	payload := rules.Payload{}

	if preview != nil {
		payload = previewPayload(*preview)
	}

	var buffer bytes.Buffer

	if err := renderHook(newHookData(text, target, payload), &buffer); err != nil {
		return nil, fmt.Errorf("linkpreview: render the hook: %w", err)
	}

	return buffer.Bytes(), nil
}
