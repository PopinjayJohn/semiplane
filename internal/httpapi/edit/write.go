package edit

// The five responses this route writes, and what each one is a statement about.
//
// They are in one file because the distinctions between them *are* the
// distinctions between the requirements, and a reader who has them in five places
// has to reassemble the contract to know whether one of them is right:
//
//   - `writeDocument` — the editor, 200. Also the 412's carrier, because a 412 is
//     an editor with a diff in its preview slot (UI §4.7) and not a body of its
//     own.
//   - `writeSaved` — 204 and a validator. No body, and saying so is the point.
//   - `writeConflict` — 412, the current content, and its hash. See below.
//   - `writePreconditionRequired` — 428. See below.
//   - `writeFailure` — everything else, with the classified status.
//
// # What the 412 carries, and what it must not
//
// S-6.2: "a mismatch returns 412 with the current body and its hash, and records
// nothing". So the response carries three things and no others:
//
//   - **the current content** — the disk's source, in the diff's second column, as
//     text rather than as rendered HTML. Rendered HTML would be a worse answer: a
//     GM reconciling a conflict needs to see what is *in the file*, and a rendered
//     page cannot be copied back into the editor without re-typing it.
//   - **its hash** — twice, in two forms, and the duplication is the point. The
//     `ETag` header is the salted validator a follow-up save must present in
//     `If-Match`, and it is what a client reads without parsing anything. The hex
//     digest in the body is the same fact unsalted, and it is what a GM can paste
//     into a diff tool when they ask "what changed" — S-5.3's salt exists so a
//     GM's validator and a player's are never the same string, which means the
//     validator itself answers no question about content.
//   - **the GM's own buffer** — in the textarea, untouched. §4.8's last row and
//     §11's risk table both make this the load-bearing property, and it is why
//     this response is a full editor document: a diff alone would be a page nobody
//     could save from.
//
// And two things it must not carry:
//
//   - **a server path.** `content.Root` refuses to put the offending path in its
//     own errors because an error whose text varies with the caller's input is a
//     channel; a conflict body echoing the content root's directory layout would
//     hand the same thing to a GM who typed a traversal, and the 403 and the 412
//     are the two answers most likely to be confused for each other in a log.
//   - **a second read of the file.** The disk's text and its digest are passed in
//     from the save path rather than re-read here, because a sync client is free to
//     write between the read that detected the conflict and the read that rendered
//     it, and a response containing two different "current" texts is worse than
//     one that is a moment stale.

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/web/components"
	editor "github.com/semiplane/semiplane/internal/web/components/edit"
	"github.com/semiplane/semiplane/internal/web/components/ui"
)

// writeDocument renders the editor, inside the shell, with the given status.
//
// One writer for the 200 and the 412, because the 412 *is* this document with a
// conflict in the preview slot: §4.7's row says the diff goes in the preview slot
// and §4.8 says the buffer stays where it is, so a 412 rendering anything else
// would be a second layout for one screen.
//
// The headers are the same on both, and the `ETag` is whatever the view carries —
// the current validator in both cases, which is the point: after a 412 the
// precondition a client holds is the *disk's*, not the one it sent.
func (h *Handler) writeDocument(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	view editor.EditorView,
) {
	ctx := r.Context()

	w.Header().Set(contentTypeHeader, htmlContentType)
	w.Header().Set(varyHeader, cookieVary)
	// S-5.4: this document renders with secrets included and advertises a
	// GM-salted validator, so it is never cacheable by anything. `no-store` rather
	// than the cache key's own `private, no-store` because there is no cache key
	// here — the editor does not use the render cache, and asking for a key's
	// directives would mean building a key for a route that never looks one up.
	w.Header().Set(cacheControlHeader, noStore)
	w.Header().Set(etagHeader, view.Validator)

	w.WriteHeader(status)

	if err := editor.EditorPage(view).Render(ctx, w); err != nil {
		// Logged and nothing else: the status line is committed and a body is
		// already on the wire, so there is no second answer available.
		h.log(ctx, slog.LevelError, "edit.render_failed",
			slog.Int("status", status),
			slog.String("error", err.Error()),
		)
	}
}

// writeSaved answers a matched write: 204 and the new validator, with no body.
//
// 204 rather than 200, and the empty body rather than a JSON acknowledgement of
// it. A save that returned content would put a second representation of the page
// on the wire — one that is neither the editor nor the published page, and one
// whose bytes a client would have to learn to ignore. The only thing a client needs
// from a save is the validator to present next time, and that is a header.
//
// `Cache-Control: no-store` for the same reason the editor's own document carries
// it (S-5.4): the validator is salted with `include_secrets=true`, and a stored
// GM-salted validator is a stored answer to "what is this page's current version"
// for a page that may hold secret text.
//
// `Vary: Cookie` is deliberately absent, unlike the two documents above. Those
// carry the reader's name in the shell, so a shared cache keyed only on the URL
// would hand one GM another's header; this response carries no body and no
// reader-dependent value, and a `Vary` on a response that varies on nothing is a
// cache hint that costs a key.
func (h *Handler) writeSaved(w http.ResponseWriter, newValidator string) {
	w.Header().Set(etagHeader, newValidator)
	w.Header().Set(cacheControlHeader, noStore)

	w.WriteHeader(http.StatusNoContent)
}

// writeConflict answers a stale save: 412, the editor, and the diff.
//
// The arguments beyond the request are the three facts the response is made of,
// passed rather than re-read for the reason the file comment gives.
func (h *Handler) writeConflict(
	w http.ResponseWriter,
	r *http.Request,
	access campaigns.Access,
	target *content.Target,
	current source,
	buffer string,
	currentValidator string,
) {
	ctx := r.Context()

	// The view's own validator is the *disk's*, and that is what the editor's save
	// control and its hidden field now carry. A GM who accepts every hunk and
	// presses save must present H2, not the H1 they arrived with — and a save that
	// presented H1 would be refused again, which is the one outcome that would make
	// the conflict UI feel broken. The buffer is untouched, so nothing is lost by
	// the precondition moving forward.
	view := editor.EditorView{
		Shell:       h.shellFor(r, pageName(target.Path())),
		Page:        pageName(target.Path()),
		Buffer:      buffer,
		Validator:   currentValidator,
		ContentHash: current.hash,
		State:       editor.SaveConflict,
		Conflict: &editor.Conflict{
			// The buffer first and the disk second, because that is the order of
			// the two columns and the order of the two headings that name them.
			// A transposition here would produce a diff that is perfectly
			// plausible and completely backwards.
			Hunks:       editor.Diff(buffer, current.body),
			Validator:   currentValidator,
			ContentHash: current.hash,
		},
	}

	h.reportConflict(ctx, access, target.Path())
	h.writeDocument(w, r, http.StatusPreconditionFailed, view)
}

// writePreconditionRequired answers a save that named no validator: 428.
//
// **Not a 200, and that is the whole of the reason this function exists.** A
// `PUT` with no `If-Match` is a request to replace the file with whatever the body
// says, unconditionally — which is precisely what S-6.3 forbids ("no silent
// overwrite, no last-write-wins fallback") and precisely what a file lock cannot
// prevent, because Obsidian is a separate process that has never heard of this
// server's state. Answering 200 would make the *absence* of the precondition
// succeed while a stale one fails, which is a worse contract than either: a client
// that dropped the header would believe it had saved.
//
// RFC 6585's 428 is the status for exactly this — the origin server requires the
// request to be conditional — and it is a 4xx a client can act on by re-reading
// the page.
//
// **No editor is rendered, and that is the second decision in this function.** A
// 428 is reached before the request body is read, so there is no buffer to echo —
// and rendering the editor with an *empty* textarea is the worst of the available
// answers: a client that replaced its document on any response would silently
// discard the GM's unsaved work in exchange for a status code that already said
// nothing was written. So this is a statement about the request, in the shell, and
// not a view of the page.
//
// **The current validator is read for the response, and that read can fail.** The
// header is a convenience — a client that can see what it should have sent can
// recover without a second round trip — so a page that has become unreadable since
// the request is answered 428 with no validator rather than with a different
// status. Failing the whole request because a *convenience* header could not be
// produced would turn a protocol error into a confusing one.
func (h *Handler) writePreconditionRequired(
	w http.ResponseWriter,
	r *http.Request,
	access campaigns.Access,
	target *content.Target,
) {
	ctx := r.Context()
	page := pageName(target.Path())

	currentValidator := ""
	if src, err := h.readSource(ctx, target); err == nil {
		currentValidator = validator(access.Campaign.ID, target.Path(), src.hash)
	} else {
		h.log(ctx, slog.LevelDebug, "edit.precondition_hash_unavailable",
			slog.String("campaign", access.Campaign.Slug),
			slog.String("path", target.Path()),
			slog.String("error", err.Error()),
		)
	}

	w.Header().Set(contentTypeHeader, htmlContentType)
	w.Header().Set(varyHeader, cookieVary)
	w.Header().Set(cacheControlHeader, noStore)

	if currentValidator != "" {
		w.Header().Set(etagHeader, currentValidator)
	}

	w.WriteHeader(http.StatusPreconditionRequired)

	view := editor.PreconditionView{
		Shell:     h.shellFor(r, page),
		Page:      page,
		Validator: currentValidator,
	}

	if err := editor.PreconditionPage(view).Render(ctx, w); err != nil {
		h.log(ctx, slog.LevelError, "edit.precondition_render_failed",
			slog.String("error", err.Error()),
		)
	}
}

// writeFailure answers everything that is neither a conflict nor a missing
// precondition: 400, 403, 404, 413 and 500.
//
// The load-failure state for four of the five, and the 413's own for the fifth —
// `content.ErrDocumentTooLarge` here means the *request body* was too large, and
// `ui.LoadError`'s sentence ("semiplane could not load this page") would be false
// in a way the GM would notice, because the page loaded perfectly and the save was
// what did not fit.
//
// `no-store` on a failure for the wiki route's reason: a pinned 404 outlives the
// page that caused it, and a GM who mistyped a path would be told the page does not
// exist until they clear their browser. The reference is the request id, which
// ties their screenshot to the access-log line for the same request.
func (h *Handler) writeFailure(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)
	status := statusFor(err)
	heading := refusedHeading

	if status == http.StatusNotFound {
		heading = pageName(r.PathValue("path"))
	}

	var state templ.Component

	if status == http.StatusRequestEntityTooLarge {
		state = editor.TooLarge(editor.TooLargeView{
			Limit:   strconv.Itoa(content.MaxDocumentBytes),
			Heading: ui.HeadingPage,
		})
	} else {
		state = ui.LoadError(ui.LoadFailure{
			Reference: middleware.MustRequestID(ctx),
			Heading:   ui.HeadingPage,
		})
	}

	w.Header().Set(contentTypeHeader, htmlContentType)
	w.Header().Set(varyHeader, cookieVary)
	w.Header().Set(cacheControlHeader, noStore)

	w.WriteHeader(status)

	view := editor.FailureView{Shell: h.shellFor(r, heading), State: state}
	if renderErr := editor.FailurePage(view).Render(ctx, w); renderErr != nil {
		h.log(ctx, slog.LevelError, "edit.failure_render_failed",
			slog.Int("status", status),
			slog.String("error", renderErr.Error()),
		)
	}

	h.logFailure(ctx, access.Campaign.Slug, status, err)
}

// shellFor is the chrome for one response, with the document title composed here.
//
// The title is composed by the route and not by the shell, for the reason
// `wiki/handler.go` composes it too: "Page — Section — Campaign" (UI §7.2) is a
// format, and a shell that derived the middle part from a field the caller might
// leave blank would produce a document titled " — — semiplane" for a route that
// forgot. The campaign falls back to its slug when it has no name, the same
// fallback `components.NewCampaignCard` makes: a campaign registered without a
// name still has a URL, and a document title with a blank slot is the first thing
// a new install looks at.
func (h *Handler) shellFor(r *http.Request, heading string) components.ShellView {
	access := campaigns.AccessFrom(r.Context())

	return components.ShellView{
		Title:       documentTitle(heading, access.Campaign),
		Instance:    h.Instance,
		Account:     components.AccountView{Username: campaigns.Requestor(r.Context()).Username},
		SignOutHref: h.SignOutHref,
	}
}

// logFailure records a classified failure at a level that matches what it means.
//
// A 500 is the operator's: something is wrong that a request cannot fix. A 404 is
// the GM's — a mistyped path, a page that has not been written yet — and is logged
// at debug so an operator's log is not a wall of ordinary misses. A 403 is between
// the two, and the access log already carries the status for every request.
func (h *Handler) logFailure(ctx context.Context, slug string, status int, err error) {
	level := slog.LevelDebug
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	}

	attrs := []slog.Attr{
		slog.Int("status", status),
		slog.String("error", err.Error()),
	}
	if slug != "" {
		attrs = append(attrs, slog.String("campaign", slug))
	}

	h.log(ctx, level, "edit.page_failed", attrs...)
}
