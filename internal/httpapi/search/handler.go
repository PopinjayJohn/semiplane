// Package search serves one route: `GET /c/{slug}/search?q=`.
//
// # Submit-to-navigate, and every decision in this package follows from it
//
// UI §7.5 is explicit that a search result page is an ordinary HTTP response: a
// form, a submit, a server round trip, and an `ETag`-cacheable answer. The reason
// it is not type-ahead is not a preference — it is that a client-side renderer
// *cannot reproduce the server's tokenisation*. ADR 0007 fixes
// `unicode61 remove_diacritics 2` and deliberately no `porter`, because TTRPG
// proper nouns stem badly; a browser folding `Svartalfheim` into a word nobody
// typed is the failure, and no amount of care in a client makes it go away.
// S-11.3 says the same thing from the server's side: a client-side search
// renderer is prohibited.
//
// So there is no fetch on keystroke here, no debounce, and no partial document.
// Nothing in this package or in `results.templ` produces script, and the single
// form is `method="get"` so the URL a reader is looking at is the query they ran.
//
// # What the route is responsible for
//
//   - **Which document to render.** Four: the §4.7 idle state, the §4.7
//     no-results state, a result list, and the load-error state. §7.5 puts the
//     count in the `<h1>`, so the result list's own heading is the count and
//     nothing else on the page is an `<h1>`.
//   - **Handing the store a campaign scope and a requestor, and post-filtering
//     nothing.** `store.SearchPages` joins `campaigns` on visibility (S-8.2) and
//     takes the requestor as data. A bare query returns private titles to an
//     anonymous user, and that is a security failure rather than a display bug,
//     so the only way this route can be right is by being unable to ask the wrong
//     question — see `searchScope`.
//   - **Deriving the validator from the rows.** S-5.2's argument is that validity
//     is a function of content and never of time, and a result list has no file
//     to hash; the rows are the content, so they are what the fingerprint is
//     taken over.
//
// # What it deliberately does not do
//
// **No cross-campaign search on this route.** `store.PageSearch.CampaignID` is 0
// for "every campaign the requestor may read" and that shape exists for ADR 0007's
// cross-campaign query, but the answer here is to always pass a resolved campaign
// id: a URL-scoped search is the shape where a private title leaks, because a
// scope the caller forgot is a scope of everything. Cross-campaign search is
// reachable only through the store, whose `visibleToViewer` predicate is joined
// for every row whatever the scope, so a caller that wanted it would not have to
// invent a second visibility rule — and this route does not offer it, because S-9
// gives search one URL and it carries a slug.
//
// **No live region, anywhere, on any of the four documents.** S-13.4 and §7.5: a
// result page is a fresh navigation and there is nothing to be told about. Every
// other dynamic surface in this application is a live region, which is exactly
// why this one is stated as an absence and tested as one.
//
// The `//nolint` below is this package's one suppression, and it is worth naming what
// it is about: templ writes `// templ: version:` above the package clause of every
// file it generates, and godoclint counts that as a second package godoc. Neither
// `components` nor `components/ui` meets the finding, because neither has a
// hand-written package comment — `ui` has none at all, and `components` keeps its
// overview in a `.go` file whose generated siblings are the only other candidates.
//
//nolint:godoclint // templ's generated file carries a second comment above `package`.
package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/a-h/templ"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
	"github.com/semiplane/semiplane/internal/store"
	"github.com/semiplane/semiplane/internal/web/components"
)

// The query parameter and this route's own path.
//
// `q` because S-9 fixes the URL scheme, and a search route is the one URL in it
// with a parameter; `?query=` would be a second spelling of the same thing.
const (
	// queryParameter is the name the search box submits under, and the only one
	// read here. `url.Values.Get` returns the first of several, which is what a form
	// submits and what a reader who hand-edited the URL expects; the second `?q=` in
	// `?q=a&q=b` is ignored rather than concatenated, because a concatenation is an
	// answer to a question nobody asked.
	queryParameter = "q"

	// campaignPathPrefix and searchPathSegment build this route's own action URL,
	// which every form on this route submits to. Spelled here rather than taken
	// from `content`, whose `campaignHref` is unexported and whose `WikiHref` builds
	// a *page* address rather than a route's action.
	campaignPathPrefix = "/c/"
	searchPathSegment  = "search"
)

// titleSeparator joins the parts of this route's document title. UI §7.2 fixes the
// form as "Page — Section — Campaign"; this route and `wiki` write the leading part
// and the campaign and stop, because `components`' three-part composer is
// unexported and restating it here would be a third spelling.
const titleSeparator = " — "

// pageExtension is the suffix a page's indexed path carries and a URL does not.
//
// A third spelling of `.md`: `content` and `wiki` each hold an unexported one
// this package cannot reach. Restated rather than avoided because a result's link
// is built from `pages.path`, which *does* carry the extension, and stripping it
// with a bare literal would be the same constant written a fourth time in a place
// with no name for it.
const pageExtension = ".md"

// resultCap is how many rows this route asks for.
//
// It is `store`'s own `maxSearchLimit`, restated, and restating a policy constant
// is a cost worth naming: the alternative was to ask for the default and not know
// what the default was, which would make "showing the first N results" unknowable
// and therefore untrue whenever the list was truncated. Asking for the cap itself
// makes the truncation condition exactly `len(hits) == resultCap` — "we asked for
// everything we are allowed to show". If the two constants ever disagree the only
// consequence is a slightly-off sentence in a note; the *rows* are the store's
// answer either way. A pagination parameter would fix it properly and is a
// URL-scheme change, which is an ADR and not this work item.
const resultCap = 100

// maxQueryDisplay is how much of the submitted query may be rendered.
//
// `components/ui`'s own `maxQueryDisplay`, restated for the reason `resultCap` is,
// and here it is load-bearing rather than cosmetic: §4.7 requires the query to be
// echoed into the heading and back into the field, the query is attacker-
// controlled, and an unbounded echo of a 40kB query string is a denial of service
// on the reader's own screen. `boundQuery` produces a value of at most this many
// bytes *including* its ellipsis, which is what makes the bound idempotent: the
// §4.7 states apply their own bound to whatever they are handed, and a value
// already inside it comes back untouched instead of losing another byte.
const maxQueryDisplay = 200

// ellipsis is what a truncated echo ends in. U+2026, and three bytes of it — which
// is why `boundQuery` reserves three bytes rather than one.
const ellipsis = "…"

// contentType is what a document on this route is served as. The shell is a
// complete HTML document, not a fragment, so this is the document type rather than
// a partial's.
const contentType = "text/html; charset=utf-8"

// cacheControl is the `Cache-Control` every document on this route carries.
//
// `private, no-cache` for every tier, and that is deliberately *not*
// `content.CacheControl`. A page's GM variant has to be `no-store` because the
// body may carry `[!secret]` text; a result list cannot, because a snippet is
// FTS5's excerpt of `body_plain`, which excludes callout content in **every**
// reveal state (S-5.11, UI §4.10.5). No view of a result list contains secret
// text, so the GM's list is as cacheable as a player's and saying otherwise is a
// tax with nothing behind it. The *validator* is salted by tier all the same —
// see `answer` — because that is about which rows come back, not about what is in
// them.
//
// `private` because the shell carries the reader's name and a sign-out form, and
// `Vary: Cookie` says so to anything downstream of this header.
const cacheControl = "private, no-cache"

// noStore is what a failure carries: there is no variant worth keeping, and a
// cached 400 outlives the request that produced it.
const noStore = "no-store"

// The header names this route writes. `net/http` has no constants for header
// names, and a literal repeated across three writers is one a rename would
// half-apply.
const (
	etagHeader         = "ETag"
	cacheControlHeader = "Cache-Control"
	contentTypeHeader  = "Content-Type"
	varyHeader         = "Vary"
	// cookieVary is the value of `Vary`. See writeDocument for why it is there.
	cookieVary = "Cookie"
	// ifNoneMatchHeader carries the client's validator on a revalidation.
	ifNoneMatchHeader = "If-None-Match"
	// weakPrefix marks a validator as weak (RFC 9110 §8.8.3), compared away in both
	// directions because a revalidation of a `GET` uses weak comparison.
	weakPrefix = "W/"
	// anyValidator is `If-None-Match: *`, which names any existing representation.
	anyValidator = "*"
)

// pageName is this route's own name in a document title and in the failure state's
// heading.
//
// "Search", and deliberately not the query. A document title is read before the
// page, and echoing 200 attacker-controlled bytes into `<title>` puts them into the
// browser tab, the history entry and the window title — three places the page's own
// copy has no business reaching. The count and the query live in the `<h1>`, which
// is where §7.5 puts them, and nowhere else.
const pageName = "Search"

// Pages is the query this route makes.
//
// One method, and the signature is the store's own: the request is a
// `store.PageSearch` rather than four parameters, because `CampaignID` is the field
// that is easy to get wrong — 0 means *every campaign the requestor may read* — and
// a route holding a struct whose field is documented in that sentence has to read
// the sentence to set it. A second spelling of the request in this package would be
// a conversion, and the conversion is where the scope would be dropped.
//
// `*store.Store` satisfies it.
type Pages interface {
	// SearchPages returns the rows this requestor may read, already joined against
	// campaign visibility (S-8.2).
	SearchPages(
		ctx context.Context,
		search store.PageSearch,
		req domain.Requestor,
	) ([]domain.SearchHit, error)
}

// Handler serves the campaign search route.
//
// Exported fields rather than a constructor, matching `wiki.Handler` and
// `accounts.Router`: the composition root writes one literal and a test writes
// another, and a struct literal names what it sets. Every field is written once
// before the server starts and read on every request, so a `Handler` is safe for
// concurrent use — which rests on the same thing `wiki.Handler` claims: no field is
// written after construction.
type Handler struct {
	// Pages is the search index. Required: a route with no index answers every
	// query with an empty list, which is indistinguishable from a vault nobody has
	// written and would hide a wiring fault behind a 200.
	Pages Pages

	// Instance names the running instance and reports what is unhealthy, for the
	// shell's chrome. A value rather than a lookup so that a response does not
	// depend on when it was written.
	Instance components.InstanceView

	// SignOutHref is where the header's sign-out form posts. Empty renders no form,
	// which is the right answer on an instance with no such route.
	SignOutHref string

	// Logger receives this route's lines. Nil is allowed and discards them, so a
	// test does not have to construct a logger to serve a request.
	Logger *slog.Logger
}

// Mount registers the search route on mux.
//
// mux is the campaign-scoped mux — the one `mountCampaignRoutes` fills — and the
// pattern carries the `/c/` prefix even though that mux is itself mounted there,
// because a Go 1.22 mux routes on a prefix and then hands the *whole* path to what
// it matched. `GET` alone: search has no unsafe verb, so a `HEAD` is net/http's own
// and a `POST` to this address is a 405 from the mux rather than a branch in a
// handler.
func Mount(mux *http.ServeMux, handler *Handler) {
	mux.Handle("GET /c/{slug}/search", handler)
}

// ServeHTTP answers one search request.
//
// Linear, and every branch leaves through one of three writers: a document, a 304,
// or a failure. The order below is the order the comments describe.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	page, err := h.read(
		ctx,
		campaigns.AccessFrom(ctx),
		campaigns.Requestor(ctx),
		r.URL.Query().Get(queryParameter),
		r.Header.Get(ifNoneMatchHeader),
	)
	if err != nil {
		h.writeFailure(w, r, err)

		return
	}

	if page.revalidated {
		writeNotModified(w, page)

		return
	}

	switch page.state {
	case stateIdle:
		h.writeIdle(w, r, page)
	case stateEmpty:
		h.writeEmpty(w, r, page)
	case stateResults:
		h.writeResults(w, r, page)
	default:
		// Unreachable by construction — `read` returns one of the three or an
		// error — and answered rather than ignored, because a switch that fell
		// through would have sent no status line and no body at all. This is what
		// happens if a fourth state is added without a writer, and it is a 500
		// rather than a 200 with an empty body: a reader is owed an answer.
		h.log(ctx, slog.LevelError, "search.unknown_state",
			slog.Int("state", int(page.state)))
		h.writeFailure(w, r, errors.New("search: no writer for this result state"))
	}
}

// read is the whole of one request's work, and it returns either a document to
// serve or an error carrying the status the failure is answered with.
//
// Every input is passed in rather than read from the request, for the reason
// `wiki.Handler.read` gives: a pipeline that took the `*http.Request` would have
// whatever the next change needs one header away, and the two things this one must
// never reach for are the campaign's access and the reader's identity. They are
// the first two parameters so that it is visibly not among the things it can reach.
func (h *Handler) read(
	ctx context.Context,
	access campaigns.Access,
	requestor domain.Requestor,
	query string,
	ifNoneMatch string,
) (pageResult, error) {
	// `echo` is what may be *shown*, computed before anything is searched for: the
	// value that reaches a template is decided here rather than at each
	// interpolation, so a new field cannot reach one unbounded.
	page := pageResult{echo: boundQuery(query)}

	// The §4.7 idle state, decided on the *raw* query rather than on the echo.
	//
	// `strings.TrimSpace` and not `page.echo == ""`: a reader who submitted a field
	// holding only spaces asked to search and typed nothing into it, and that is the
	// idle state. The distinction matters because a query that is present but
	// *unusable* is a different thing — punctuation only, or a term past the store's
	// per-term cap — and the store says so with a sentinel this route answers 400
	// from rather than folding into "no query". A reader whose query could not be
	// searched for is owed the refusal; the idle state would hand them an empty form
	// and no reason, which reads as "this campaign has nothing".
	if strings.TrimSpace(query) == "" {
		page.state = stateIdle

		return h.answer(access, page, ifNoneMatch, nil)
	}

	campaign, ok := searchScope(access)
	if !ok {
		// Not an authorisation check, and deliberately not phrased as one. ADR 0024
		// puts authorisation in the gate this route is mounted behind, and nothing
		// here re-asks whether the reader may search. What is missing is a
		// *campaign*: the gate did not run, so there is no slug, no id and no tier,
		// and the one scope value this route could build from it is zero — which
		// `store.PageSearch` documents as "every campaign the requestor may read".
		// Passing it would turn a composition mistake into a search across the
		// instance, which is the shape a private title leaks through. So the route
		// refuses, at error level, because a reader reaching it this way is a wiring
		// fault and not a reader's problem.
		h.log(ctx, slog.LevelError, "search.unresolved_campaign",
			slog.String("slug", access.Campaign.Slug))

		return pageResult{}, refusalError{
			status: http.StatusNotFound,
			err:    errNoCampaignScope,
		}
	}

	hits, err := h.Pages.SearchPages(ctx, store.PageSearch{
		Query:      query,
		CampaignID: campaign.ID,
		Limit:      resultCap,
	}, requestor)
	if err != nil {
		if errors.Is(err, store.ErrInvalidSearchQuery) {
			// 400, per the sentinel's own reason for existing. A 400 cannot be
			// mistaken for "no results", which is the failure the alternative —
			// rendering the idle state for anything unusable — would have had.
			return pageResult{}, refusalError{
				status: http.StatusBadRequest,
				err:    fmt.Errorf("%w: the query has no usable terms", err),
			}
		}

		return pageResult{}, fmt.Errorf("search pages: %w", err)
	}

	if len(hits) == 0 {
		page.state = stateEmpty

		return h.answer(access, page, ifNoneMatch, hits)
	}

	page.state = stateResults
	page.count = len(hits)
	page.hits = resultViews(campaign.Slug, hits)
	// Exactly the cap, which is what "we asked for everything we may show" means. A
	// count below the cap is the whole result set, so assuming truncation whenever
	// the query looks large would put a false caveat under every long search.
	page.truncated = len(hits) == resultCap

	return h.answer(access, page, ifNoneMatch, hits)
}

// answer computes the validator for a document and resolves the revalidation.
//
// One function for all three success states because the *validator* does not depend
// on the state: it is a function of the campaign, the echoed query and the rows,
// and an idle document has none of the last. Folding it in here is what keeps "every
// document on this route carries an `ETag`" from being a property three writers have
// to remember.
//
// `hits` is passed separately from `page` because the state that fills `page.hits`
// has already converted the rows, and hashing the *converted* rows would be hashing
// this package's view model rather than what the store answered. The store's rows
// are the content; the view is a rendering of them.
func (h *Handler) answer(
	access campaigns.Access,
	page pageResult,
	ifNoneMatch string,
	hits []domain.SearchHit,
) (pageResult, error) {
	// The salt is the tier's ability to edit, and it is the value the page route
	// salts with, for the reason ADR 0016 gives: a GM's response and a player's must
	// never advertise one validator (S-14.2). Nothing distinguishes them *today* —
	// visibility is per campaign rather than per page, and `body_plain` excludes
	// callout content for every role — so the salt is future-proofing rather than a
	// claim about this phase's rows. Phase 10 is the phase that could make the sets
	// differ, and the validator that could differ already does.
	//
	// `content.CacheKey` rather than a second ETag derivation in this package: its
	// `ETag()` is the project's only one, and the wiki handler's comment on why it
	// calls that function rather than computing a validator ("two derivations of a
	// validator in one process is a defect waiting for the one that changes") is the
	// rule this follows. Two of its four fields stay zero because this is not a
	// render cache key — only the two `ETag` reads are a body validator, and this
	// document has no path and no file.
	page.cacheControl = cacheControl
	page.validator = content.CacheKey{
		ContentHash:    fingerprint(access.Campaign.Slug, page.echo, hits),
		IncludeSecrets: access.Tier.CanEdit(),
	}.ETag()

	if revalidated(ifNoneMatch, page.validator) {
		page.revalidated = true
	}

	return page, nil
}

// writeIdle renders the §4.7 idle state: the field, focused, with a hint.
//
// The document is *built* by `idleDocument` and rendered here, rather than built
// inline, and the split is what `contextcheck` requires: templ's generated closures
// take a `context.Context` and cannot forward it, so a component constructed in a
// function that holds one reads to that linter as a dropped context. A constructor
// that holds no context is not the problem, and `wiki` avoids the finding for the same
// reason by living entirely inside `components`.
//
// forward it; the render below is handed one explicitly.
//
//nolint:contextcheck // templ's generated closure takes a context and cannot
func (h *Handler) writeIdle(w http.ResponseWriter, r *http.Request, page pageResult) {
	h.writeHeaders(w, http.StatusOK, page)

	if err := h.idleDocument(r, campaigns.AccessFrom(r.Context()), page).
		Render(r.Context(), w); err != nil {
		h.log(r.Context(), slog.LevelError, renderFailedEvent,
			slog.String("error", err.Error()))
	}
}

// writeEmpty renders the §4.7 no-results state, which echoes the query into its own
// heading. `writeIdle` for why the document is built elsewhere.
//
// forward it; the render below is handed one explicitly.
//
//nolint:contextcheck // templ's generated closure takes a context and cannot
func (h *Handler) writeEmpty(w http.ResponseWriter, r *http.Request, page pageResult) {
	h.writeHeaders(w, http.StatusOK, page)

	if err := h.emptyDocument(r, campaigns.AccessFrom(r.Context()), page).
		Render(r.Context(), w); err != nil {
		h.log(r.Context(), slog.LevelError, renderFailedEvent,
			slog.String("error", err.Error()))
	}
}

// writeResults renders the result list, whose `<h1>` is the count (§7.5).
// `writeIdle` for why the document is built elsewhere.
//
// forward it; the render below is handed one explicitly.
//
//nolint:contextcheck // templ's generated closure takes a context and cannot
func (h *Handler) writeResults(w http.ResponseWriter, r *http.Request, page pageResult) {
	h.writeHeaders(w, http.StatusOK, page)

	if err := h.resultsDocument(r, campaigns.AccessFrom(r.Context()), page).
		Render(r.Context(), w); err != nil {
		h.log(r.Context(), slog.LevelError, renderFailedEvent,
			slog.String("error", err.Error()))
	}
}

// writeFailure sends the load-error state with the classified status.
//
// The designed failure surface rather than a bare status page, and the same one
// `wiki` uses: the reader got here authenticated — they passed a gate — so "not
// found" from `net/http` is plain text with no shell around it and no reference in it.
// The request id is the whole content of the state and it comes from the context
// rather than from the header, so a reader who sent their own `X-Request-Id` gets one
// minted here.
//
// **The query is not echoed here.** It is attacker-controlled and this is the response
// a scanner triggers; §4.7's closing rule — no raw echo of the caller's input on an
// error page — is the rule the 404 state applies to a path, for the same reason. The
// heading is `pageName` for the same reason the document title is.
func (h *Handler) writeFailure(w http.ResponseWriter, r *http.Request, failure error) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)
	status := statusFor(failure)

	h.writeHeaders(w, status, pageResult{cacheControl: noStore})

	if err := h.failureDocument(r, access).Render(ctx, w); err != nil {
		h.log(ctx, slog.LevelError, renderFailedEvent, slog.String("error", err.Error()))
	}

	h.logFailure(ctx, access.Campaign.Slug, status, failure)
}

// writeHeaders writes what every document on this route carries, and commits the
// status.
//
// `Vary: Cookie` is written on top of whatever `Cache-Control` says, and it is
// required rather than incidental: the shell carries the reader's name and a sign-out
// form, so two 200 responses to one URL differ, and a shared cache that keyed only on
// the URL would hand one reader another's header. ADR 0035 records the correction —
// an earlier claim that no `Vary` is emitted anywhere was false, and byte-identity
// across five cookie values is the substantive check while header-absence only
// catches today's shape.
//
// `Cache-Control` is `no-store` on a failure, because there is no variant worth
// keeping: a pinned error outlives the request that produced it, and a GM whose index
// read failed should not be shown the same failure from a cache after the index
// recovered. That is the same direction as the access gates' own answers
// (`private, no-store` on every 404/401/403) and for the same reason — every one of
// them is reader-dependent. `page.validator` is empty on that path, and an empty
// `ETag` is what RFC 9110 §8.8.3 means by "no validator applies": set to the empty
// string rather than omitted, which would be a claim that there is a representation
// here to validate.
func (h *Handler) writeHeaders(w http.ResponseWriter, status int, page pageResult) {
	w.Header().Set(contentTypeHeader, contentType)
	w.Header().Set(varyHeader, cookieVary)
	w.Header().Set(etagHeader, page.validator)
	w.Header().Set(cacheControlHeader, page.cacheControl)

	w.WriteHeader(status)
}

// renderFailed is the event name a body's render failure is logged under.
//
// A constant because four writers render a body and `goconst` is right that four
// copies of one event name are four places a rename would half-apply — and an event
// name is a query somebody runs.
const renderFailedEvent = "search.render_failed"

// idleDocument builds the idle state's document.
//
// Takes the request rather than a context because it holds none of its own — see
// `writeIdle` — and it is a method because the chrome it fills in is the handler's.
func (h *Handler) idleDocument(
	r *http.Request,
	access campaigns.Access,
	page pageResult,
) templ.Component {
	return IdlePage(IdleView{
		Shell:  h.shell(r, access),
		Action: searchAction(access.Campaign.Slug),
		// The reader's own text, so a submitted-and-refined reader is not asked to type
		// it again.
		Query: page.echo,
	})
}

// emptyDocument builds the no-results state's document. `idleDocument` for the shape.
func (h *Handler) emptyDocument(
	r *http.Request,
	access campaigns.Access,
	page pageResult,
) templ.Component {
	return EmptyPage(EmptyView{
		Shell:  h.shell(r, access),
		Action: searchAction(access.Campaign.Slug),
		Query:  page.echo,
	})
}

// resultsDocument builds the result list's document. `idleDocument` for the shape.
func (h *Handler) resultsDocument(
	r *http.Request,
	access campaigns.Access,
	page pageResult,
) templ.Component {
	return ResultsPage(ResultsView{
		Shell:     h.shell(r, access),
		Action:    searchAction(access.Campaign.Slug),
		Query:     page.echo,
		Hits:      page.hits,
		Truncated: page.truncated,
		Cap:       resultCap,
	})
}

// failureDocument builds the load-error state's document. `idleDocument` for the
// shape.
//
// forward it; the render above is handed one explicitly.
//
//nolint:contextcheck // templ's generated closure takes a context and cannot
func (h *Handler) failureDocument(
	r *http.Request,
	access campaigns.Access,
) templ.Component {
	return FailurePage(FailureView{
		Shell:     h.shell(r, access),
		Reference: middleware.MustRequestID(r.Context()),
	})
}

// shell builds the chrome for one document on this route.
//
// Taken by value so a caller's view model is never written through, and the title
// is composed here rather than by the caller so that UI §7.2's "Page — Section —
// Campaign" has exactly one answer in this package. The campaign falls back to its
// slug when it has no name, the same fallback `wiki` and `components` make: a
// campaign registered without a name still has a URL, and a blank slot in a
// document title is the first thing a new install looks like.
func (h *Handler) shell(r *http.Request, access campaigns.Access) components.ShellView {
	name := access.Campaign.Name
	if name == "" {
		name = access.Campaign.Slug
	}

	return components.ShellView{
		Title:       pageName + titleSeparator + name,
		Instance:    h.Instance,
		Account:     components.AccountView{Username: campaigns.Requestor(r.Context()).Username},
		SignOutHref: h.SignOutHref,
	}
}

// resultState is which of the route's three success documents was decided.
//
// A named type rather than three booleans because "no hits" and "no query" are
// different answers, and a pair of booleans can express a third combination that
// means neither.
type resultState int

const (
	// stateIdle is §4.7's "search, no query": the field, focused, with a hint.
	stateIdle resultState = iota
	// stateResults is a non-empty result list, whose `<h1>` carries the count.
	stateResults
	// stateEmpty is §4.7's "search, no results": "No results for *q*", echoed.
	stateEmpty
)

// pageResult is one request's decided response, before any of it reaches a
// template.
//
// A struct rather than four return values because `read` also returns an error, and
// a five-value signature is a signature whose fields get transposed. The name
// rather than `result` because a variable called `result` in this file would shadow
// the type.
type pageResult struct {
	// state is which document to render.
	state resultState
	// echo is the bounded query, and it is the only query-derived value on the page:
	// the `<h1>`'s term, the no-results state's heading and the form's field all
	// read this one field, so "what may be shown" is decided once.
	echo string
	// hits are the rows to list, converted for the template, and count is how many
	// there are — which is what the `<h1>` says. They are counted *after*
	// conversion, so the number in the heading is the number of links rendered.
	hits  []ResultView
	count int
	// truncated reports that count reached the cap, so the page says so rather than
	// implying it listed everything.
	truncated bool
	// validator is the ETag and cacheControl what the response carries. Both are set
	// together in `answer`, so a document cannot carry one without the other.
	validator    string
	cacheControl string
	// revalidated reports that the client's `If-None-Match` already named this
	// document, so the answer is a 304 and the body is not sent.
	revalidated bool
}

// errNoCampaignScope is why a request arrived with no campaign on its context.
//
// A named sentinel rather than an error built at the call site, because it is a
// fact about the process's wiring rather than about the request, and a log line
// that names it is one an operator can grep for.
var errNoCampaignScope = errors.New("search: no campaign was resolved for this request")

// refusalError is a classified failure: an error and the status it is answered
// with.
//
// A type rather than a status returned alongside an error so that the classification
// travels with the failure instead of with the caller: a handler that forgets to
// classify an error gets a 500, which is the safe direction, and one that
// classifies twice cannot — there is one function to change.
type refusalError struct {
	status int
	err    error
}

// Error returns the underlying failure's message.
func (e refusalError) Error() string {
	return e.err.Error()
}

// Unwrap keeps `errors.Is` reaching the sentinel, so a caller or a test can ask
// what actually happened rather than only how it was answered.
func (e refusalError) Unwrap() error {
	return e.err
}

// statusFor answers a classified failure, and 500 for anything unclassified.
func statusFor(err error) int {
	if refusal, found := errors.AsType[refusalError](err); found {
		return refusal.status
	}

	return http.StatusInternalServerError
}

// searchScope returns the campaign this request is scoped to, and whether one was
// resolved at all.
//
// It is a function rather than a field read at the call site because the zero value
// of that field is a *meaningful and dangerous* value in `store.PageSearch`: it
// means every campaign the requestor may read, and for an anonymous requestor that
// is every public campaign on the instance. A caller that passed
// `access.Campaign.ID` directly would compile whether or not the gate ran, and the
// failure would be a private title in a stranger's results (S-8.2, S-14.3).
//
// The tier is checked too, because `TierNone` is the gate's own answer for "no
// access" and a route that reached its handler with it has been mounted without
// `RequireRead`.
func searchScope(access campaigns.Access) (domain.Campaign, bool) {
	if access.Tier == domain.TierNone || access.Campaign.ID <= 0 {
		return domain.Campaign{}, false
	}

	return access.Campaign, true
}

// searchAction is the URL every form on this route submits to.
//
// No escaping of the slug, and the reason is `domain.ValidateSlug`: a slug is 1..64
// bytes of lowercase ASCII, digits and single hyphens, so there is nothing in one
// that `url.PathEscape` would change. A campaign that reached the gate with another
// slug would have failed `ValidateSlug` at registration.
func searchAction(slug string) string {
	return campaignPathPrefix + slug + "/" + searchPathSegment
}

// resultViews converts the store's rows into the template's rows.
//
// Two conversions happen here rather than in the template, and both are refusals
// rather than formatting: the extension is stripped because a URL does not carry one
// (S-9), and the link is built with `content.WikiHref`, whose per-segment escaping is
// the reason a link to a page in a folder works at all.
//
// A row that produces no href is dropped rather than rendered unlinked, because
// `WikiHref` returns "" for a path it refuses and a result row with no link is a dead
// end wearing a link's clothes. The count in the `<h1>` is the number of rows
// *rendered*, which is the only count that can be true — see `read`.
func resultViews(slug string, hits []domain.SearchHit) []ResultView {
	rows := make([]ResultView, 0, len(hits))

	for index := range hits {
		hit := &hits[index]

		pagePath := strings.TrimSuffix(hit.Path, pageExtension)

		href := content.WikiHref(slug, pagePath)
		if href == "" {
			continue
		}

		// The title falls back to the path's base name rather than to the path itself:
		// a page with no front-matter `title:` still has a name, and the page route
		// names one the same way (`wiki.pageName`).
		//
		// **A title is carried verbatim, and nothing here filters it.** ADR 0036:
		// `pages.title` is indexed unredacted because a title is one line of front
		// matter with no callout structure and therefore no boundary to redact to, and
		// a result list is one of the three surfaces it reaches (with the nav tree and
		// the page's own `<h1>`). The consequence is a rule for authors rather than a
		// rule for this route — a secret in a `title:` is a secret in a search result —
		// and the alternative, guessing which words are secret, breaks real titles
		// while missing real leaks. `TestAResultCarriesTheTitleVerbatim` pins the
		// decision so that a later change filtering titles has to argue with the record
		// rather than with a test nobody wrote on purpose.
		//
		// The *snippet* is the opposite and is redacted: it is FTS5's excerpt of
		// `body_plain`, which excludes `[!secret]` callout content in every reveal
		// state (S-5.11, UI §4.10.5), so no view of a result list carries secret text.
		title := hit.Title
		if title == "" {
			title = baseName(pagePath)
		}

		rows = append(rows, ResultView{
			Href:    href,
			Title:   title,
			Path:    pagePath,
			Snippet: hit.Snippet,
		})
	}

	return rows
}

// countLabel is the count half of §7.5's "12 results for *goblin*".
//
// A function rather than an inline expression because a plural needs a condition,
// and a condition written inside a template is one more place for the two branches
// to disagree about spacing. The count is of *rendered* rows rather than of rows the
// store returned, because `resultViews` drops a row it cannot link and a heading
// counting a row the page does not show would be the larger lie.
func countLabel(rows []ResultView) string {
	if len(rows) == 1 {
		return "1 result"
	}

	return strconv.Itoa(len(rows)) + " results"
}

// baseName is a page path without its directories, which is what a page with no
// front-matter title calls itself.
//
// `strings.CutLast` rather than `path.Base`, because `path.Base(".")` is `"."` and
// the empty path is already refused upstream; a page path here is always
// root-relative and non-empty.
func baseName(pagePath string) string {
	if _, base, found := strings.CutLast(pagePath, "/"); found {
		return base
	}

	return pagePath
}

// boundQuery bounds a submitted query for display, on a rune boundary.
//
// The same contract as `components/ui`'s unexported `displayText`, restated because
// a template cannot be relied on to have applied it and this route's `<h1>` is its
// own component rather than one of the §4.7 states.
//
// Three properties, each for a reason:
//
//   - `ToValidUTF8` first, so a query carrying `%FF` cannot put a lone invalid byte
//     into a rendered page. Some engines show U+FFFD for it and others show nothing,
//     so the same bytes would read differently in two browsers.
//   - The cut walks back to a rune boundary, for the same reason.
//   - The bound counts the ellipsis, so the value is *inside* `ui`'s bound when
//     handed to a state that applies its own. A 200-byte echo plus a three-byte
//     ellipsis would be 203 bytes, over `ui`'s limit, and the state would trim it
//     again — losing a character of the reader's own query to a second bound that
//     exists for the same safety reason.
func boundQuery(value string) string {
	value = strings.ToValidUTF8(value, "")

	// Control characters are removed rather than escaped or kept.
	//
	// A C0 control or DEL in a rendered value is a filter-bypass vector — a log line,
	// a filename or an HTML attribute value that a downstream reader truncates at the
	// control byte sees a different string from the one here — and no search box
	// produces one: a browser's input element will not accept a newline, and
	// `strings.Fields` in the store treats them as term separators anyway. So they
	// cannot be a term and they cannot be displayable.
	if stripped := strings.Map(dropControl, value); stripped != value {
		value = stripped
	}

	if len(value) <= maxQueryDisplay {
		return value
	}

	cut := maxQueryDisplay - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}

	return value[:cut] + ellipsis
}

// dropControl removes the C0 controls and DEL, and keeps everything else.
//
// A function rather than an inline predicate because `strings.Map` takes one and the
// predicate is the whole of what is being decided; the identity case is what keeps
// `strings.Map` from allocating for the ordinary query.
func dropControl(character rune) rune {
	if character < 0x20 || character == 0x7f {
		return -1
	}

	return character
}

// fingerprint is the hex SHA-256 over everything in the document that is not the
// chrome: the campaign it was rendered for, the echoed query, and every row.
//
// The rows rather than the query, and that is S-5.2's argument restated for a
// document with no file behind it: validity is a function of content, and the
// content of a result list is the rows. A fingerprint over the query alone would not
// move when a page was edited, so every result list in the instance would keep its
// validator across an edit that changed what it lists.
//
// The campaign slug is in it, which `content.CacheKey.ETag`'s comment explains as
// unnecessary for a page — two URLs carrying one validator confuse nothing, because
// a cache stores by URL. Here it is not unnecessary: the campaign's name is *in* the
// document, so two campaigns' result lists can be byte-different under one
// validator.
//
// Length-prefixed fields, because a bare concatenation is ambiguous: `("ab","c")` and
// `("a","bc")` produce the same bytes, so two different result lists could share a
// validator and a cache would serve one for the other.
func fingerprint(slug, query string, hits []domain.SearchHit) string {
	preimage := make([]byte, 0, 256)

	preimage = appendField(preimage, slug)
	preimage = appendField(preimage, query)
	preimage = appendField(preimage, strconv.Itoa(len(hits)))

	for index := range hits {
		hit := &hits[index]

		preimage = appendField(preimage, strconv.FormatInt(hit.ID, 10))
		preimage = appendField(preimage, hit.Path)
		preimage = appendField(preimage, hit.Title)
		preimage = appendField(preimage, hit.Snippet)
	}

	digest := sha256.Sum256(preimage)

	return hex.EncodeToString(digest[:])
}

// appendField appends one length-prefixed field.
func appendField(dst []byte, value string) []byte {
	dst = strconv.AppendInt(dst, int64(len(value)), 10)
	dst = append(dst, ':')

	return append(dst, value...)
}

// revalidated reports whether an `If-None-Match` header names this validator.
//
// A list, because a client may send several — after a merge, or from a tab holding
// an old copy — and it must match any of them. `*` matches any representation,
// which is what RFC 9110 §13.1.2 says. Compared weakly, because a validator for a
// `GET` is compared with weak comparison, so `W/"x"` and `"x"` are the same answer.
//
// An empty header is not a match, and neither is an empty validator: a response with
// no validator must not be revalidated against nothing, which would answer 304 for
// every request a client made with no header at all.
//
// **This is a second copy of `wiki`'s function**, which is unexported there and
// unreachable from here. It is twenty lines of RFC matching with one right answer,
// and the third copy arrives with the assets route; the shared helper belongs in
// `internal/httpapi` beside `securityHeaders`, and moving it there is the
// integrator's change rather than this work item's.
func revalidated(header, validator string) bool {
	if header == "" || validator == "" {
		return false
	}

	for candidate := range strings.SplitSeq(header, ",") {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == anyValidator {
			return true
		}

		if strings.TrimPrefix(trimmed, weakPrefix) == strings.TrimPrefix(validator, weakPrefix) {
			return true
		}
	}

	return false
}

// writeNotModified answers a revalidation.
//
// The headers and no body. RFC 9110 §15.4.5: a 304 carries the metadata the client
// needs to update its stored copy, and a body is a protocol error.
func writeNotModified(w http.ResponseWriter, page pageResult) {
	w.Header().Set(varyHeader, cookieVary)
	w.Header().Set(etagHeader, page.validator)
	w.Header().Set(cacheControlHeader, page.cacheControl)

	w.WriteHeader(http.StatusNotModified)
}

// logFailure records a classified failure at a level that matches what it means.
//
// A 500 is the operator's: something is wrong that a request cannot fix. A 400 and a
// 404 are between the two — the access log already carries the status for every
// request — so they are recorded without being escalated.
//
// The error's text is logged and the query is not passed alongside it, and that is
// not an oversight: `store`'s invalid-query error quotes the query it refused, so
// logging the error logs up to a kilobyte of attacker-chosen bytes, and AGENTS.md's
// rule against carrying caller input into a log line is a rule about exactly this.
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

	h.log(ctx, level, "search.query_failed", attrs...)
}

// log writes one line, discarding it when no logger is configured.
//
// The context is threaded rather than replaced, for the reason `wiki`'s gives: a log
// line that cannot be correlated with the request it describes is the failure slog
// exists to prevent.
func (h *Handler) log(ctx context.Context, level slog.Level, event string, attrs ...slog.Attr) {
	if h.Logger == nil {
		return
	}

	h.Logger.LogAttrs(ctx, level, "search",
		append([]slog.Attr{slog.String("event", event)}, attrs...)...)
}
