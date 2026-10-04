// Package edit serves a campaign's editor: `GET` then `PUT` with `If-Match`, and
// the 412 conflict that the second can answer.
//
// One route, and it is the first place in the product where a *write* meets a
// human. Everything it does is therefore one of four things, and the order they
// happen in is the design:
//
//   - **Confinement, before anything else.** The page path arrives in a URL, and
//     `Root.At` is the boundary (S-3.5). It is the first operation after the
//     campaign's root is resolved, so a path that leaves the campaign is refused
//     before a body is read, before a validator is computed and before anything
//     can be written.
//   - **The precondition, before the body.** S-6.2: a write carries `If-Match`,
//     and a mismatch answers 412 having recorded nothing. A missing header is a
//     428 and not a 200, because a request with no precondition is an
//     unconditional overwrite wearing a save's clothes — see
//     `writePreconditionRequired`.
//   - **The write, in the record's order.** S-6.4 and architecture §6.1: append
//     the `page_revisions` row, then write through `Target.WriteFile`, which
//     stages a temp file in the same directory, `fsync`s it, renames it over the
//     target and `fsync`s the directory. The atomic write is *not* reimplemented
//     here — `content` owns it, and a second implementation would be a second set
//     of answers to "is this save atomic".
//   - **No forced render, no forced re-index.** S-6.4 closes with "the watcher's
//     own event re-renders and re-indexes; the save path forces neither", so this
//     package never touches the render cache, the page index, or the event
//     stream. It moves a file and returns a validator, which is the whole
//     contract. Forcing a re-index here would make the editor a second pipeline
//     whose failure mode is an index that disagrees with the vault in the window
//     between the save and the watcher's event.
//
// # Authorisation is not here
//
// The route is mounted behind `campaigns.RequireEdit` (S-6.5, ADR 0024) and this
// handler never asks whether the GM may edit: it reads the resolved campaign and
// the resolved account, and a handler that also consulted the tier would be a
// second copy of the S-8 matrix — the copy nobody reviews, and the one that
// eventually answers 403 where the gate answered 404, turning a private
// campaign's existence into a fact a stranger can learn one status code at a
// time. A slug naming nothing never reaches this package at all; the gate has
// already answered 404.
//
// # Every status this route can return
//
// Written out because the shape of a surface is a contract, and a reader
// integrating a client should not have to read the branches to learn it:
//
//	GET   200  the editor, with the current validator
//	PUT   204  matched: the file is written, a revision is appended, the new
//	            validator is on the response
//	PUT   412  mismatched: the current content and its hash are in the body, and
//	            nothing was written and nothing recorded
//	PUT   428  no `If-Match` at all
//	GET/PUT 400 a path that named no location
//	GET/PUT 403 a path that leaves the campaign's root
//	GET/PUT 404 a page that is not there
//	PUT   413 a body over the document cap
//	GET/PUT 500 the write failed
//
// 401, 403 and 404 also come from the gate the route is mounted behind, and they
// are the gate's to answer: a `player`'s `PUT` is 403 and an anonymous one is 401
// (S-14.4), and a private campaign is 404 to everybody who is not a member.
//
// # The 412 is not an error page
//
// UI §4.7's `412` row and §4.8's last two rows agree: the conflict diff goes in
// the editor's preview slot, the buffer stays in the textarea, and the status
// region goes assertive. So the 412 response is the *editor*, with the diff where
// the preview was — which is why `writeConflict` renders a document rather than a
// body, and why the buffer it renders is the request's own text.
package edit

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/observability"
	"github.com/semiplane/semiplane/internal/web/components"
	editor "github.com/semiplane/semiplane/internal/web/components/edit"
)

// pageExtension is the suffix a page's path gains on its way in from a URL.
//
// A URL carries no extension — `content.WikiHref` writes
// `/c/{slug}/wiki/Some%20Page` — and the extension is put back here, once, so
// that the link a GM reads aloud and the link a browser follows are the same
// string. The same constant and the same reasoning as the wiki route's; the two
// spellings of a page must be the same page, and a test asserts the editor's
// validator and the wiki route's `ETag` agree for the same bytes, which is what
// would fail first if they did not.
const pageExtension = ".md"

// pageFileMode is the mode a saved page is written with.
//
// 0o600, and not 0o644: the campaign's content root is created 0o700 (see
// `campaigns.Registrar`) because a directory any account on the host can read is
// a campaign any account on the host can read. A world-readable file inside it
// would be consistent with that root only by accident, and the mode is stated
// here rather than inherited from whatever created the file first, because a page
// that arrived from a sync client may be 0o644 and a save would then preserve it.
const pageFileMode = 0o600

// The response headers this route writes.
const (
	contentTypeHeader  = "Content-Type"
	cacheControlHeader = "Cache-Control"
	varyHeader         = "Vary"
	// cookieVary is the `Vary` value, and it is the same one the wiki route writes
	// for the same reason: the *document* is reader-dependent even when the body is
	// not, because the shell carries the reader's name and a sign-out form. A
	// shared cache keyed only on the URL would hand one GM another's header.
	cookieVary = "Cookie"
	// noStore is the `Cache-Control` on every response that carries page text or a
	// GM-salted validator. S-5.4 asks for it on `include_secrets=true` responses
	// and every response here is one: the editor renders with secrets included
	// (§4.8's "the preview is rendered with include_secrets=true") and its
	// validator is salted with `true`.
	noStore = "no-store"
	// htmlContentType is what a rendered document is served as. The shell is a
	// complete document, not a fragment, so this is the document type rather than
	// the type of a partial.
	htmlContentType = "text/html; charset=utf-8"
)

// refusedHeading is what a page whose path was refused calls itself.
//
// A fixed string, and never the requested path, for the reason `content.Root`
// keeps the offending path out of its own errors: an error whose text varies with
// the caller's input is a channel, and a heading is the largest text on the page.
// "Page" is the word the interface uses for the thing (UI §1.2), so a GM is told
// what could not be opened without being told what was asked for.
const refusedHeading = "Page"

// titleSeparator joins the parts of a document title, in UI §7.2's form
// "Page — Section — Campaign".
//
// Spelled here rather than shared, for the same reason `wiki/handler.go` spells
// it: the two routes each compose a title for their own heading, and a shared
// constant would be a package neither of them owns.
const titleSeparator = " — "

// RootLookup returns a campaign's confined content root.
//
// `*content.Registry` satisfies it as it stands. Narrower than the registry on
// purpose: a handler that can enumerate every campaign's root is a handler that
// could be handed a slug it was never authorised for, and the only slug it is ever
// given is the one the access gate resolved.
type RootLookup interface {
	Get(slug string) (*content.Root, error)
}

// Renderer renders one campaign's pages.
//
// `*content.Renderer` satisfies it. An interface for the same reason the wiki
// route has one: a test can hand the editor a renderer that records what it was
// given, which is the only way to assert that the preview came from the
// publication pipeline rather than from a string the route built.
type Renderer interface {
	Render(doc content.Document) (content.Rendered, error)
}

// Renderers returns the renderer for a campaign's slug.
//
// A lookup rather than a field because the renderer is per campaign: it holds the
// campaign's slug for its log lines and its own kind registry, and a process with
// one renderer for every campaign would report every render under one name.
type Renderers interface {
	Renderer(slug string) (Renderer, error)
}

// CampaignRenderers is a Renderers over renderers the composition root built, one
// per campaign.
//
// The same type `wiki` declares, over the same values, and it is a second
// declaration rather than a shared one on purpose: a route does not import a
// sibling route, and a shared type would have to live in a package neither of them
// owns. The composition root builds it twice, or — better — extracts it when a
// third route needs it. What matters is that both hold the *same* renderers, and
// `TestTheEditorAndTheWikiRouteAgreeOnTheValidator` holds them to that through the
// one property a reader can observe.
type CampaignRenderers map[string]*content.Renderer

// Renderer returns the renderer for slug, or an error naming the slug when there
// is none.
//
// The error is an operator's problem rather than a GM's: a campaign row with no
// renderer is a wiring fault in the composition root, and answering it with an
// empty preview would hide that fault behind a working-looking editor.
func (renderers CampaignRenderers) Renderer(slug string) (Renderer, error) {
	renderer, present := renderers[slug]
	if !present {
		return nil, fmt.Errorf("edit: no renderer for campaign %s", slug)
	}

	return renderer, nil
}

// Handler serves the editor route.
//
// Exported fields rather than a constructor with eight parameters, matching
// `wiki.Handler` and `accounts.Router`: the composition root writes one literal
// and a test writes another, and a struct literal names what it sets. Every field
// is written once before the server starts and read on every request, so a
// Handler is safe for concurrent use — which is the same claim `content.Root`
// makes and rests on the same thing: no field is written after construction.
type Handler struct {
	// Roots is how a campaign's content root is found, and the confinement
	// boundary every path this route touches goes through. Required.
	Roots RootLookup

	// Revisions is where a successful save is recorded (S-6.4). Required, and
	// required to be *absent from the refusal path*: a 412 that appended a row
	// would be a revision of a page nobody published, and the phase's §14 row is
	// specifically that it does not.
	Revisions Revisions

	// Renderers is how a campaign's renderer is found, for the preview. Required:
	// a route with no renderer has nothing to preview with, and answering that
	// with an empty pane would hide a wiring fault behind a working-looking
	// editor.
	Renderers Renderers

	// Kinds is the page-kind registry `content.Parse` discriminates `kind`
	// against, and may be nil, in which case every page is prose.
	Kinds domain.PageKindRegistry

	// Redactor removes content a viewer may not see. Required. This phase installs
	// `content.NoSecrets()`, which removes nothing and is replaced by P10; the
	// editor still calls it, and with `include_secrets=true`, so that the ordering
	// S-5.7 requires is in place before the redactor starts removing things.
	Redactor content.Redactor

	// Logger receives this route's lines. Nil is allowed and discards them, so a
	// test does not have to construct a logger to serve a request.
	Logger *slog.Logger

	// Instance names the running instance and reports what is unhealthy, for the
	// shell's chrome and for the failure states' rail.
	Instance components.InstanceView

	// SignOutHref is where the header's sign-out form posts. Empty renders no
	// form, which is the right answer on an instance with no such route.
	SignOutHref string
}

// Mount registers the editor route on mux, behind the edit gate.
//
// **The gate is mounted here and not left to the caller.** ADR 0024 says
// authorisation is a gate a route mounts and never a check inside a handler, and the
// strongest form of that is for the route to mount its own: a caller that had to
// remember `RequireEdit` would eventually register the route without it, and the
// failure mode of that is a `player`'s `PUT` writing a campaign's pages — which is
// the one thing S-6.5 says cannot happen and the one thing no other test in the
// project would notice.
//
// Layering is harmless and intentional. The router also wraps the whole campaign mux
// in `RequireRead`, so a request passes that first and this second: a member with the
// player role clears `RequireRead` and is refused 403 by this, and an anonymous
// reader of a public campaign is challenged 401 (S-14.4). Both answers come from
// `campaigns.Guard`, which is the only place the S-8 matrix is written down.
//
// Two patterns rather than one handler switching on the method, because `net/http`
// gives the 405 for a method a path does not take: a `POST` to an editor URL is a
// method that exists nowhere in this design, and answering it 405 with an `Allow`
// header is a more honest answer than a branch that guesses.
//
// mux is the campaign-scoped mux — the one `mountCampaignRoutes` fills — and the
// pattern carries the `/c/` prefix even though that mux is itself mounted there.
// A Go 1.22 mux routes on a prefix and then hands the *whole* path to what it
// matched, so a pattern written without the prefix would never fire.
func Mount(mux *http.ServeMux, handler *Handler) {
	gated := campaigns.RequireEdit(handler)

	mux.Handle("GET /c/{slug}/edit/{path...}", gated)
	mux.Handle("PUT /c/{slug}/edit/{path...}", gated)
}

// ServeHTTP answers one editor request.
//
// Linear, and every branch leaves through one of the four writers at the bottom.
// The order is the one the package comment states, and the comments on the calls
// are that order — a step that moves is a step whose comment no longer matches it.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)
	slug := r.PathValue("slug")

	// The campaign's root first, then the path inside it. Nothing about the
	// request's *body* is read before both have been settled, so a request that
	// names a path outside the campaign costs two operations rather than a full
	// body upload.
	root, err := h.Roots.Get(slug)
	if err != nil {
		h.writeFailure(w, r, fmt.Errorf("content root for %s: %w", slug, err))

		return
	}

	target, err := pageTarget(root, r.PathValue("path"))
	if err != nil {
		h.writeFailure(w, r, err)

		return
	}

	if r.Method == http.MethodPut {
		h.save(w, r, access, target)

		return
	}

	h.edit(w, r, access, target)
}

// edit answers the `GET`: the editor, with the current validator.
//
// Three steps in a fixed order — read, derive, render — and the render is last
// because it is the only expensive one and the only one that can fail for a reason
// that is not about the page. A read failure is a 404 the GM needs immediately; a
// render failure is a 500 they need to report, and rendering first would produce
// that 500 for a page that does not exist.
//
// **No 304, and that is a decision rather than an omission.** A conditional `GET`
// would normally revalidate against `If-None-Match`, and this route deliberately
// does not: a 304 tells the client "the copy you hold is current", and the entire
// reason a GM opens the editor is to find out whether the copy they hold is
// current. Answering 304 for a page Obsidian has changed since the GM's last save
// would make the editor the one route in the product that lies about the disk.
// `Cache-Control: private, no-store` says the same thing to an intermediary, and
// the external-change notice (`internal/httpapi/events`) is how a GM learns about a
// change *without* reloading.
func (h *Handler) edit(
	w http.ResponseWriter,
	r *http.Request,
	access campaigns.Access,
	target *content.Target,
) {
	ctx := r.Context()

	src, err := h.readSource(ctx, target)
	if err != nil {
		h.writeFailure(w, r, err)

		return
	}

	preview, err := h.preview(r.PathValue("slug"), src)
	if err != nil {
		h.writeFailure(w, r, err)

		return
	}

	page := pageName(target.Path())

	h.writeDocument(w, r, http.StatusOK, editor.EditorView{
		Shell:       h.shellFor(r, page),
		Page:        page,
		Buffer:      src.body,
		Validator:   validator(access.Campaign.ID, target.Path(), src.hash),
		ContentHash: src.hash,
		Preview:     preview,
		State:       editor.SaveIdle,
		Secrets:     h.disclosuresFor(access.Campaign, target.Path(), src.body, src.hash),
	})
}

// save answers the `PUT`, and it is the whole of S-6.2 and S-6.4.
//
// Every branch returns, and the two that must record nothing do so by returning
// before the append. That is the whole of S-6.3 — no silent overwrite, no
// last-write-wins fallback — expressed structurally: there is no path from a
// failed or absent comparison to `Append` or to `WriteFile`, and adding one would
// be a four-line change to a function whose every other line is a comment about why
// it is where it is.
func (h *Handler) save(
	w http.ResponseWriter,
	r *http.Request,
	access campaigns.Access,
	target *content.Target,
) {
	ctx := r.Context()

	// 1. The precondition, before the body. Two reasons for the order: a request
	// with no validator is refused without a body upload, and — more importantly —
	// a 428 renders no editor, so it cannot be mistaken for a re-render that
	// cleared the GM's buffer. See writePreconditionRequired.
	ifMatch := strings.TrimSpace(r.Header.Get(ifMatchHeader))
	if ifMatch == "" {
		h.writePreconditionRequired(w, r, access, target)

		return
	}

	// 2. The body, under the same cap the read path applies (S-4.7). The cap is
	// `content.ReadDocument` rather than a limit of our own so that a page the
	// reader may not be *shown* is also a page that may not be *written*: a
	// request that could put 500MB into a content root is a denial of service
	// aimed at the operator's disk, and the read path's answer to it is the
	// project's answer.
	body, err := content.ReadDocument(r.Body)
	if err != nil {
		h.writeFailure(w, r, classify(err))

		return
	}

	// 3. What is on disk now. Read *after* the precondition is known to be present
	// and *before* it is compared, because the comparison is against these bytes
	// and nothing else — S-5.2 makes validity a fact about content, so a validator
	// computed from a stat or from a row would be a validator about the
	// filesystem rather than about the file.
	current, err := h.readSource(ctx, target)
	if err != nil {
		h.writeFailure(w, r, err)

		return
	}

	currentValidator := validator(access.Campaign.ID, target.Path(), current.hash)

	// 4. The comparison. A mismatch — including a header that cannot be parsed —
	// answers 412 with the current content and its hash, and records nothing: no
	// revision row, no file write, not a byte changed. The evidence that this is
	// true rather than intended is `TestAStaleIfMatchAnswers412AndChangesNothing`,
	// which asserts all three clauses against the table and the bytes.
	if !matches(ifMatch, currentValidator) {
		h.writeConflict(w, r, access, target, current, string(body), currentValidator)

		return
	}

	// 5. The write, in the architecture record's order: append the revision, then
	// move the file. The order is the record's and the argument for it is which of
	// the two failures is recoverable — a revision row for content that did not
	// land is noise a later save duplicates, while a file that landed with no row
	// is a hole in the one part of this project's state the vault cannot rebuild
	// (ADR 0008). `Target.WriteFile` does the atomic write: a temp file in the same
	// directory, fsynced, renamed over the target, then the directory fsynced.
	if err := h.Revisions.Append(ctx, Revision{
		CampaignID: access.Campaign.ID,
		Path:       target.Path(),
		Content:    string(body),
		AuthorID:   campaigns.Requestor(ctx).UserID,
		Source:     SourceWeb,
	}); err != nil {
		// Before the write, so nothing on disk has changed. A 500 and not a 412:
		// the precondition *did* match, and telling the GM their save conflicted
		// would send them to reconcile a conflict that does not exist.
		h.log(ctx, slog.LevelError, "edit.revision_failed",
			slog.String("campaign", access.Campaign.Slug),
			slog.String("path", target.Path()),
			slog.String("error", err.Error()),
		)
		h.writeFailure(w, r, err)

		return
	}

	if err := target.WriteFile(ctx, body, pageFileMode); err != nil {
		// After the rename may already have happened, and `content`'s own
		// documentation says what that means: *durability unknown*, not *not
		// written*. The status cannot distinguish the two from here, so the error
		// log has to — and it does, because the write path's own line is the only
		// record of which half happened.
		h.log(ctx, slog.LevelError, "edit.write_failed",
			slog.String("campaign", access.Campaign.Slug),
			slog.String("path", target.Path()),
			slog.String("error", err.Error()),
		)
		h.writeFailure(w, r, err)

		return
	}

	// 6. The new validator, over the bytes that were just written rather than over
	// a re-read of the file. The rename replaced the file, so a re-read would be a
	// second read of a tree a sync client is free to be writing to, and the
	// validator a client is handed has to be the one its own bytes produce.
	// Nothing else happens: no re-render, no re-index, no publish (S-6.4). The
	// watcher's own event is what converges the index and the caches, and this
	// route's only job now is to say what the page is.
	h.writeSaved(w, validator(access.Campaign.ID, target.Path(), contentHash(body)))
}

// source is a page's bytes as they were read, and its digest.
//
// A local copy of the same idea as `wiki`'s `source`, and the reason it is not
// shared is the same as for the failure classification: the type is unexported
// there, and a route does not import a sibling route. The digest is taken here,
// over the bytes as read, because S-5.2 makes validity a fact about content and a
// `stat` answers a question about the filesystem, which a sync client is free to
// change without the file changing.
type source struct {
	body string
	hash string
}

// readSource reads a page's bytes through the confined root and digests them.
//
// `Target.Open` rather than `ReadFile` so the size cap applies while the bytes
// arrive: applying it afterwards bounds the parser but not the buffer, and the
// file came from a directory a sync client writes to. The close is checked rather
// than deferred and dropped, for the reason `wiki`'s is: a read handle left open on
// a watcher-driven tree is a descriptor leak on a server that never restarts.
func (h *Handler) readSource(ctx context.Context, target *content.Target) (source, error) {
	file, err := target.Open()
	if err != nil {
		return source{}, classify(err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			h.log(ctx, slog.LevelWarn, "edit.page_close_failed",
				slog.String("path", target.Path()),
				slog.String("error", closeErr.Error()),
			)
		}
	}()

	data, err := content.ReadDocument(file)
	if err != nil {
		return source{}, classify(err)
	}

	return source{body: string(data), hash: contentHash(data)}, nil
}

// preview renders the buffer through the publication pipeline.
//
// `slug` is the campaign the gate resolved for this request. It is a log-line
// input for the renderer and never an authorisation input: the route is behind
// `RequireEdit`, and a handler that re-derived the tier from the slug would be the
// second copy of the S-8 matrix ADR 0024 exists to prevent.
//
// Three calls in S-5.7's order — redact, parse, render — and the order is the
// requirement rather than a convention, so it is stated here where the three calls
// are. `include_secrets` is true: §4.8's own rule is that the preview is rendered
// with secrets included, because the buffer is the GM's own content and the
// editor is not a disclosure surface. That makes the redactor a pass-through
// *today*, and the call stays because the phase that replaces `content.NoSecrets`
// with one that removes things must not have to add it.
//
// What the preview does **not** do is attach resolved addresses to the page's
// references. That step writes markup no sanitiser has seen, and
// `internal/httpapi/wiki/links.go` owns it and argues for it at length; a second
// copy in an editor would be a second implementation of a security boundary. A
// wikilink therefore previews as the renderer emits it — an anchor with no
// destination — while every extension around it renders for real.
func (h *Handler) preview(slug string, src source) (string, error) {
	body, err := h.Redactor.Redact(src.body, true)
	if err != nil {
		return "", fmt.Errorf("redact preview: %w", err)
	}

	renderer, err := h.Renderers.Renderer(slug)
	if err != nil {
		return "", fmt.Errorf("renderer for preview: %w", err)
	}

	rendered, err := renderer.Render(content.Parse([]byte(body), h.Kinds))
	if err != nil {
		return "", fmt.Errorf("render preview: %w", err)
	}

	return rendered.HTML, nil
}

// pageTarget confines the page path a URL carried, and adds the extension the URL
// does not have.
//
// `Root.At` and not `filepath.Join`: a path from a request is untrusted, and the
// confinement is `os.Root`'s rather than a lexical normaliser's (S-3.5). The value
// arrives already unescaped by the mux — `PathValue` returns `%20` as a space — so
// an escaped `%2F` reaches here as a separator, and is harmless for exactly the
// reason everything else here is: the path it forms is confined to the campaign's
// root before it is opened.
//
// A `..` reaches this function in its percent-encoded spelling. A literal one never
// does: `net/http` cleans the request path and answers with a redirect before a
// handler runs, and only an escaped one survives that — which is what makes the
// confinement load-bearing rather than a second opinion.
func pageTarget(root *content.Root, urlPath string) (*content.Target, error) {
	if urlPath == "" {
		// The route matched `/c/{slug}/edit/` with nothing after it. A path that
		// names nothing is malformed rather than absent: appending the extension
		// would look for a file called `.md`, which is a page nobody can have
		// written and a 404 that says so for the wrong reason.
		return nil, classify(content.ErrInvalidRef)
	}

	rel := urlPath
	if !strings.HasSuffix(rel, pageExtension) {
		rel += pageExtension
	}

	target, err := root.At(rel)
	if err != nil {
		return nil, classify(err)
	}

	return target, nil
}

// pageName is what a page calls itself in the interface: its base name, without
// the extension.
//
// Not the front matter's `title:`, and that is a deliberate refusal rather than an
// omission — `content.Reference.Label` is the author's alias or the author's target
// and never a title read from the target page, which is ADR 0017's rule and is as
// much about the cache as about privacy. Reading `title:` for this heading and not
// for the links would give a wiki two names for one page.
func pageName(pagePath string) string {
	return strings.TrimSuffix(path.Base(pagePath), pageExtension)
}

// documentTitle composes this route's document title in UI §7.2's form.
func documentTitle(heading string, campaign domain.Campaign) string {
	name := campaign.Name
	if name == "" {
		name = campaign.Slug
	}

	return heading + titleSeparator + name
}

// log writes one line, discarding it when no logger is configured.
//
// The context is threaded rather than replaced: the request's values are what tie
// this line to the access-log entry for the same request, and a log line that
// cannot be correlated with the request it describes is the failure slog exists to
// prevent.
func (h *Handler) log(ctx context.Context, level slog.Level, event string, attrs ...slog.Attr) {
	if h.Logger == nil {
		return
	}

	h.Logger.LogAttrs(ctx, level, "edit",
		append([]slog.Attr{slog.String("event", event)}, attrs...)...)
}

// reportConflict records a refused save through `observability`, which is the only
// spelling `conflict.412` has anywhere in the project.
//
// Through `observability.Event` rather than `h.log`, and the reason is S-12.3: the
// `EventAttributes` type has no field a page body could be passed through, so an
// event emitted through it cannot carry file contents. The two attributes written
// here are the campaign's id and the page's path — the two S-12.3 names — and the
// `Detail` is a discriminator rather than a sentence, which is that type's
// contract. The level is warn: a 412 is a client-visible outcome, and a fight
// between two writers is an operator's problem when it becomes a pattern rather
// than when it happens once.
func (h *Handler) reportConflict(ctx context.Context, access campaigns.Access, pagePath string) {
	observability.Event(ctx, h.Logger, observability.EventConflict412, slog.LevelWarn,
		observability.EventAttributes{
			CampaignID: strconv.FormatInt(access.Campaign.ID, 10),
			Path:       pagePath,
			Detail:     "stale_if_match",
		},
	)
}
