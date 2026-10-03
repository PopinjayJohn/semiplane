package plugins

// The document around a plugin's centre slot.
//
// # Why this package renders a document at all
//
// `webplugins.PageType.Render` returns a `templ.Component`, and ADR 0042 recorded that a
// registered page type "is unreachable until somebody mounts it". This file is the other
// half of that: the `<html>`, the `<head>`, the skip link and the `main` landmark, around
// whatever the plugin rendered.
//
// It is here rather than in `internal/web/components` for one reason, and it is a boundary
// rather than a convenience. **`components.Shell` is phase 5's and composes phase 5's
// chrome** — a banner with the campaign's name, the campaign navigation, a utilities rail
// and a footer. A plugin page has no campaign chrome of its own, and rendering
// `CampaignShell` here would put the campaign's own navigation on a widget page whose whole
// job is one form. So this document is the minimum §10.2 requires and §7.2's own list
// permits where there is no campaign navigation to skip to: a skip link to the one landmark
// there is, and that landmark.
//
// # The skip link and the landmark it names are one condition in two places
//
// AGENTS.md is explicit that both directions are asserted, because "a link with no landmark"
// and "a landmark with no link" are both failures and only one of them is visible in a
// rendering. There is exactly one skip link and exactly one landmark here, so the pairing
// is structural — the template cannot be edited to add a `#nav` link without adding a
// `#nav` landmark — and `TestTheSkipLinkAndTheLandmarkItNamesAreOneCondition` asserts both
// directions over the rendered bytes rather than over the template.
//
// # The landmarks that are absent, and why
//
// There is no `nav` here, so there is no `#nav` skip link — the same pairing
// `components.Shell`'s pre-campaign variant argues. There is no `rail`, and therefore no
// "Skip to utilities" link. And there is no `banner` or `contentinfo`: this document has no
// header and no footer of its own, because the plugin's page is a widget and the chrome
// belongs to the shell that frames it. §10.2 requires `main` unconditionally and the others
// only where the document carries them, which is what
// `assertLandmarksArePresentAndDistinguishing` encodes for this route.
//
// # One render pass, and why the centre is buffered
//
// The centre slot is rendered **into a buffer before the response status is written**, and
// the document is then rendered as one `templ.Component` straight to the writer. The split
// is the order of what can fail:
//
//   - **The centre is plugin code.** A plugin whose `Render` returns an error is a bug in
//     the plugin, and a 500 is the answer a reader and an operator can both act on — which
//     is only available while the status line is uncommitted. So it is rendered first, into
//     a buffer.
//   - **The document is semiplane's own constant.** Executing it can fail only on a
//     programming error in this file, and a failure there after the status is committed is
//     a truncated body plus a log line — which is exactly what `wiki`'s `writeDocument`
//     accepts, for exactly the same reason ("a document that failed halfway has already
//     told the reader more than an error page would have").
//
// So the buffer is spent on the half that can usefully fail, and the half that cannot is
// written in one pass like the wiki route's.

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"strings"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/httpapi/shell"
	"github.com/semiplane/semiplane/internal/web/components/ui"
)

// documentMarkup is the plugin page's document, and the class names reach it as **fields**
// rather than as literals.
//
// That is not a stylistic choice. §7.3's target contract is "enforced by construction" and
// §7.2's skip-link rule targets `.skip-link`, and `ui`'s own comment says the constant
// exists "so a test can assert the contract against the same name the markup uses". A literal
// `target` written here would be a second spelling, and §10.6's audit reads the class out of
// the rendered bytes — so a rename in `ui` would leave this document's markup silently out of
// step with the audit that is supposed to police it. Reading the constant makes the
// divergence a compile error, which is the only kind of drift that cannot ship.
//
// # The inline resolver is the shell's own, imported rather than restated
//
// ADR 0035 records that there is exactly one blocking script and that it resolves
// `data-theme` and `data-ui` before first paint; `internal/httpapi/shell`'s `Resolver()` is
// that script. A copy here would be a second script in a product whose audit says there is
// one, and `TestTheCheckedInScriptIsTheServedScript` would **not** catch it, because a copy
// is not the checked-in file — it would be a *third* place the resolver exists, invisible to
// the drift test that guards the other two.
//
// The colour-scheme meta is written out rather than composed from
// `shell.ColorSchemeMeta()`, and the split is deliberate: **copy what is a constant, import
// what is behaviour.** Seven bytes of markup inside a `text/template` are not worth a templ
// component for one attribute; two kilobytes of JavaScript with a CSP nonce and an
// inertness check are worth exactly one owner. The spelling is the HTML specification's —
// `color-scheme` is a CSS property and an attribute name, and
// `internal/httpapi/shell` holds the same `nolint:misspell` for the same reason.
//
//nolint:misspell // `color-scheme` is a CSS property and an HTML attribute name.
const documentMarkup = `<!DOCTYPE html>
<html lang="en">
	<head>
		<meta charset="utf-8"/>
		<meta name="viewport" content="width=device-width, initial-scale=1"/>
		<meta name="color-scheme" content="light dark"/>
		<title>{{.Title}}</title>
		<link rel="stylesheet" href="/assets/app.css"/>
		{{.Resolver}}
	</head>
	<body>
		<a class="{{.SkipLinkClass}} {{.TargetClass}}" href="#main" data-testid="skip-to-main">Skip to content</a>
		<main id="main" class="shell-main {{.TargetClass}}" role="main" tabindex="-1" data-testid="shell-main">
{{.Centre}}
		</main>
	</body>
</html>
`

// documentData is what the document template sees.
type documentData struct {
	// Title is the document's title element.
	Title string

	// TargetClass is §7.3's minimum-size class, read from `ui`.
	TargetClass string

	// SkipLinkClass is §7.2's off-screen-until-focused class, read from `ui`.
	SkipLinkClass string

	// Centre is the plugin's rendered centre slot, verbatim.
	//
	// **`template.HTML`, and it is the one place this project uses it.** The argument is
	// that the bytes came out of a plugin's own `html/template` — which escaped them for
	// their own contexts — and that this document adds no attribute for them to be
	// interpolated into, so there is no context a second escaping pass could be wrong about.
	// templ's own `Raw`, `SafeScript` and `SafeCSS` helpers make the same escape hatch for
	// the same reason. `html.WithUnsafe()` is a different thing entirely: that one enables
	// *parsing* raw HTML from a vault, and this one accepts bytes semiplane itself produced.
	//
	// The premise rather than the conclusion is what a test can hold, and
	// `TestTheCentreSlotIsEscapedBeforeItReachesTheDocument` holds it: a hostile
	// expression typed into the roller's form is escaped by the plugin's template, so what
	// arrives here is text and not markup.
	Centre template.HTML

	// Resolver is the shell's blocking script.
	Resolver template.HTML
}

// document composes a rendered centre slot into a full document.
//
// `title` is the page type's own title, which `webplugins.ErrNoTitle` refuses at
// registration if it is empty — so the fallback is unreachable, and it is here because a
// document with an empty `<title>` is a browser tab with no name, which is a smaller failure
// than a panic over an invariant two packages away.
func document(ctx context.Context, title string, centre []byte) templ.Component {
	name := strings.TrimSpace(title)
	if name == "" {
		name = "Semiplane"
	}

	data := documentData{
		Title:         name,
		TargetClass:   ui.TargetClass,
		SkipLinkClass: ui.SkipLinkClass,
		Centre:        template.HTML(centre),
		Resolver:      template.HTML(renderResolver(ctx)),
	}

	return templ.ComponentFunc(func(ctx context.Context, out io.Writer) error {
		parsed, err := template.New("plugin-page").Parse(documentMarkup)
		if err != nil {
			// Unreachable: the markup is a constant in this file. Panicking is
			// `html/template`'s own `Must` choice, and the router's Recoverer catches a panic
			// in a handler rather than letting it reach a reader as an empty page.
			panic("plugins: the document template does not parse: " + err.Error())
		}

		if executeErr := parsed.Execute(out, data); executeErr != nil {
			return fmt.Errorf("plugins: render the document: %w", executeErr)
		}

		return nil
	})
}

// renderComponent renders one component to bytes, before the status is committed.
//
// **A nil component is a fault, not an empty page.** `webplugins.ErrNoRender` refuses a nil
// `Render` *function*; a function that *returns* nil is one edit away, and a nil component
// is a panic in whichever template engine rendered it. A 500 carrying a class is the answer
// a reader and an operator can both act on — which is why this returns an error rather than
// an empty byte slice.
//
// The context is the caller's and is not replaced: `templ`'s components read
// `templ.GetNonce` from it, and a `context.Background()` here would silently strip the CSP
// nonce from the resolver that `document` renders.
func renderComponent(ctx context.Context, component templ.Component) ([]byte, error) {
	if component == nil {
		return nil, fmt.Errorf("%w: the page type rendered nothing", ErrNoPageType)
	}

	var buffer bytes.Buffer

	if err := component.Render(ctx, &buffer); err != nil {
		return nil, fmt.Errorf("plugins: render the centre slot: %w", err)
	}

	return buffer.Bytes(), nil
}

// renderResolver renders the shell's blocking script to bytes.
//
// **The caller's context is the one passed in**, and the reason is the nonce:
// `shell.Resolver` reads `templ.GetNonce(ctx)` and omits the attribute entirely when the
// context carries none, because `nonce=""` is not the same as no nonce to a CSP check. A
// copy of that script would have no nonce to read, which is the second reason it is not
// copied.
func renderResolver(ctx context.Context) []byte {
	var buffer bytes.Buffer

	if err := shell.Resolver().Render(ctx, &buffer); err != nil {
		// Unreachable: `shell.Resolver` writes two constants into a `bytes.Buffer`, and the
		// buffer cannot fail. Panicking rather than swallowing it is right because a
		// document that lost its theme resolver renders in the wrong polarity on every
		// page, which is a fault worth stopping on.
		panic("plugins: render the head resolver: " + err.Error())
	}

	return buffer.Bytes()
}
