package assets

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/web/components"
)

// Failure classification: which of the answers is answered with which status, and
// what a reader is shown for each.
//
// The content layer's answers are three distinct types of fact and conflating any
// two of them is a security bug rather than a simplification — `content.Root` says
// so above its sentinels, and this file is the other half of that claim:
//
//   - **ErrNotExist** — inside the campaign's root, not there. A 404, and the only
//     answer here that is a statement about existence.
//   - **ErrOutsideRoot / ErrSymlink** — the path leaves the root, or runs through
//     a link the policy refuses. A 403. Never a 404: a 404 says "there is nothing
//     at this path", and answering it for a path that left the campaign turns an
//     asset read into a probe for the layout of the host's filesystem.
//   - **ErrInvalidRef** — the path never named a location: empty, absolute, or
//     carrying a NUL. A 400, because the input is malformed rather than hostile.
//
// This route's own three refusals join them, and each is a statement about a file
// rather than about a path:
//
//   - **errHiddenName** — a name the index also refuses, so it is a 404: from the
//     product's point of view a `.semiplane-….tmp` staging file is not campaign
//     content, and answering 403 would make a sync client's own bookkeeping look
//     like a permission problem.
//   - **errRefusedKind** — a kind of file this route declines to serve. A 403,
//     because the file is there and a GM who dropped a `.md` into the vault is
//     owed the reason rather than a claim that it does not exist.
//   - **errUnknownKind** — an extension the closed table has no opinion on, and a
//     404 for the same reason as errHiddenName: there is no decision to report.
//
// The refusals' text carries no path and neither does the response, which is the
// property that makes a refusal safe to describe. `content.Root` refuses to put the
// offending value in its error for exactly this reason, and a body that echoed it
// would undo that one string later — and here the string would be the largest text
// on the page.

// The refusals this route produces itself. None of them wraps anything: each is
// the whole answer, and each says the same thing whatever path provoked it.
var (
	errHiddenName      = errors.New("assets: name is not campaign content")
	errRefusedKind     = errors.New("assets: this kind of file is not served")
	errUnknownKind     = errors.New("assets: extension has no servable type")
	errNotARegularFile = errors.New("assets: not a regular file")
)

// refusalError is a classified failure: an error and the status it is answered
// with.
//
// A type rather than a status returned alongside an error so that the
// classification travels with the failure instead of with the caller: a handler
// that forgets to classify an error gets a 500, which is the safe direction, and
// one that classifies it twice cannot — there is one function to change.
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
	case errors.Is(err, content.ErrNotExist),
		errors.Is(err, content.ErrNotDir),
		errors.Is(err, errHiddenName),
		errors.Is(err, errUnknownKind),
		errors.Is(err, errNotARegularFile):
		return refusalError{status: http.StatusNotFound, err: err}
	case errors.Is(err, content.ErrOutsideRoot),
		errors.Is(err, content.ErrSymlink),
		errors.Is(err, errRefusedKind):
		return refusalError{status: http.StatusForbidden, err: err}
	case errors.Is(err, content.ErrInvalidRef):
		return refusalError{status: http.StatusBadRequest, err: err}
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
// The same state the wiki route answers its failures with, and for the same
// reason: a reader who reaches here passed the gate, so they are a member of this
// campaign or a reader of a public one, and "not found" from `net/http` is plain
// text with no shell around it. The request id is the whole content of the state
// and it comes from the context rather than from the header, so a reader who sent
// their own `X-Request-Id` gets a prefixed echo rather than adoption.
//
// `no-store` rather than a validator's directives, because there is no validator
// here and a failure is the one response a cache must not keep: a pinned 404
// outlives the file that caused it, and the reader who adds that file is told it
// does not exist until they clear their cache.
func (h *Handler) writeFailure(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)
	status := statusFor(err)

	view := components.WikiPage(components.WikiPageView{
		Shell: components.ShellView{
			Title:       documentTitle(refusedHeading, access.Campaign),
			Instance:    h.Instance,
			Account:     components.AccountView{Username: campaigns.Requestor(ctx).Username},
			SignOutHref: h.SignOutHref,
		},
		Heading: refusedHeading,
		Failure: &components.LoadFailure{Reference: middleware.MustRequestID(ctx)},
	})

	header := w.Header()
	header.Set(contentTypeHeader, documentContentType)
	header.Set(varyHeader, cookieVary)
	header.Set(cacheControlHeader, noStore)

	w.WriteHeader(status)

	if renderErr := view.Render(ctx, w); renderErr != nil {
		// The status line is committed and a body is already on the wire, so there
		// is no second answer available.
		h.log(ctx, slog.LevelError, "asset.failure_render_failed",
			slog.Int("status", status),
			slog.String("error", renderErr.Error()),
		)
	}

	h.logFailure(ctx, access.Campaign.Slug, status, err)
}

// logFailure records a classified failure at a level that matches what it means.
//
// A 500 is the operator's: something is wrong that a request cannot fix. A 404 is
// the reader's — a mistyped link, a file a sync has not written yet — and is
// logged at debug so an operator's log is not a wall of ordinary misses. A 403 is
// between the two, and the access log already carries the status for every
// request.
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

	h.log(ctx, level, "asset.failed", attrs...)
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

	h.Logger.LogAttrs(ctx, level, "asset",
		append([]slog.Attr{slog.String("event", event)}, attrs...)...)
}

// documentTitle composes this route's document title in UI §7.2's form.
//
// The campaign falls back to its slug when it has no name, the same fallback
// `components.NewCampaignCard` makes: a campaign registered without a name still
// has a URL, and a document title with a blank slot is the first thing a new
// install looks like.
func documentTitle(heading string, campaign domain.Campaign) string {
	name := campaign.Name
	if name == "" {
		name = campaign.Slug
	}

	return heading + titleSeparator + name
}
