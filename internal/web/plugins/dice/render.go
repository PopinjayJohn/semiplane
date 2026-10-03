package dice

// The roller's markup, as `html/template` wrapped into a `templ.Component`.
//
// # Why `html/template` and not a `.templ` file
//
// Two reasons, and the second is the one that matters. **`internal/web/components`
// belongs to phase 5**, so a `.templ` file would have to be added there — outside this
// work item's declared paths — and templ writes `*_templ.go` into the same directory,
// which is a generated file in a diff (AGENTS.md, "Rules that make parallel work
// merge", rule 6). `html/template` reaches the same place with the same contextual
// escaping from a file this work item owns.
//
// The escaping is the whole of the argument and it is not a downgrade: `html/template`
// is context-aware, so `{{.View.Expression}}` inside an attribute and inside text are
// escaped for their own contexts, and a reader who types
// `" autofocus onfocus="alert(1)` into the expression field gets a value. Nothing here
// needs `template.HTML` and nothing here would be allowed it — the page body is the
// one place a "trust me" escape would be catastrophic, and a widget is not exempt from
// a rule because it is a widget.
//
// # Where the class names come from
//
// Every class below is one `shell.css` or `tokens.css` defines, and **never a Tailwind
// utility**. `internal/web/static/css/app.css`'s `@source` globs cover
// `components/**/*.templ`, `httpapi/**/*.go` and `domain/**/*.go` — **not**
// `web/plugins/**` — so a utility class used in this package would never reach the
// built stylesheet, and the page would render unstyled while every other gate passed.
// That is the stylesheet-import failure wearing a different hat, and it is why the
// primitives here are hand-written classes and why this is reported rather than fixed:
// the `@source` line is the integrator's.
//
// `TargetClass` is imported from `internal/web/components/ui` rather than written as a
// literal, which is what §7.3's "enforced by construction" asks for — the constant and
// the markup cannot disagree because there is one spelling and this package reads it.
//
// # The template is parsed per render, not once in a package variable
//
// A package-level `*template.Template` is mutable global state in a package whose
// whole argument is that there is none, and `template.Template` is safe for concurrent
// use only because nothing mutates it after the first `Execute` — which is a promise
// about callers rather than a type-level guarantee. Parsing a ~2 KiB constant per
// render costs microseconds next to a database round trip, and the result is that
// there is nothing to race and nothing for a test to poison.

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/realtime"
	"github.com/semiplane/semiplane/internal/web/components/ui"
)

// widgetMarkup is the roller's centre slot.
//
// **No `<html>`, no `<head>`, no landmark.** The plugin renders the *inside* of the
// route's `<main>`, and the document around it belongs to
// `internal/httpapi/plugins`, which is the package that owns the route and therefore
// the document. A plugin that emitted its own `<html>` would put a second rendered
// document in the product with no §10.2 audit over it — the failure `play`'s `refuse`
// names when it chooses a sentence over a shell document, and the same reasoning from
// the other side.
//
// The `<h1>` is in here rather than in the route's document for a related reason: the
// route has no other content to head, and a route that rendered a title of its own and
// then a plugin that rendered one would produce two. §10.2 requires exactly one.
const widgetMarkup = `<h1 data-testid="page-title">{{.Title}}</h1>
{{- if .View.Outcome.Applied}}
<div class="notice" data-testid="roll-result"
	data-placement="{{.View.Outcome.Applied.Placement}}"
	data-version="{{.View.Outcome.Applied.Version}}"
	data-by="{{.View.Outcome.Applied.By}}">
	<p>The Table recorded this roll on token
		<code>{{.View.Outcome.Applied.Placement}}</code> at version
		<code>{{.View.Outcome.Applied.Version}}</code>.</p>
	<p class="card-meta">The result is on the token, not here: a gameplay system
		decides what a roll means, and this page reads the answer the server stamped.</p>
</div>
{{- else if .View.Outcome.Refused}}
<div class="notice notice--error" data-testid="roll-refused"
	data-reason="{{.View.Outcome.Refused.Reason}}">
	<p>The Table did not record this roll: {{reasonCopy .View.Outcome.Refused.Reason}}</p>
</div>
{{- end}}
{{- if .HasGrammar}}
<p class="card-meta" data-testid="roll-notation">{{.View.Grammar.Summary}}</p>
{{- end}}
<form class="form" method="post" data-testid="roll-form">
	<input type="hidden" name="seq" value="{{.View.Seq}}">
	<div class="field">
		<label for="roll-placement">Token</label>
		<input class="{{.TargetClass}}" id="roll-placement" name="placement" type="text"
			value="{{.View.Placement}}" maxlength="64" autocomplete="off"
			aria-describedby="roll-placement-hint" {{if not .HasPlacement}}required{{end}}>
		<p class="field-hint" id="roll-placement-hint">The token on the Table this roll is
			recorded against.</p>
	</div>
	<div class="field">
		<label for="roll-expression">Expression</label>
		<input class="{{.TargetClass}}" id="roll-expression" name="expr" type="text"
			value="{{.View.Expression}}" maxlength="256" autocomplete="off"
			aria-describedby="roll-expression-hint" required>
		<p class="field-hint" id="roll-expression-hint">{{.View.Grammar.Example}}</p>
	</div>
	<div class="field">
		<label for="roll-reason">What for</label>
		<input class="{{.TargetClass}}" id="roll-reason" name="reason" type="text"
			value="{{.View.Reason}}" maxlength="256" autocomplete="off">
		<p class="field-hint">Shown in the recap so the Table knows whose roll it was.</p>
	</div>
	<button class="button button--primary {{.TargetClass}}" type="submit"
		data-testid="roll-submit">Roll</button>
</form>
`

// widgetData is what the template sees.
//
// A struct rather than the view model directly, for one reason: `Grammar` is
// *conditionally* meaningful. With no payload there is no system behind it, and the
// example the template prints beside the expression would be an empty string
// announced to a reader as documentation. `HasGrammar` makes "there is nothing to say"
// a value the template can branch on, and it is **derived** rather than set by a
// caller so the two cannot disagree — a hand-maintained flag beside the value it
// describes is a second answer waiting to be wrong.
type widgetData struct {
	// Title is the document's only `<h1>`.
	Title string

	// View is the page model, and the zero value when no payload carried one.
	View WidgetView

	// HasGrammar reports whether the campaign declared a system this build can ask.
	HasGrammar bool

	// HasPlacement reports whether a token was named. Drives `required`, so a reader
	// who has not chosen a token is told by the browser rather than by a refusal.
	HasPlacement bool

	// TargetClass is §7.3's class, read from the package that owns the spelling.
	TargetClass string
}

// Widget renders the roller's page.
//
// **Signature fixed by `webplugins.PageType.Render`**, which takes only the payload —
// so the page cannot know its campaign's slug and does not try. The form posts to the
// current URL, which is the standard HTML answer and needs no path knowledge: `POST` to
// the same pattern is the send, which is why `internal/httpapi/plugins` mounts both
// verbs on one pattern rather than inventing a second route.
//
// An **absent, empty or wrong payload still renders**, with an empty grammar and no
// outcome, because a component that renders nothing at all is a nil function called by
// a template engine, which is a panic in somebody's browser tab. §14 requires every
// declared view to render for an empty output, and this is that rule applied to a page
// type rather than to a view.
func Widget(payload rules.Payload) templ.Component {
	view, _ := ReadWidgetView(payload)

	data := newWidgetData(view)

	return templ.ComponentFunc(func(_ context.Context, out io.Writer) error {
		if err := renderWidget(data, out); err != nil {
			return fmt.Errorf("dice: render the roller: %w", err)
		}

		return nil
	})
}

// Render is the whole of the plugin's markup, and returns the bytes rather than a
// component.
//
// Exported because the route package owns the document and has to compose: it needs
// the centre slot's bytes to put inside its own `<main>`, and a `templ.Component` would
// mean rendering into a buffer anyway. One function, and `Widget` is the `templ`
// spelling of it — so there is one template and one data type, and the two callers
// cannot render different things.
//
// Rendering into a buffer rather than straight to the response is load-bearing for the
// route's half: a template that fails after `WriteHeader` has already committed a
// status, and the honest answer for that is a truncated body rather than a 500 nobody
// can send. The route decides whether it can still change the status.
func Render(view WidgetView) ([]byte, error) {
	var buffer bytes.Buffer

	if err := renderWidget(newWidgetData(view), &buffer); err != nil {
		return nil, fmt.Errorf("dice: render the roller: %w", err)
	}

	return buffer.Bytes(), nil
}

// newWidgetData derives the template's data from the page model.
//
// The only function that knows how `WidgetView` becomes `widgetData`, and the reason it
// exists is that `Widget` and `Render` are two spellings of one render: two hand-written
// conversions would be two places for `HasGrammar` to be set in one of them and not the
// other, and the two callers would then disagree about whether the campaign has a system.
func newWidgetData(view WidgetView) widgetData {
	return widgetData{
		Title:        PluginTitle,
		View:         view,
		HasGrammar:   view.Grammar.Valid(),
		HasPlacement: view.Placement != "",
		TargetClass:  ui.TargetClass,
	}
}

// renderWidget executes the template, and is the one place the parse happens.
func renderWidget(data widgetData, out io.Writer) error {
	parsed, err := template.New("dice-roller").Funcs(template.FuncMap{
		"reasonCopy": reasonCopy,
	}).Parse(widgetMarkup)
	if err != nil {
		// Unreachable: the markup is a constant in this file and a syntax error in it
		// is a compile-adjacent fault rather than a request-dependent one. Panicking
		// rather than threading an error through every render is `html/template`'s own
		// `Must` choice, and a panic here is caught by the router's Recoverer and logged
		// as a fault rather than rendered to a reader as an empty page.
		panic("dice: the roller's template does not parse: " + err.Error())
	}

	if err := parsed.Execute(out, data); err != nil {
		return fmt.Errorf("dice: execute the roller: %w", err)
	}

	return nil
}

// AnswerOutcome builds the `Outcome` for a resolution the server accepted.
//
// Returns `(Outcome, true)` when the hub answered with an `applied` frame and
// `(Outcome, false)` when it did not — a caller that gets `false` has a refused roll
// and should read `ReadRefusal`.
//
// The two-argument shape rather than an error because **"the server applied it" and
// "the server refused it" are both answers**, and a refusal is not a failure of this
// function. A caller that treated it as an error would log a refused roll as a fault,
// which is S-12.3's whole complaint about logs in one line.
func AnswerOutcome(resolution realtime.Resolution) (Outcome, bool) {
	answer, err := ReadAnswer(resolution)
	if err != nil {
		return Outcome{}, false
	}

	return Outcome{Applied: &answer}, true
}

// RefusalOutcome builds the `Outcome` for a dispatch the server refused.
//
// `seq` is passed because `ReadRefusal` may not be able to recover one: ADR 0042's
// production resolver reports a refusal as a `plugin.RejectionError` carrying a reason and
// **no frame**, so the `seq` the answer will echo is the one the request carried. See
// `ReadRefusal` for the whole of the two-shape argument.
//
// `(Outcome, false)` when the error was not a refusal at all, which is the difference
// between "the server said no and said why" and "this route is broken" — and the two want
// different answers from the reader and different lines in the log.
func RefusalOutcome(err error, seq realtime.ClientSeq) (Outcome, bool) {
	refusal, isRefusal := ReadRefusal(err, seq)
	if !isRefusal {
		return Outcome{}, false
	}

	return Outcome{Refused: &refusal}, true
}
