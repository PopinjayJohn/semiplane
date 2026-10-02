package wiki

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/web/components"
)

// Failure classification: which of the content layer's answers is answered with
// which status, and what a reader is shown for each.
//
// The three answers are distinct types of fact and conflating any two of them is
// a security bug rather than a simplification — `content.Root` says so in the
// comment above its sentinels, and this file is the other half of that claim:
//
//   - **ErrNotExist** — inside the campaign's root, not there. A 404, and the
//     only answer here that is a statement about existence.
//   - **ErrOutsideRoot / ErrSymlink** — the path leaves the root, or runs through
//     a link the policy refuses. A refusal. Never a 404: a 404 says "there is
//     nothing at this path", and answering it for a path that left the campaign
//     turns a page read into a probe for the layout of the host's filesystem.
//   - **ErrInvalidRef** — the path never named a location: empty, absolute, or
//     carrying a NUL. A 400, because the input is malformed rather than hostile.
//
// The refusals' text carries no path and neither does the response, which is the
// property that makes a refusal safe to describe: `content.Root` refuses to put
// the offending value in its error for exactly this reason, and a body that echoed
// it would undo that in one line. The path is in the access log, where the
// middleware wrote it before this handler ran.

// refusalError is a classified failure: an error and the status it is answered
// with.
//
// A type rather than a status returned alongside an error so that the
// classification travels with the failure instead of with the caller: a handler
// that forgets to classify an error gets a 500, which is the safe direction, and
// one that classifies it twice cannot — there is one function to change.
//
// `errname` has opinions about names, and this one follows it: a type whose name
// does not read as an error is a type a reader has to stop and work out.
type refusalError struct {
	status int
	err    error
}

// Error returns the underlying failure's message, which for a refusal is a fixed
// sentence and never the caller's path.
func (e refusalError) Error() string {
	return e.err.Error()
}

// Unwrap keeps `errors.Is` reaching the content layer's own sentinel, so a caller
// — or a test — can ask what actually happened rather than only how it was
// answered.
func (e refusalError) Unwrap() error {
	return e.err
}

// classify turns one of the content layer's answers into a refusal, and leaves
// everything else alone.
//
// A refusal is a value so that the default is the 500 rather than the 404: an
// error this function has not been taught about is an error whose answer is
// unknown, and "unknown" must not become "there is nothing at this path".
func classify(err error) error {
	switch {
	case errors.Is(err, content.ErrNotExist), errors.Is(err, content.ErrNotDir):
		return refusalError{status: http.StatusNotFound, err: err}
	case errors.Is(err, content.ErrOutsideRoot), errors.Is(err, content.ErrSymlink):
		return refusalError{status: http.StatusForbidden, err: err}
	case errors.Is(err, content.ErrInvalidRef):
		return refusalError{status: http.StatusBadRequest, err: err}
	case errors.Is(err, content.ErrDocumentTooLarge):
		// A page over the size cap, on a campaign the reader may read. Not a 404,
		// because the page is there; not a 413, because nothing says "this page is
		// too large to show you" and a status a GM's tooling has never seen is
		// worse than an honest one. The error log names the path and the limit and
		// the request id ties the reader's screenshot to it.
		return refusalError{status: http.StatusInternalServerError, err: err}
	default:
		return err
	}
}

// statusFor answers a classified failure, and 500 for anything unclassified.
func statusFor(err error) int {
	if refusal, found := errors.AsType[refusalError](err); found {
		return refusal.status
	}

	return http.StatusInternalServerError
}

// writeFailure sends the load-error state with the classified status.
//
// The load-error state and not a bare status page, because the reader got here
// authenticated: they passed a gate, so they are a member of this campaign or a
// reader of a public one, and "not found" from `net/http` is plain text with no
// shell around it and no reference in it. A GM who mistyped a `[[wikilink]]` needs
// to be able to tell the reader's report from an attacker's.
//
// The same campaign shell as the page, and for the same reason UI §4.6 gives:
// the reader is inside a campaign. A GM who followed a broken link needs the
// navigation to get out of here, and a page whose nav vanished because the page
// was missing would make the *failure* the only route on the site that cannot
// navigate anywhere.
//
// The navigation carries its destinations but **not** the page tree. The listing
// is what a cache miss costs and a failure is not a cache miss: the reader is
// here because something is wrong with one page, and spending a query to list
// every page in the campaign so the error state has a sidebar is the wrong
// trade. `NavView.hasWiki` is false for the nil tree, so the Wiki section is
// absent — and §4.6's rule is that a section with nothing in it is absent, not
// empty.
//
// The request id is the whole content of the state, and it comes from the
// context rather than from the header: the middleware put it there after
// validating it, and a reader who sent their own `X-Request-Id` gets it echoed
// back with a prefix rather than adopted.
func (h *Handler) writeFailure(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)
	status := statusFor(err)

	// The page's own name as the heading for a 404, and a fixed one for every
	// refusal. A 404 is about a page inside the campaign the gate admitted the
	// reader to, so naming it is the useful answer; a refusal is about a path that
	// was never a page, and echoing it into the largest text on the page would
	// undo the confinement one string later.
	heading := refusedHeading
	if status == http.StatusNotFound {
		heading = pageName(r.PathValue("path"))
	}

	view := h.document(r, h.navigation(ctx, access, nil), components.WikiPageView{
		Heading: heading,
		Failure: &components.LoadFailure{Reference: middleware.MustRequestID(ctx)},
	})

	w.Header().Set("Content-Type", contentType)
	w.Header().Set(varyHeader, cookieVary)
	// `no-store`, and not the key's own directives: there is no key here, and a
	// failure is the one response a cache must not keep — a pinned 404 outlives
	// the page that caused it, and the reader who wrote that page gets told it does
	// not exist until they clear their browser.
	w.Header().Set(cacheControlHeader, noStore)

	w.WriteHeader(status)

	h.writeDocument(w, r, view, "wiki.failure_render_failed")

	h.logFailure(ctx, access.Campaign.Slug, status, err)
}

// logFailure records a classified failure at a level that matches what it means.
//
// A 500 is the operator's: something is wrong that a request cannot fix. A 404 is
// the reader's — a mistyped link, a page that has not been written yet — and is
// logged at debug so an operator's log is not a wall of ordinary misses. A 403 is
// between the two: the access log already carries the status for every request,
// so it is recorded here without being escalated.
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

	h.log(ctx, level, "wiki.page_failed", attrs...)
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

	h.Logger.LogAttrs(ctx, level, "wiki",
		append([]slog.Attr{slog.String("event", event)}, attrs...)...)
}

// noStore is the `Cache-Control` a failure response carries.
const noStore = "no-store"
