// Package wiki serves a campaign's pages: `GET /c/{slug}/wiki/{path...}`.
//
// One route, and it is the first place in the request path where a reader, a
// campaign and a file on disk meet. Everything it does is therefore one of three
// things, and the split between them is the design:
//
//   - **Decide what the viewer may see.** One value, `includeSecrets`, derived
//     from the tier the access gate resolved and from nothing else — not a query
//     parameter, not a cookie, not a header. It selects the cache key's variant
//     and it selects what the redactor is asked to keep.
//   - **Produce the body in the one order that is safe.** Read the bytes through
//     the campaign's confined root, hash them, look the key up, and on a miss
//     redact the source, parse it, render it and sanitise it in that order — an
//     order `pipeline.go` makes structural rather than conventional.
//   - **Answer.** An `ETag` and `Cache-Control` derived from the key, an
//     `If-None-Match` revalidation, and a missing page as a designed state rather
//     than as net/http's plain text.
//
// The route is mounted behind `campaigns.RequireRead`, which is where the S-8
// matrix lives (ADR 0024). Nothing here re-checks it: a handler that also asked
// whether the reader may read the campaign would be a second copy of the matrix,
// and the copy that nobody reviews is the one that eventually answers 403 where
// the first answered 404 — turning a private campaign's existence into a fact a
// stranger can learn one status code at a time.
package wiki

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/web/components"
)

// pageExtension is the suffix a page's path gains on its way in from a URL.
//
// A URL carries no extension: `content.WikiHref` writes
// `/c/{slug}/wiki/Some%20Page` rather than `Some%20Page.md`, so that a link an
// author reads aloud and one a browser follows are the same string. The extension
// is put back here, once, and `content`'s own `pageExtension` — which strips it
// from a reference — is unexported and owned by the link layer's grammar.
const pageExtension = ".md"

// titleSeparator joins the parts of a document title, in UI §7.2's form
// "Page — Section — Campaign".
//
// Spelled here rather than reusing `components`' because that package's own
// helper is unexported and composes the *pre-campaign* form, whose third part
// names the instance. P5 unifies the two when it restyles the shell; until then
// this route would otherwise render a document title of just the page's name.
const titleSeparator = " — "

// The header names this route writes. `net/http` has no constants for header
// names, and a literal repeated across the response writers is one a rename could
// half-apply.
const (
	etagHeader         = "ETag"
	cacheControlHeader = "Cache-Control"
	varyHeader         = "Vary"
	// cookieVary is the value of `Vary`. See writePage for why it is there.
	cookieVary = "Cookie"
	// ifNoneMatchHeader carries the client's validator on a revalidation.
	ifNoneMatchHeader = "If-None-Match"
	// weakPrefix marks a validator as weak (RFC 9110 §8.8.3). Compared away in
	// both directions, because a revalidation against `If-None-Match` uses weak
	// comparison and a client that dropped the `W/` has still named the same body.
	weakPrefix = "W/"
	// anyValidator is `If-None-Match: *`, which matches any existing
	// representation.
	anyValidator = "*"
)

// contentType is what a page is served as. The shell is a complete HTML document,
// not a fragment, so this is the document type rather than the type of a partial.
const contentType = "text/html; charset=utf-8"

// refusedHeading is what a page whose path was refused calls itself.
//
// A fixed string, and never the requested path: `content.Root` refuses to put the
// offending path in its error message because an error whose text varies with
// the caller's input is a channel, and a heading is the largest text on the page.
// "Page" is the word the interface uses for the thing (UI §1.2), so a reader is
// told what could not be opened without being told what was asked for.
const refusedHeading = "Page"

// RootLookup returns a campaign's confined content root.
//
// `*content.Registry` satisfies it as it stands. Narrower than the registry on
// purpose: a handler that can enumerate every campaign's root is a handler that
// can be handed a slug it was never authorised for, and the only slug it is ever
// given is the one the access gate resolved.
type RootLookup interface {
	Get(slug string) (*content.Root, error)
}

// Pages lists a campaign's pages, for link resolution.
//
// Asked on a cache miss and only on one, so a hit costs no query: the addresses
// depend on the listing, and the listing is not part of the cache key, which is
// why the addresses are recomputed per response rather than cached with the body.
// That is the correct direction — a cached address would keep pointing at the page
// a name resolved to when the entry was written — and it is why P4 can replace
// this with the watcher's index without changing anything above it.
type Pages interface {
	PagesForCampaign(ctx context.Context, campaignID int64) ([]domain.Page, error)
}

// Renderer renders one campaign's pages.
//
// `*content.Renderer` satisfies it. An interface so a test can hand the pipeline
// a renderer that records what it was given — which is the only way to assert that
// redaction happened *before* the render rather than after it, as opposed to
// asserting that a particular string is missing from a particular response.
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
// A map type rather than a constructor so the composition root owns the contents
// and this package owns none of them: there is no registration here, and no way
// for a package that imports `wiki` to add a renderer behind the root's back.
type CampaignRenderers map[string]*content.Renderer

// Renderer returns the renderer for slug, or an error naming the slug when there
// is none.
//
// The error is an operator's problem rather than a reader's: a campaign row
// without a renderer is a wiring fault in the composition root, and the route
// answers it as a load failure with a request id so the reader's screenshot and
// the log line are the same request.
func (renderers CampaignRenderers) Renderer(slug string) (Renderer, error) {
	renderer, present := renderers[slug]
	if !present {
		return nil, fmt.Errorf("wiki: no renderer for campaign %s", slug)
	}

	return renderer, nil
}

// Handler serves the campaign page route.
//
// Exported fields rather than a constructor with nine parameters, matching
// `accounts.Router`: the composition root writes one literal and a test writes
// another, and a struct literal names what it sets. Every field is written once
// before the server starts and read on every request, so a `Handler` is safe for
// concurrent use — which is the same claim `content.Renderer` makes, and rests on
// the same thing: no field is written after construction.
type Handler struct {
	// Roots is how a campaign's content root is found, and the confinement
	// boundary every path this route reads goes through. Required.
	Roots RootLookup

	// Renderers is how a campaign's renderer is found. Required: a route with no
	// renderer has nothing to render with, and answering that with an empty page
	// would hide a wiring fault behind a 200.
	Renderers Renderers

	// Kinds is the page-kind registry `content.Parse` discriminates `kind`
	// against, and it may be nil, in which case every page is prose.
	Kinds domain.PageKindRegistry

	// Pages lists a campaign's pages so references resolve. Required, and asked
	// on a cache miss only.
	Pages Pages

	// Redactor removes content the viewer may not see, and is the seam the
	// pipeline is ordered around. Required. This phase installs
	// `content.NoSecrets()`, which removes nothing and is replaced by P10.
	Redactor content.Redactor

	// Cache is the render cache, and it is the concrete `*content.Cache` rather
	// than an interface over it.
	//
	// The concrete type is the right answer for three reasons, and each of them
	// would be a reason the other way round if the cache were not already written.
	// `CacheKey` is this route's four cache-key inputs and *is* the type the
	// cache is keyed by, so an interface here would mean a second struct with the
	// same four fields and an adapter between them — and a key that the cache
	// derives a validator from must be the same key the route looked up, or a
	// future change could salt one and not the other. `CacheKey.ETag` and
	// `CacheControl` are the only two derivations of these headers in the project,
	// and this route calls both rather than computing either: two answers to "what
	// is this page's validator" is how a GM's unredacted response ends up
	// advertising the player's validator. And `Cache.Render` is the single-flight
	// seam, so a burst of requests for one uncached page runs one render because
	// the cache says so rather than because this route implements it.
	//
	// Required. `content.NewCache(0)` is the documented instance that stores
	// nothing and renders every time, which is the configuration a test wants and
	// the shape a misconfigured bound takes.
	Cache *content.Cache

	// Logger receives this route's lines. Nil is allowed and discards them, so a
	// test does not have to construct a logger to serve a request.
	Logger *slog.Logger

	// Instance names the running instance and reports what is unhealthy, for the
	// shell's chrome. A value rather than a lookup so a response does not depend
	// on when it was written.
	Instance components.InstanceView

	// SignOutHref is where the header's sign-out form posts. Empty renders no
	// form, which is the right answer on an instance with no such route.
	SignOutHref string
}

// Mount registers the page route on mux.
//
// mux is the campaign-scoped mux — the one `mountCampaignRoutes` fills — and the
// pattern carries the `/c/` prefix even though that mux is itself mounted there.
// A Go 1.22 mux routes on a prefix and then hands the *whole* path to what it
// matched, so a pattern written without the prefix would never fire.
func Mount(mux *http.ServeMux, handler *Handler) {
	mux.Handle("GET /c/{slug}/wiki/{path...}", handler)
}

// ServeHTTP answers one page request.
//
// Linear, and every branch leaves through one of the three writers at the bottom:
// a 200, a 304, or a failure. The order is the pipeline's, and the comments on
// the calls are the order — a step that moves is a step whose comment no longer
// matches it.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	page, err := h.read(
		ctx,
		campaigns.AccessFrom(ctx),
		r.PathValue("slug"),
		r.PathValue("path"),
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

	h.writePage(w, r, page)
}

// read is the whole pipeline for one request, in the order S-5.2 and S-5.7
// require, and it returns either a page to serve or an error carrying the status
// the failure is answered with.
//
// Every input is passed in rather than read from the request: a pipeline that took
// the `*http.Request` would have whatever the next change needs one header away,
// and the two things this one must never read from a request — the campaign's
// access and whether the viewer may see secrets — are the first two parameters so
// that they are visibly not among the things it can reach.
//
// Returning a value rather than writing to the response keeps the order readable:
// there is no early `w.WriteHeader` in the middle of it to mistake for the end of
// the pipeline, and no path that writes a body without first having built the key
// whose `ETag` goes on it.
func (h *Handler) read(
	ctx context.Context,
	access campaigns.Access,
	slug string,
	urlPath string,
	ifNoneMatch string,
) (pageResult, error) {
	// includeSecrets first, and before any bytes are read. It is the one value
	// that decides whether text the reader may not see is in the response, and it
	// comes from the tier the access gate resolved and from nothing else — not a
	// query parameter, not a cookie, not a header. Each of those is a way for a
	// reader to ask for text they do not have, and S-8's answer to that is that
	// roles come from a membership row.
	includeSecrets := access.Tier.CanEdit()

	root, renderer, err := h.forCampaign(slug)
	if err != nil {
		return pageResult{}, err
	}

	target, err := pageTarget(root, urlPath)
	if err != nil {
		return pageResult{}, err
	}

	// Read through the confined root, and hash the bytes as they were read. The
	// order is read-then-hash rather than stat-then-read: S-5.2 makes validity a
	// fact about content, and a stat answers a question about the filesystem,
	// which a sync client is free to change without the file changing.
	src, err := h.readSource(ctx, target)
	if err != nil {
		return pageResult{}, err
	}

	// The key, in S-5.2's four fields and built from the bytes that were just
	// read. `CampaignID` is the id the gate resolved rather than the slug, so no
	// entry can be shared across campaigns (S-8.3); `Path` is `Target.Path()`, so
	// it is root-relative by construction and a bug above cannot widen it;
	// `ContentHash` is the sha256 of those bytes; and `IncludeSecrets` is the one
	// viewer-dependent field, which is what makes a GM's body and a player's two
	// entries rather than one, and what salts the validator below.
	key := content.CacheKey{
		CampaignID:     access.Campaign.ID,
		Path:           target.Path(),
		ContentHash:    src.hash,
		IncludeSecrets: includeSecrets,
	}

	validator, directives := key.ETag(), content.CacheControl(key)

	// Revalidated before the cache is consulted, because a 304 needs the key's
	// validator and no body, and asking the cache for a body that will not be sent
	// is a lookup a client could be made to pay for.
	if revalidated(ifNoneMatch, validator) {
		return pageResult{
			revalidated:  true,
			etag:         validator,
			cacheControl: directives,
		}, nil
	}

	// `Cache.Render` rather than a `Get`-then-`Put`: it is the cache's own
	// single-flight, so N concurrent requests for one uncached page cause one
	// render rather than N, and no request waits behind a mutex held across
	// somebody else's goldmark pass. Nothing here can reimplement that
	// incorrectly, because nothing here implements it.
	entry, err := h.Cache.Render(key, func() (content.Entry, error) {
		rendered, renderErr := h.render(src, renderer, includeSecrets)
		if renderErr != nil {
			return content.Entry{}, renderErr
		}

		return content.Entry{Rendered: rendered}, nil
	})
	if err != nil {
		return pageResult{}, fmt.Errorf("page render: %w", err)
	}

	// Outside the cache, and it has to be: the addresses are a function of the
	// campaign's page listing rather than of this page's bytes, so they are not
	// part of the key. Caching them would keep every reader pointed at whatever a
	// name resolved to when the entry was written. See addresses.
	body, err := h.addresses(ctx, access.Campaign.ID, root, target.Path(), entry.Rendered)
	if err != nil {
		return pageResult{}, err
	}

	return pageResult{
		title:        pageName(target.Path()),
		body:         body,
		etag:         validator,
		cacheControl: directives,
	}, nil
}

// forCampaign resolves the two per-campaign dependencies a page read needs.
func (h *Handler) forCampaign(slug string) (*content.Root, Renderer, error) {
	root, err := h.Roots.Get(slug)
	if err != nil {
		return nil, nil, fmt.Errorf("content root for %s: %w", slug, err)
	}

	renderer, err := h.Renderers.Renderer(slug)
	if err != nil {
		return nil, nil, fmt.Errorf("renderer for %s: %w", slug, err)
	}

	return root, renderer, nil
}

// render produces the body for one cache variant.
//
// Four calls, and the order is S-5.7: redact, parse, render, sanitise. The first
// is a method on `source` and the second on `redacted`, so the order is also the
// type graph — see pipeline.go — and the sanitiser runs inside `Render`, which is
// where it belongs because it is the last thing that touches author-supplied HTML
// and a body that had already left the render would be markup no layer had seen.
//
// Every failure is a 500. A page that will not parse does not exist as far as the
// reader is concerned, and `content.Parse` being total means this is reached only
// for a renderer that failed or a redactor that did.
func (h *Handler) render(
	src source,
	renderer Renderer,
	includeSecrets bool,
) (content.Rendered, error) {
	body, err := src.redact(h.Redactor, includeSecrets)
	if err != nil {
		return content.Rendered{}, fmt.Errorf("redact: %w", err)
	}

	rendered, err := renderer.Render(body.parse(h.Kinds))
	if err != nil {
		return content.Rendered{}, fmt.Errorf("render: %w", err)
	}

	return rendered, nil
}

// addresses resolves the page's references and writes the answers into the
// elements the renderer left empty.
//
// Outside the cache on purpose. The addresses are a function of the campaign's
// page listing rather than of the page's own bytes, so they are not part of the
// key and must not be cached with the body: a cached address would keep pointing
// at whatever a name resolved to when the entry was written, and would do so for
// every reader until the content hash changed. Recomputing them is cheap next to
// the render they replace, and it is the only way ADR 0017's byte-identity holds
// when the listing moves underneath a cached body.
func (h *Handler) addresses(
	ctx context.Context,
	campaignID int64,
	root *content.Root,
	pagePath string,
	rendered content.Rendered,
) (string, error) {
	pages, err := h.Pages.PagesForCampaign(ctx, campaignID)
	if err != nil {
		return "", fmt.Errorf("list pages for campaign %d: %w", campaignID, err)
	}

	origin, err := content.NewOrigin(pagePath)
	if err != nil {
		return "", fmt.Errorf("reference origin for %s: %w", pagePath, err)
	}

	return attachAddresses(
		rendered.HTML,
		content.NewResolver(root, pages).Links(origin, rendered.References),
	), nil
}

// pageResult is one page's response, before any of it reaches a template.
//
// A struct rather than three return values because `read` already returns an
// error, and a four-value signature is a signature whose fields get transposed. The
// name rather than `page` because a variable called `page` in the same file would
// shadow the type.
type pageResult struct {
	// title is what the page calls itself: its base name. See pageName.
	title string
	// body is the sanitised HTML with the references' addresses written into it.
	// The only unescaped value this package produces, and the only value that
	// came out of a sanitiser.
	body string
	// etag and cacheControl are the key's validator and directives, taken from
	// `CacheKey.ETag` and `CacheControl` rather than computed here. Two derivations
	// of a validator in one process is a defect waiting for the one that changes.
	etag         string
	cacheControl string
	// revalidated reports that the client's `If-None-Match` already named this
	// variant, so the answer is a 304 and the body is not sent.
	revalidated bool
}

// pageTarget confines the page path a URL carried, and adds the extension the
// URL does not have.
//
// `Root.At` and not `filepath.Join`: a path from a request is untrusted, and the
// confinement is `os.Root`'s rather than a lexical normaliser's (S-3.5). The value
// is already unescaped by the mux — `PathValue` returns `%20` as a space — which
// also means an escaped `%2F` arrives as a separator, and is harmless for exactly
// the reason everything else here is: the path it forms is confined to the
// campaign's root before it is opened.
//
// A `..` reaches this function in its percent-encoded spelling. A literal one
// never does: `net/http` cleans the request path and answers with a redirect
// before a handler runs, and only an escaped one survives that, which is why the
// confinement below is load-bearing rather than a second opinion.
func pageTarget(root *content.Root, urlPath string) (*content.Target, error) {
	if urlPath == "" {
		// The route matched `/c/{slug}/wiki/` with nothing after it. A path that
		// names nothing is malformed rather than absent: appending the extension
		// would look for a file called `.md`, which is a page nobody can have
		// written and a 404 that says so for the wrong reason.
		return nil, refusalError{status: http.StatusBadRequest, err: content.ErrInvalidRef}
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

// readSource reads a page's bytes through the confined root and hashes them.
//
// A method rather than a free function only so that the close failure has a
// logger to reach; it needs none of the handler's other fields.
//
// `content.ReadDocument` rather than `Target.ReadFile`, and the difference is the
// size cap: applying it after the bytes are in memory bounds the parser but not
// the buffer, and the file came from a directory anything on the host can write
// to. `Target.Open` rather than `ReadFile` for the same reason — it is the only
// handle that lets the cap be applied while the bytes arrive.
//
// The close is checked rather than deferred and dropped: a read handle left open
// on a watcher-driven tree is a descriptor leak on a server that never restarts,
// and a handler is not the place to assume somebody else closed it.
func (h *Handler) readSource(ctx context.Context, target *content.Target) (source, error) {
	file, err := target.Open()
	if err != nil {
		return source{}, classify(err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			h.log(ctx, slog.LevelWarn, "wiki.page_close_failed",
				slog.String("path", target.Path()),
				slog.String("error", closeErr.Error()),
			)
		}
	}()

	data, err := content.ReadDocument(file)
	if err != nil {
		return source{}, classify(err)
	}

	return newSource(data), nil
}

// pageName is what a page calls itself in the interface: its base name, without
// the extension.
//
// Not the front matter's `title:`, and that is a deliberate refusal rather than an
// omission. `content.Reference.Label` is the author's alias or the author's
// target and never a title read from the target page — ADR 0017's rule, which is
// as much about the cache as about privacy, and which exists so that one page has
// one name everywhere it is referred to. Reading `title:` for this heading and
// not for the links would give a wiki two names for one page, and would put a
// value read out of the document into the document's own heading, which is the
// shape of the viewer-dependence S-5.1 rules out. P6's article header is where a
// title belongs.
func pageName(pagePath string) string {
	return strings.TrimSuffix(path.Base(pagePath), pageExtension)
}

// revalidated reports whether an `If-None-Match` header names this validator.
//
// A list, because a client may send several — after a merge, or from a tab holding
// an old copy — and it must match any of them to be revalidated. `*` matches any
// representation, which is what RFC 9110 §13.1.2 says, and which a client sends
// after a `PUT`. Compared weakly, because a validator for a `GET` is compared
// with weak comparison, so `W/"x"` and `"x"` are the same answer.
//
// An empty header is not a match, and neither is an empty validator: a response
// with no validator must not be revalidated against nothing, which would answer
// 304 for every request a client made with no header at all.
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

// writePage sends the page.
//
// The two cache headers are written from the key and nothing else touches them,
// and `Vary: Cookie` is written on top of whatever the cache decided. The document
// is reader-dependent even when its body is not: the shell carries the reader's
// name and a sign-out form, so a shared cache that keyed only on the URL would
// hand one reader another's header. `Vary` is the mechanism for saying so, and it
// is additive — a cache that already says `private` is unaffected by it, and one
// that says `public` has been told what it was missing.
func (h *Handler) writePage(w http.ResponseWriter, r *http.Request, page pageResult) {
	ctx := r.Context()
	requestor := campaigns.Requestor(ctx)
	access := campaigns.AccessFrom(ctx)

	view := components.WikiPage(components.WikiPageView{
		Shell: components.ShellView{
			Title:       documentTitle(page.title, access.Campaign),
			Instance:    h.Instance,
			Account:     components.AccountView{Username: requestor.Username},
			SignOutHref: h.SignOutHref,
		},
		Heading: page.title,
		Body:    page.body,
	})

	w.Header().Set("Content-Type", contentType)
	w.Header().Set(varyHeader, cookieVary)
	writeCacheHeaders(w, page.etag, page.cacheControl)

	w.WriteHeader(http.StatusOK)

	if err := view.Render(ctx, w); err != nil {
		// Logged and nothing else. The status line is committed and a body is
		// already on the wire, so there is no second answer available — and a page
		// that failed halfway has already told the reader more than an error page
		// would have.
		h.log(ctx, slog.LevelError, "wiki.page_render_failed",
			slog.String("campaign", access.Campaign.Slug),
			slog.String("error", err.Error()),
		)
	}
}

// writeNotModified answers a revalidation.
//
// The headers and no body. RFC 9110 §15.4.5: a 304 carries the metadata the
// client needs to update its stored copy, and a body is a protocol error.
func writeNotModified(w http.ResponseWriter, page pageResult) {
	writeCacheHeaders(w, page.etag, page.cacheControl)

	w.WriteHeader(http.StatusNotModified)
}

// writeCacheHeaders writes the two headers the key implies.
func writeCacheHeaders(w http.ResponseWriter, etag, cacheControl string) {
	w.Header().Set(etagHeader, etag)
	w.Header().Set(cacheControlHeader, cacheControl)
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
