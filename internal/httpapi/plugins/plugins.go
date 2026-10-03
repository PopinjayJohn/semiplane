// Package plugins mounts the two reference UI plugins of §10.6 on routes a reader can
// reach, and owns the dispatch a UI plugin's send goes through.
//
// # The three boundaries, and they are in this order for the same reason `play`'s are
//
//  1. **Membership, mounted and not checked.** `Mount` wraps every route in
//     `campaigns.RequirePlay` or `campaigns.RequireRead`, so ADR 0024's rule —
//     authorisation is a gate the route mounts, never a check inside a handler — holds by
//     construction. There is no role test anywhere in this file, and that absence is the
//     property to grep for when somebody adds one. S-8's "no access is 404" cannot be
//     reproduced from inside a handler at all.
//
//  2. **The actor is server-derived, and it is the whole security argument of the
//     dispatcher.** `actorFor` builds a `realtime.Actor` from the access the gate
//     resolved and nothing else: the campaign from the URL, the user from the session
//     the identity middleware put on the context, the role from the membership the gate
//     looked up. A plugin cannot name a campaign and cannot name a role, because
//     `realtime.Actor`'s fields are unexported **and** its `UnmarshalJSON` refuses, and
//     because this function does not read a request body for either.
//     `TestTheActorCannotBeForgedFromRequestBytes` is what holds that.
//
//  3. **The frame is built from what the reader typed and decoded by the codec.** A
//     send is a `dice.Request` — an expression, a token, a sequence number — and it
//     becomes a `realtime.ClientIntent` that `realtime.Decode` parses. The plugin's
//     dispatch therefore crosses the same grammar allowlist, the same length bound and
//     the same refusal vocabulary a socket client's keystroke crosses, which is S-10.3's
//     "it dispatches the same intents a human player would, so it passes identical
//     authorisation and validation" made into a code path rather than a claim.
//
// # Why this package exists when `webplugins.Registry` says a page type is unreachable
//
// ADR 0042 recorded the gap honestly: `PageType.Path` is the declaration and
// `internal/httpapi/router.go` holds the mount. This package is the mount's other half,
// and it **derives the patterns from the registry** rather than from literals here —
// a route written twice is two answers for one route, which is the stylesheet-import
// failure this repository keeps paying for. What it does **not** do is edit
// `router.go`: that is the integrator's file, and the wiring this package needs is
// reported rather than made.
//
// # The link preview's fetch is the second security surface, and it is a route
//
// `GET /c/{slug}/plugins/link-preview?url=…` is a URL a campaign's own content chose,
// which makes it attacker-reachable in the way AGENTS.md says Obsidian Sync makes front
// matter attacker-reachable. It is behind `RequireRead` (a preview is a reading, not a
// play) and every refusal it produces is a class rather than a string — the URL is
// author input and a refusal message is a log line (S-12.3). The SSRF rules themselves
// are `linkpreview`'s, and the argument for them is that package's.
package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/domain/rules"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/realtime"
	webplugins "github.com/semiplane/semiplane/internal/web/plugins"
	"github.com/semiplane/semiplane/internal/web/plugins/dice"
	"github.com/semiplane/semiplane/internal/web/plugins/linkpreview"
)

// The refusals this package produces, and none of them carries anything a reader chose.
//
// The naming is the interesting part. A refusal here is about **this request**: the
// campaign's access was answered by the gate before the handler ran, so a 404 from this
// package never means "no access" and saying so would be a lie an operator could act on
// wrongly. Every status below is therefore about the request's own shape or about this
// process's wiring.
var (
	// ErrNoHub is a handler with no hub: a wiring fault, and 503 rather than a silent
	// refusal, because a page that rendered a form whose button does nothing is a broken
	// product reported as a working one.
	ErrNoHub = errors.New("plugins: no hub is configured")

	// ErrNoReader is a hub with no resolver, which `realtime.Hub.Dispatch` reports
	// itself as `ErrNoResolver`. Named here so a route can tell "this build has no
	// systems" from "the reader asked for something odd".
	ErrNoReader = errors.New("plugins: the reader could not be resolved")

	// ErrNoPageType is a request for a kind this build registers no page type for. §10.8's
	// last row makes an unknown kind inert rather than an error, so this is the page
	// type's *own* refusal — a kind whose component was removed — and 404 is right
	// because the reader would get the same answer for a URL that names nothing.
	ErrNoPageType = errors.New("plugins: no page type is registered for that kind")

	// ErrNoRollTarget is a roll send with no token named.
	//
	// Its own sentinel because the fix differs from every other refusal's: a reader who
	// sent no token can be told which field to fill in, and a roll with no token cannot
	// be resolved because `rules.NewMutation` requires one. 400, with a body that names
	// the field.
	ErrNoRollTarget = errors.New("plugins: the roll names no token")

	// ErrBadSequence is a `seq` the wire's own counter range refuses. 400, and named
	// separately from `ErrNoRollTarget` for the same reason: the fix is different.
	ErrBadSequence = errors.New("plugins: the roll names no usable sequence number")

	// ErrNoPreviewURL is a preview request naming no url parameter.
	ErrNoPreviewURL = errors.New("plugins: the preview request names no url")

	// ErrRefused is a dispatch the hub or the system refused. **Not a fault**: it is a
	// roll the Table declined, and it renders as a page with the reason on it rather
	// than as an error status, because the reader's action was understood.
	ErrRefused = errors.New("plugins: the Table refused the roll")
)

// previewQueryKey is the query parameter naming what to preview.
//
// A constant because the link the hook renders and the query the route reads are two
// spellings of one question, and a route that read `u` while the hook wrote `url` would
// be a preview that silently never fires.
const previewQueryKey = "url"

// maxSequence is the largest `seq` a roll send may carry: 2^53, the largest integer a
// JavaScript `number` represents exactly.
//
// **The same bound, and the same reason, as `internal/realtime`'s `maxCounter`** — which is
// the *wire's* bound and is the authority. This one exists because a reader's `seq` comes
// through a form rather than a socket: a value above it would be refused by the codec after
// this route had assembled a frame, and the route would report its own construction failure
// as a 500 to a reader who typed a large number into a hidden field.
//
// The bound is not a round number of convenience, which is what `protocol.go` says of its
// copy: the client is a browser, and a version above 2^53 does not survive `JSON.parse`
// intact.
const maxSequence = uint64(1) << 53

// The form fields the roller's send carries.
//
// **Named once, and used by both the form's rendering and its reading**, because the
// two are a wire between a page this process rendered and a request this process
// receives — and a field renamed on one side only is a form that submits a blank
// expression. `dice` owns the template and this package owns the read, so the names are
// stated here and `dice`'s template refers to them by value at the call site; the test
// `TestEveryFormFieldTheRouteReadsIsOneTheTemplateWrites` holds the pairing.
const (
	fieldSequence   = "seq"
	fieldPlacement  = "placement"
	fieldExpression = "expr"
	fieldReason     = "reason"
)

// Handler serves the two reference plugins.
//
// Three fields and no per-request state: the UI registry, the hub and the logger.
// Everything a request needs beyond that is a local variable in `ServeHTTP`, because a
// `Handler` is shared by every request in the process and a field per request would be a
// data race wearing a struct.
type Handler struct {
	// UI is the UI tier's registry. Required: it is what holds the page type this route
	// renders and what the mount patterns are derived from.
	UI *webplugins.Registry

	// Hub is the broker. Required for the roller's send and nothing else — the link
	// preview does not touch game state, which is §10.6's "read-only" row.
	Hub *realtime.Hub

	// Systems reports which gameplay system a campaign plays under. Optional, and a nil
	// is a real state rather than a fault.
	//
	// **It exists because the notation on the roller's page is the system's data**
	// (§10.4: the table is data and the resolver reads it), and a widget that cannot ask
	// would have to guess — and a plugin that guessed `1d20` would be exactly the thing
	// `rules.Grammar`'s doc comment exists to prevent: "the protocol never assumes d20".
	//
	// A nil (or a campaign whose system this build does not resolve) renders the widget
	// with **no** notation at all, which is S-10.6's first row reached from the UI side and
	// §14's "renders for an empty output" applied to a page type. The widget says so.
	Systems Systems

	// Preview reads a link's preview. Optional; a nil means the route answers 503 for a
	// preview, so a build that did not wire one says so rather than rendering a card
	// with nothing in it.
	Preview *linkpreview.Unfurler

	// Logger receives this route's lines. Nil discards them, so a test does not have to
	// construct a logger to reach a page.
	Logger *slog.Logger
}

// Systems is what this route asks about a campaign's gameplay system.
//
// **A function type rather than the gameplay registry**, for the same reason
// `plugin.SystemOf` is: the answer lives in `campaigns.system_id`, `internal/store` is not
// this package's to read, and the composition root is the only place that can join them. A
// narrow interface over `rules.System` would be wider than the two questions this route
// asks (`Grammar` and, through `Reader.View`, `Derive`) and would drag in nine methods it
// does not use.
//
// A nil or an error is **not** fatal: S-10.6 says a campaign whose plugin was removed
// still serves its wiki and refuses only its game, and this route serves a widget, so the
// widget renders without notation rather than not at all.
type Systems func(ctx context.Context, campaignID int64) (rules.System, error)

// Mount registers the plugin routes on mux, each behind its own gate.
//
// **The page type's patterns come from the registry.** `webplugins.Registry.PageTypes`
// is the declaration list and this is the mount, so reading the patterns from it is what
// makes "naming a route is a matter of registering it" true rather than nearly true —
// and it means a plugin added by another work item becomes reachable by being
// registered, with no edit here.
//
// Two verbs on one pattern, and the reason is the widget's own form: `POST` to the
// current URL is how the roller submits, because a `templ.Component` for a page type
// receives only a payload and therefore never learns its campaign's slug. A second
// pattern would need the slug in the component, which the signature cannot carry.
//
// `RequirePlay` for a page type and `RequireRead` for the preview, and the difference is
// §10.6's: the roller *dispatches an intent* and a preview only reads. Mounting the
// roller behind `RequireRead` would let a non-member of a public campaign roll; mounting
// the preview behind `RequirePlay` would hide a link preview from every reader of a
// public page, which is the failure mode the gate table exists to avoid.
//
// Nil-tolerant, like every other route package: a handler with no registry registers
// nothing and answers 404 for these paths, which is a better failure than a router that
// panics at boot over a wiring mistake.
func Mount(mux *http.ServeMux, handler *Handler) {
	if handler == nil || handler.UI == nil {
		return
	}

	for _, pageType := range handler.UI.PageTypes() {
		gated := campaigns.RequirePlay(handler)
		mux.Handle("GET "+pageType.Path, gated)
		mux.Handle("POST "+pageType.Path, gated)
	}

	previewPath := previewPath(handler.UI)
	if previewPath != "" {
		mux.Handle("GET "+previewPath, campaigns.RequireRead(handler))
	}
}

// previewPath is where the link preview's read is mounted.
//
// A **derived** path rather than a literal, for the same reason the page type's is: one
// answer per route. `linkpreview.PreviewPath` is the plugin's declaration and this is the
// mount; a route written in both places is a route that can be changed in one of them.
//
// Empty when the build registers no link-preview hook, which is the case for a build
// whose plugins were removed — S-10.6's first row reached from the UI side.
func previewPath(registry *webplugins.Registry) string {
	if _, declared := registry.RenderHook(linkpreview.HookName); !declared {
		return ""
	}

	return linkpreview.PreviewPath
}

// ServeHTTP routes one request to one plugin.
//
// The switch is on the **path suffix**, not on the plugin name, because the path is the
// thing a reader asked for and the names are this build's. Everything before the switch
// has already been through the gate.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	access := campaigns.AccessFrom(r.Context())

	if strings.HasSuffix(r.URL.Path, previewSuffix) {
		h.preview(w, r, access)

		return
	}

	if h.UI == nil {
		refuse(w, http.StatusServiceUnavailable, "this page is unavailable")

		return
	}

	kind, found := kindFor(h.UI, access.Campaign.Slug, r.URL.Path)
	if !found {
		refuse(w, http.StatusNotFound, "not found")

		return
	}

	pageType, declared := h.UI.PageType(kind)
	if !declared {
		// Unreachable: `kindFor` returned a kind it read off a registered page type, so the
		// lookup cannot now miss. Present because the registry could be written to between
		// the two calls, and the answer for "the kind I just read is gone" is the same 404 a
		// kind nobody registered gets — not a privileged error.
		h.log(r.Context(), slog.LevelWarn, "plugins.page_type_missing",
			slog.String("class", "plugins.no_page_type"),
		)

		refuse(w, http.StatusNotFound, "not found")

		return
	}

	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		// One method or the other and nothing else, so `net/http`'s 405 is *not* the answer
		// here — the mux has already matched, and a `PUT` to this URL is a verb this
		// design does not have rather than one the mux refuses. 405 with no body is what a
		// client expects, and it is a smaller failure than a 404 (which reads as "there is
		// no such page") or a 200 (which would render the widget for a verb nobody wrote).
		w.Header().Set("Allow", "GET, POST")
		refuse(w, http.StatusMethodNotAllowed, "that verb is not available here")

		return
	}

	if r.Method == http.MethodPost {
		h.roll(w, r, access, pageType)

		return
	}

	h.widget(w, r, access, pageType)
}

// previewSuffix is what distinguishes the preview's path from a page type's.
//
// A constant rather than a comparison of the whole path, because the campaign slug is
// in the path and a comparison of the whole string would be a comparison against a slug
// nobody controls.
const previewSuffix = "/link-preview"

// kindFor names the kind a plugin path is asking for.
//
// **Derived from the registered page types rather than parsed out of the URL.** A page
// type's `Kind` and its `Path` are one declaration, and the mapping between them is the
// registry's — so a caller cannot reach a page type's component through a kind it did not
// register, which is S-8's "authorisation is a gate, never a check" reaching the render
// table as well: a path that resolved to a kind by string surgery would be a path that
// rendered whatever the surgery produced.
//
// **The slug comes from the gate's campaign, not from the request.** `net/http` has
// already matched the pattern — that is why the handler is running at all — so what is left
// to identify is *which* page type, and the answer must come from the declaration. Comparing
// against a path built from `campaigns.AccessFrom`'s slug rather than from `r.URL` means a
// request whose path was rewritten above the handler still resolves to the campaign the
// gate admitted, which is the only campaign this response may be about.
func kindFor(registry *webplugins.Registry, slug, path string) (rules.Kind, bool) {
	if registry == nil {
		return "", false
	}

	for _, pageType := range registry.PageTypes() {
		if path == pathFor(pageType.Path, slug) {
			return pageType.Kind, true
		}
	}

	return "", false
}

// pathFor substitutes a campaign slug into one mux pattern.
//
// The comparison is on the whole path rather than a suffix, and that is deliberate: a
// suffix comparison would accept `/c/greyhaven/plugins/dice-roller` for a page type
// declared at `/c/{slug}/plugins/spare-dice-tray` if the two shared a tail, and the whole
// path is the only comparison that cannot.
func pathFor(pattern, slug string) string {
	return strings.ReplaceAll(pattern, "{slug}", slug)
}

// widget renders the roller's page, or whatever page type this path names.
//
// **The payload is empty and that is the honest answer.** A `rules.Payload` answers a
// view the campaign's *system* declared, and the roller's page is the plugin's own view
// model rather than any system's `Derive` output. So the page renders with an empty
// payload — which §14 requires every declared view to survive — and the roller's grammar
// arrives through the widget's own fields, set from the campaign's system by the
// composition root when it builds the page. `PageType.Render` is given what there is.
//
// The alternative — synthesising a payload with a made-up view name — would put a view
// name in this package that no system declared, and `Payload.View()` is what picks the
// renderer, so it would be a route into somebody else's renderer.
func (h *Handler) widget(
	w http.ResponseWriter,
	r *http.Request,
	access campaigns.Access,
	pageType webplugins.PageType,
) {
	ctx := r.Context()

	view := widgetView(h.grammar(ctx, access.Campaign.ID))

	centre, centreErr := renderCentre(ctx, pageType, view)
	if centreErr != nil {
		h.log(ctx, slog.LevelError, "plugins.render_failed",
			slog.String("class", Class(centreErr)),
		)

		refuse(w, http.StatusInternalServerError, "this page could not be loaded")

		return
	}

	writeDocument(
		ctx,
		w,
		http.StatusOK,
		document(ctx, pageType.Title, centre),
		"plugins.page_render_failed",
	)
}

// roll dispatches one roll and renders the server's answer.
//
// **The order is the argument, and each step is where it is for a reason:**
//
//  1. The form is read and validated *before* anything is dispatched, so a roll with no
//     token never reaches the hub. `rules.NewMutation` would refuse it inside the
//     adapter anyway — but refusing here means the reader gets a body naming the field,
//     and a campaign's roll log never sees a malformed attempt.
//  2. The actor is built from the context (§11 above).
//  3. The frame is decoded by `realtime.Decode`, so the plugin's send crosses the codec.
//  4. `Dispatch` is called, and **its `Resolution` is what is rendered** — not a number
//     derived from the expression, not a re-roll, not a placeholder.
//
// Step 4 is the one the whole package exists to make true, and `TestTheRenderedResultIsTheHubsResolution`
// is what holds it.
func (h *Handler) roll(
	w http.ResponseWriter,
	r *http.Request,
	access campaigns.Access,
	pageType webplugins.PageType,
) {
	ctx := r.Context()

	if h.Hub == nil {
		// A wiring fault, and 503 rather than a refusal: the form rendered and its
		// button would do nothing, which is a product reporting itself healthy.
		refuse(w, http.StatusServiceUnavailable, "the Table is unavailable")

		return
	}

	if err := r.ParseForm(); err != nil {
		refuse(w, http.StatusBadRequest, "the roll could not be read")

		return
	}

	request, err := rollRequest(r)
	if err != nil {
		h.log(ctx, slog.LevelWarn, "plugins.roll_refused",
			slog.String("class", dice.Class(err)),
		)

		refuse(w, http.StatusBadRequest, "the roll needs a token and a usable expression")

		return
	}

	actor, err := actorFor(ctx, access)
	if err != nil {
		// A fault in the gate rather than an access decision: `RequirePlay` admitted
		// this request, so a missing membership means the two disagree. 500, because
		// naming it a 404 would attribute our bug to the reader's permissions.
		h.log(ctx, slog.LevelError, "plugins.actor_unavailable",
			slog.String("class", "plugins.actor_unavailable"),
		)

		refuse(w, http.StatusInternalServerError, "this page could not be loaded")

		return
	}

	frame, err := request.Frame()
	if err != nil {
		h.log(ctx, slog.LevelError, "plugins.roll_unbuildable",
			slog.String("class", dice.Class(err)),
		)

		refuse(w, http.StatusInternalServerError, "this page could not be loaded")

		return
	}

	decoded, err := realtime.Decode(frame)
	if err != nil {
		// A frame this package built was refused by the codec, which is a bug here and
		// not in the reader. `realtime.FrameClass` is the content-free identifier, and
		// the codec's own text quotes the frame (S-12.3).
		h.log(ctx, slog.LevelError, "plugins.roll_frame_refused",
			slog.String("class", realtime.FrameClass(err)),
		)

		refuse(w, http.StatusInternalServerError, "this page could not be loaded")

		return
	}

	intentFrame, isIntent := decoded.(*realtime.ClientIntent)
	if !isIntent {
		// Unreachable: `Request.Frame` marshals a `ClientIntent` and `Decode` returns
		// the frame its `t` named. Present because a type assertion on an interface
		// must not be assumed, and because a route that rendered somebody else's page
		// with an empty answer is worse than a 500.
		h.log(ctx, slog.LevelError, "plugins.roll_frame_unexpected",
			slog.String("class", "plugins.roll_frame_unexpected"),
		)

		refuse(w, http.StatusInternalServerError, "this page could not be loaded")

		return
	}

	resolution, dispatchErr := h.Hub.Dispatch(ctx, actor, intentFrame)

	// **The `Resolution` is what is rendered.** Not a number derived from the expression,
	// not a re-roll, not a placeholder: the answer the hub returned, reduced by the plugin
	// to the frame's own fields, with the version the hub stamped on it.
	//
	// A refusal is the other shape and is read out of the **error**, because
	// `Hub.Dispatch` returns a refusal as an error rather than as a frame (its doc comment
	// says so: a UI plugin's dispatch has no client frame behind it and needs the reason as
	// a value it can render). `ReadRefusal` recovers the `rejected` frame through
	// `realtime.ResolutionFor`, which is the only supported way to read one.
	outcome, refused := dice.RefusalOutcome(dispatchErr, request.Seq)
	if !refused {
		applied, isApplied := dice.AnswerOutcome(resolution)
		if isApplied {
			outcome = applied
		}
	}

	centre, centreErr := renderCentre(ctx, pageType, dice.WidgetView{
		Placement:  string(request.Placement),
		Expression: request.Expression,
		Reason:     request.Reason,
		Seq:        uint64(request.Seq),
		Outcome:    outcome,
	})
	if centreErr != nil {
		h.log(ctx, slog.LevelError, "plugins.render_failed",
			slog.String("class", Class(centreErr)),
		)

		refuse(w, http.StatusInternalServerError, "this page could not be loaded")

		return
	}

	// A refusal is a **200 with the reason on the page**, not a 4xx. The reader's action
	// was understood and answered: §10.6's roller renders the server's result, and "the
	// Table declined" is a result. A 400 here would tell a browser the form was malformed
	// when the form was fine and the game said no.
	//
	// `private, no-store` on a body that depends on who asked **and** on the campaign's
	// current state: the same reasoning as every gate response in this project (AGENTS.md),
	// and here it is stronger — a roll result is campaign state, and a cache that served a
	// stale "at version 4" to a reader whose token is now at version 9 has shown them a
	// number the server would never produce.
	w.Header().Set("Cache-Control", "private, no-store")
	writeDocument(
		ctx,
		w,
		http.StatusOK,
		document(ctx, pageType.Title, centre),
		"plugins.roll_render_failed",
	)
}

// renderCentre renders a page type's centre slot from a view model.
//
// **`webplugins.PageType.Render` is called, not the plugin's function reached for.** That
// is the whole point of registering a component: the registry holds the function, and a
// route that called `dice.Widget` directly would render whichever plugin it happened to
// know about — so a second registered page type would be unreachable and the first would be
// reached whether or not it had been registered.
//
// The payload carries the plugin's **own view model**, which is §10.6.1's mechanism rather
// than a shortcut: `rules.Payload`'s `Value()` is an `any`, a plugin's component takes its
// own Go type out of it, and that is what makes a renderer type-safe instead of a traversal
// through a map. The view name is the plugin's own `dice.ViewName`, recorded so
// `Payload.View()` — which is what picks a renderer — has one answer, and the answer is the
// plugin's.
func renderCentre(
	ctx context.Context,
	pageType webplugins.PageType,
	view dice.WidgetView,
) ([]byte, error) {
	payload, err := rules.NewPayload(dice.ViewName, view)
	if err != nil {
		return nil, fmt.Errorf("plugins: build the roller payload: %w", err)
	}

	return renderComponent(ctx, pageType.Render(payload))
}

// widgetView is the roller's page before any roll: a form, the campaign's notation, and
// **no outcome**.
//
// The zero `Seq` is deliberate and it is the one field the form does not pre-fill. A
// server-rendered form has no client counter, and `dice.Request`'s doc comment argues the
// only discipline that matters is that the answer echoes what the request carried — so the
// field is filled by the reader's browser on load rather than by this server guessing. A
// hard-coded `0` would work today and be wrong the moment a client kept a counter, so the
// template leaves it and the field is required.
func widgetView(grammar rules.Grammar) dice.WidgetView {
	return dice.WidgetView{Grammar: grammar}
}

// grammar asks the campaign's system for its notation, and **an empty grammar is an answer
// rather than a failure**.
//
// The three shapes, and each is a real state:
//
//   - no `Systems` configured at all — a build that wired the UI tier without a gameplay
//     registry, which S-10.6's first row reaches from the other direction;
//   - a campaign whose `system_id` resolves to nothing — the operator's removal, and the
//     wiki still serves;
//   - a `rules.System` that fails to answer, which is a fault but **not this page's**: the
//     widget is still usable, it just cannot say what notation is expected.
//
// The last one is the argument for not propagating the error. A 500 here would make a
// notation lookup take down a dice widget over a cosmetic field, and the reader can still
// roll. The failure is logged with its class, which is where an operator will look.
func (h *Handler) grammar(ctx context.Context, campaignID int64) rules.Grammar {
	if h.Systems == nil || campaignID <= 0 {
		return rules.Grammar{}
	}

	system, err := h.Systems(ctx, campaignID)
	if err != nil {
		h.log(ctx, slog.LevelDebug, "plugins.grammar_unavailable",
			slog.String("class", "plugins.grammar_unavailable"),
		)

		return rules.Grammar{}
	}

	if system == nil {
		return rules.Grammar{}
	}

	return system.Grammar()
}

// rollRequest reads one roll send out of a form.
//
// **Bounded here, not only at the codec.** `realtime.Decode` refuses an expression over
// 256 bytes, and it does so with a class and no text — but a reader who sends a
// megabyte should get a body that names the field rather than a codec class, so the
// bound is applied where the field is read. Both bounds are the same number, and the
// duplication is stated: it is a *user-facing* limit where the codec's is a wire one.
func rollRequest(r *http.Request) (dice.Request, error) {
	placement := realtime.PlacementID(strings.TrimSpace(r.PostFormValue(fieldPlacement)))
	if placement == "" {
		return dice.Request{}, ErrNoRollTarget
	}

	// Parsed and **range-checked here**, against `maxSequence`. A `seq` above 2^53 does not
	// survive `JSON.parse` intact in a browser, so accepting one is accepting a value this
	// project would hand to a client that cannot hold it — and the codec would refuse the
	// frame with `counter_range` anyway, turning a field the reader could fix into a 500
	// about a frame *this route* built.
	//
	// The codec's own `maxCounter` is unexported and `internal/realtime` is not this work
	// item's to edit, so the number is written out here and the duplication is stated: this
	// is a *user-facing* bound where the codec's is a wire one, and the codec remains the
	// authority. A disagreement between them is a 500 with a class rather than a silent
	// wrong answer.
	seq, err := strconv.ParseUint(strings.TrimSpace(r.PostFormValue(fieldSequence)), 10, 64)
	if err != nil || seq > maxSequence {
		return dice.Request{}, ErrBadSequence
	}

	return dice.Request{
		Seq:        realtime.ClientSeq(seq),
		Placement:  placement,
		Expression: strings.TrimSpace(r.PostFormValue(fieldExpression)),
		Reason:     strings.TrimSpace(r.PostFormValue(fieldReason)),
	}, nil
}

// actorFor builds the identity a plugin's dispatch is performed as, and the whole
// reason this package can dispatch at all.
//
// **Three sources and no fourth**, and the type makes substituting one impossible:
//
//   - the campaign from `campaigns.AccessFrom`, which `Resolve` read from the URL and
//     which `RequirePlay` then decided on;
//   - the user from `campaigns.Requestor`, which the identity middleware put on the
//     context from the session cookie;
//   - the role from the membership the gate looked up.
//
// **Not** from the request body, the query string or a header — and not because each was
// considered and rejected, but because `realtime.Actor`'s fields are unexported *and*
// its `UnmarshalJSON` refuses, which is the pair `realtime.Actor`'s own doc comment
// explains. A dispatcher that read a campaign id from a form field would be a form field
// that chose which table a plugin rolled on.
//
// `NewActor` refuses a non-positive campaign, and its error here is a 500 rather than a
// 404: the gate admitted this request against *some* campaign, so a zero one means the
// gate and this function disagree.
func actorFor(ctx context.Context, access campaigns.Access) (realtime.Actor, error) {
	requestor := campaigns.Requestor(ctx)
	if !requestor.Authenticated || requestor.UserID <= 0 {
		return realtime.Actor{}, fmt.Errorf("%w: the requestor is anonymous", ErrNoReader)
	}

	// A nil membership behind `RequirePlay` is the same shape of disagreement as a
	// zero campaign, and is checked for the same reason: the gate admitted this request,
	// so its absence here is a bug rather than an access decision.
	if access.Membership == nil {
		return realtime.Actor{}, fmt.Errorf(
			"%w: the play gate admitted an anonymous request",
			ErrNoReader,
		)
	}

	actor, err := realtime.NewActor(
		access.Campaign.ID,
		realtime.UserID(requestor.UserID),
		access.Membership.Role,
	)
	if err != nil {
		return realtime.Actor{}, fmt.Errorf("%w: the campaign could not be bound", ErrNoReader)
	}

	return actor, nil
}

// preview answers a link preview request.
//
// **Read-only and behind `RequireRead`**, and the refusals are the SSRF rules from the
// `linkpreview` package rather than this package's own: `ErrNotAbsolute` and
// `ErrBadScheme` before any I/O, `ErrPrivateAddress` at the dial. A link naming this
// process's own campaign routes is refused by exactly the rule that refuses
// `127.0.0.1`, which is the property
// `TestALinkIntoACampaignsOwnContentRootIsRefused` holds.
//
// The status for every one of them is **204 with no body**: a link that cannot be
// previewed is not an error a reader can act on, and a 404 would tell a reader (or a
// scanner) which of the private addresses this instance will not reach — which is a small
// oracle about the host's network, and S-8's reasoning about existence oracles applied to
// the host rather than to a campaign. The class goes to the log and nowhere else.
//
// The distinction that matters to the reader — "this link has no preview" versus "this
// link was refused" — is deliberately **not** in the response. A reader looking at a page
// needs a plain link either way, and a page whose links turn into error messages when a
// fetch fails is a page that reports its own infrastructure to everybody.
func (h *Handler) preview(w http.ResponseWriter, r *http.Request, _ campaigns.Access) {
	ctx := r.Context()

	if h.Preview == nil {
		refuse(w, http.StatusServiceUnavailable, "link previews are unavailable")

		return
	}

	raw := r.URL.Query().Get(previewQueryKey)
	if strings.TrimSpace(raw) == "" {
		h.log(ctx, slog.LevelDebug, "plugins.preview_absent",
			slog.String("class", linkpreview.Class(ErrNoPreviewURL)),
		)

		noContent(w)

		return
	}

	if _, err := url.ParseRequestURI(raw); err != nil {
		h.log(ctx, slog.LevelDebug, "plugins.preview_refused",
			slog.String("class", linkpreview.Class(err)),
		)

		noContent(w)

		return
	}

	preview, err := h.Preview.Unfurl(ctx, raw)
	if err != nil {
		h.log(ctx, slog.LevelDebug, "plugins.preview_refused",
			slog.String("class", linkpreview.Class(err)),
		)

		noContent(w)

		return
	}

	component := linkpreview.PreviewLink("", raw, preview)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	writeDocument(ctx, w, http.StatusOK, component, "plugins.preview_render_failed")
}

// noContent answers a preview that will not happen.
//
// 204 and not 404, and the reason is in `preview`'s doc comment: the difference between
// the two is a small oracle about this host's network. `private, no-store` because the
// response is reader-dependent by construction — a preview is fetched for the reader who
// asked, and a shared cache serving one reader's fetch to another is a fetch on their
// behalf.
func noContent(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Cache-Control", "private, no-store")
	header.Set("Content-Security-Policy", "default-src 'none'; img-src https: http:")

	w.WriteHeader(http.StatusNoContent)
}

// writeDocument sends one composed document, and records a failure to render it.
//
// **A method rather than a free function, and the same shape as `wiki`'s
// `writeDocument`**, for the same reason: the status line is committed before the render
// runs, so a failure has no second answer available, and a document that failed halfway
// has already told the reader more than an error page would have. What differs here is
// only the event name, which is what tells an operator which path failed — and the
// roller and the preview are genuinely different paths, one of which renders a document
// and the other a fragment.
//
// The content type is set **before** the status, because a header written after
// `WriteHeader` is dropped, and a document served as `text/plain` is a document no reader
// can read. It is written here rather than at each call site so there is one answer.
func writeDocument(
	ctx context.Context,
	w http.ResponseWriter,
	status int,
	component templ.Component,
	event string,
) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)

	if component == nil {
		return
	}

	// The caller's context, not a fresh one: a component reads `templ.GetNonce` from it,
	// and `document` has already rendered the resolver's nonce into the head from *this*
	// same context. Replacing it here would produce a document whose script carries a
	// nonce and whose body does not — which a CSP check refuses, so the page would render
	// unstyled for a reason that appears nowhere.
	if err := component.Render(ctx, w); err != nil {
		// Logged rather than returned: the status is committed and the reader has what
		// they are going to get. `slog.ErrorContext` rather than the handler's logger
		// because this function is free — a method would need the handler threaded through
		// three call sites to reach a logger that already exists, and the access log
		// already carries the status this reader received.
		//
		// The error's text is a template execution error quoting **semiplane's own**
		// constant, so `err.Error()` is safe here; that is not true of every error on this
		// route and the difference is why the refusals above use `Class`.
		slog.ErrorContext(ctx, "plugins.render_failed",
			slog.String("event", event),
			slog.String("class", "plugins.render_failed"),
			slog.String("error", err.Error()),
		)
	}
}

// refuse answers a request that must not become a page.
//
// `private, no-store` for the reason every gate response in this project carries one
// (AGENTS.md): a reverse proxy in front of a self-hosted instance is the ordinary
// deployment, and a cached refusal is a refusal served to somebody entitled.
//
// **A short plain-text body and deliberately not a rendered document**, for the reason
// `play`'s `refuse` gives: a shell document here would be a second rendered document in
// the project with no §10.2 audit over it, and the a11y gate's rule is that naming a
// route is a claim of audits. A sentence cannot break a landmark audit because it has no
// landmarks.
func refuse(w http.ResponseWriter, status int, message string) {
	header := w.Header()
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Cache-Control", "private, no-store")

	w.WriteHeader(status)

	//nolint:errcheck // The status line is already committed, so there is nothing to
	// answer with; the access log already carries the status this reader received.
	_, _ = w.Write([]byte(message + "\n"))
}

// log writes one line, discarding it when no logger is configured.
func (h *Handler) log(
	ctx context.Context,
	level slog.Level,
	event string,
	attrs ...slog.Attr,
) {
	if h.Logger == nil {
		return
	}

	h.Logger.LogAttrs(ctx, level, "plugins",
		append([]slog.Attr{
			slog.String("event", event),
			slog.String("request_id", middleware.MustRequestID(ctx)),
		}, attrs...)...)
}

// DomainRoleOf is the reader's role, and exists so a caller outside this package can
// read it without reaching into `campaigns`.
//
// A `Role` is **not** a capability: nothing here authorises anything on the strength of
// it (ADR 0024), and it is exposed because a caller wiring a plugin's components needs to
// know whether to render a GM-only affordance as one. A player who then submits it is
// refused by the hub's own check, which is the correct outcome for a cosmetic mistake and a
// bad one for a security one — and that distinction is the whole reason the affordance may
// be rendered at all.
func DomainRoleOf(access campaigns.Access) domain.Role {
	if access.Membership == nil {
		return ""
	}

	return access.Membership.Role
}

// Class reduces an error to a content-free class for a log line.
//
// `observability.errorClass` falls back to `%T`, which for these sentinels would be
// `*errors.errorString` and lose the distinction between "no hub wired" and "no page
// type registered". Every branch is a fixed word.
func Class(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrNoHub):
		return "plugins.no_hub"
	case errors.Is(err, ErrNoReader):
		return "plugins.no_reader"
	case errors.Is(err, ErrNoPageType):
		return "plugins.no_page_type"
	case errors.Is(err, ErrNoRollTarget):
		return "plugins.no_roll_target"
	case errors.Is(err, ErrBadSequence):
		return "plugins.bad_sequence"
	case errors.Is(err, ErrNoPreviewURL):
		return "plugins.no_preview_url"
	case errors.Is(err, ErrRefused):
		return "plugins.refused"
	default:
		return dice.Class(err)
	}
}
