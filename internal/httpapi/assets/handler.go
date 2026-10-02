// Package assets serves a campaign's files: `GET /c/{slug}/assets/{path...}`.
//
// The second campaign-scoped read route, and the one that reaches past the render
// pipeline entirely. A page goes through `content.Renderer` — redact, parse,
// render, sanitise — and a battle map does not, because a PNG is not markdown and
// the interesting question here is not what a file means but whether it may be
// read at all. So this package is almost entirely about refusals, and the design
// is arranged so that the order of its checks is the argument:
//
//  1. **Confinement first, and it is `os.Root`.** `content.Root.At` is the whole
//     boundary (S-3.5): never `filepath.Clean` plus a prefix check, which is wrong
//     for `..`, for an absolute path, for a symlink and for a case-insensitive
//     filesystem in four different ways. Everything after this point operates on
//     `Target.Path()`, which is root-relative by construction and cannot be
//     widened by anything above it.
//  2. **The name policies second.** A dotfile, a `node_modules` directory and a
//     `.tmp`/`.swp`/`~` name are refused by the same rules the index uses, so the
//     index and this route cannot disagree about what a campaign's content is.
//  3. **The extension third, from a closed table.** See media.go. The answer is
//     never `http.DetectContentType` on attacker-controlled bytes, and nothing is
//     ever served as `text/html`.
//  4. **Existence, and only then the bytes.** `Stat` before `Open`, so a directory
//     and a device node are refused rather than served.
//
// # Range
//
// Maps are tens of megabytes (architecture §16.2) and a VTT needs to seek, so this
// route delegates the range algebra to `http.ServeContent`, which is the standard
// library's own answer and handles the single, open-ended, suffix, multi-range and
// unsatisfiable cases along with `If-Range` and `If-None-Match`. Delegating is not
// a shortcut: a second range parser in this repository would be a second answer to
// every edge case in RFC 9110 §14.2, and the one that would be wrong is the one
// nobody tests.
//
// What this package adds around it is everything `ServeContent` cannot know: that
// the handle it is handed came out of a confined root, that the media type came
// from a table rather than from the bytes, and that the validator names a
// campaign.
//
// # Caching
//
// S-8.3 — assets inherit campaign visibility, and a cache entry is never shared
// across campaigns. The route holds no server-side cache at all, so there is
// nothing for a request to be served out of by accident; what stands in for it is
// the `ETag`, which is salted with the campaign id, and a `Cache-Control` that no
// shared cache may store. See cacheControlAssets and assetValidator.
package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/web/components"
)

// The header names this route writes. `net/http` has no constants for header
// names, and a literal repeated across two response writers is one a rename could
// half-apply.
const (
	acceptRangesHeader  = "Accept-Ranges"
	dispositionHeader   = "Content-Disposition"
	contentTypeHeader   = "Content-Type"
	contentPolicyHeader = "Content-Security-Policy"
	cacheControlHeader  = "Cache-Control"
	etagHeader          = "ETag"
	nosniffHeader       = "X-Content-Type-Options"
	varyHeader          = "Vary"
)

const (
	// nosniffValue forbids content-type sniffing.
	nosniffValue = "nosniff"

	// cookieVary is the `Vary` value. See writeAsset for why an asset response
	// carries it when the asset's bytes do not vary by reader.
	cookieVary = "Cookie"

	// acceptRangesBytes is the only range unit this route supports, and it is the
	// unit `ServeContent` implements.
	acceptRangesBytes = "bytes"

	// weakValidatorPrefix opens a weak validator (RFC 9110 §8.8.3).
	weakValidatorPrefix = `W/"`

	// validatorPreimageSep joins the validator's inputs. A separator rather than
	// concatenation, for the reason `content/cache.go` gives: concatenation makes
	// the encoding ambiguous, and a collision in a validator is a cache that
	// serves the wrong bytes.
	validatorPreimageSep = "\x00"

	// refusedHeading is what a failed asset calls itself.
	//
	// A fixed string on every failure, including the 404s, and never the
	// requested path. `content.Root` refuses to put the offending value in its
	// error message because an error whose text varies with the caller's input is
	// a channel, and a heading is the largest text on the page. The wiki route
	// echoes a *page's* name, because a mistyped wikilink is worth naming; an
	// asset path is not, because a reader who mistyped one is looking at a broken
	// image and lands on the same state either way.
	refusedHeading = "Asset"

	// documentContentType is what a failure state is served as. The shell is a
	// complete HTML document rather than a fragment, so this is the document type
	// — and it appears only on responses this route composed, never on a response
	// carrying a file's bytes. That distinction is the load-bearing one: see
	// media.go for why nothing under `/assets/` is ever `text/html`.
	documentContentType = "text/html; charset=utf-8"

	// noStore is the `Cache-Control` a failure response carries. There is no
	// validator for a failure, and a pinned 404 outlives the file that caused it:
	// the reader who adds that file is told it does not exist until they clear
	// their cache.
	noStore = "no-store"

	// titleSeparator joins the parts of a document title, in UI §7.2's form.
	//
	// Spelled here because `components`' own joiner is unexported, exactly as in
	// the wiki route. Two copies of one separator is a known duplication of this
	// phase and belongs in `components` the moment anything needs a third.
	titleSeparator = " — "

	// cacheControlAssets is UI §6.6's row for this route, with `private` applied
	// to every campaign rather than only to a private one.
	//
	// §6.6 says "private if the campaign is private", and it is private in both
	// cases on purpose. A shared cache that stored an asset for a public campaign
	// goes on serving it after the campaign is made private, because nothing tells
	// it otherwise, and S-8.3 is a claim about the bytes rather than about the
	// moment they were fetched. `no-cache` keeps the `ETag` useful: a browser
	// stores a 40 MB map and revalidates it, so a reader who navigates back pays
	// one conditional request rather than 40 MB again. A 304 to a *ranged*
	// revalidation is the cheap answer rather than the wrong one, because the
	// premise of a client sending a validator is that it holds the whole
	// representation and can serve its own slice from it.
	cacheControlAssets = "private, no-cache"
)

// RootLookup returns a campaign's confined content root.
//
// `*content.Registry` satisfies it as it stands. Narrower than the registry on
// purpose: a handler that can enumerate every campaign's root is a handler that
// can be handed a slug it was never authorised for, and the only slug it is ever
// given is the one the access gate resolved.
type RootLookup interface {
	Get(slug string) (*content.Root, error)
}

// Handler serves the campaign asset route.
//
// Exported fields rather than a constructor with four parameters, matching
// `wiki.Handler` and `accounts.Router`: the composition root writes one literal
// and a test writes another, and a struct literal names what it sets. Every field
// is written once before the server starts and read on every request, so a Handler
// is safe for concurrent use — no field is written after construction.
type Handler struct {
	// Roots is how a campaign's content root is found, and the confinement
	// boundary every path this route reads goes through. Required.
	Roots RootLookup

	// Logger receives this route's lines. Nil is allowed and discards them, so a
	// test does not have to construct a logger to serve a request.
	Logger *slog.Logger

	// Instance names the running instance for the failure state's shell. A value
	// rather than a lookup so a response does not depend on when it was written.
	Instance components.InstanceView

	// SignOutHref is where the failure state's sign-out form posts. Empty renders
	// no form.
	SignOutHref string
}

// Mount registers the asset route on mux.
//
// mux is the campaign-scoped mux — the one `mountCampaignRoutes` fills — and the
// pattern carries the `/c/` prefix even though that mux is itself mounted there.
// A Go 1.22 mux routes on a prefix and then hands the *whole* path to what it
// matched, so a pattern written without the prefix would never fire.
//
// The route is mounted here and nowhere else. Authorisation is a gate the mount
// list applies (ADR 0024): `mountCampaignRoutes` fills a mux the router wraps in
// `campaigns.RequireRead`, so a handler that also asked whether the reader may
// read this campaign would be a second copy of the S-8 matrix — and the copy
// nobody reviews is the one that eventually answers 403 where the gate answered
// 404, which turns a private campaign's existence into a fact a stranger can
// learn one status code at a time.
func Mount(mux *http.ServeMux, handler *Handler) {
	mux.Handle("GET /c/{slug}/assets/{path...}", handler)
}

// ServeHTTP answers one asset request.
//
// Three branches and nothing else: a failure, the bytes, or a close failure logged
// on the way out. The checks live in `open`, in the order its comments give; the
// only thing decided here rather than there is that a found asset is served with
// the media type the closed table chose for it.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	found, err := h.open(r.PathValue("slug"), r.PathValue("path"))
	if err != nil {
		h.writeFailure(w, r, err)

		return
	}

	defer func() {
		if closeErr := found.file.Close(); closeErr != nil {
			h.log(ctx, slog.LevelWarn, "asset.close_failed",
				slog.String("path", found.rel),
				slog.String("error", closeErr.Error()),
			)
		}
	}()

	h.writeAsset(w, r, campaigns.AccessFrom(ctx), found)
}

// foundAsset is one asset, proved addressable and open.
//
// A struct rather than four return values because the fourth is a file handle
// with a close attached to it, and a four-value signature is a signature whose
// values get transposed the first time somebody adds a field.
type foundAsset struct {
	// rel is `Target.Path()`: root-relative, slash-separated, no leading slash.
	// Carried because it is the validator's path component and the log line's,
	// and because handing `ServeContent` anything else would put a host directory
	// layout into a response.
	rel string

	// media is what the closed table decided about the extension.
	media assetMedia

	// info is the stat that established the size and the modification time. The
	// size is what `ServeContent` computes its ranges from.
	info fs.FileInfo

	// file is the read handle, opened through the confined root. Never an
	// `*os.File` this package built from a path: a path handed to `os.Open` skips
	// the confinement, which is the one thing `content.Root` exists to provide.
	file *os.File
}

// open resolves a URL path to an open asset, or to a classified failure.
//
// The order is the argument, so the comments are the order:
//
//  1. `Root.At` — confinement. S-3.5, and it is `os.Root`.
//  2. the name policies — what the index also refuses.
//  3. the extension — what may be served at all.
//  4. `Stat` — does it exist, and is it a regular file.
//  5. `Open` — the handle `ServeContent` reads.
//
// A missing file is a 404 and a refusal is a 403, and the two are never
// interchanged: a 404 is a statement about existence, so a 404 in reply to a path
// that leaves the campaign's root tells an attacker which paths exist outside it
// and turns an asset read into a probe for the host's filesystem layout. That is
// why `content.Root`'s sentinels are three distinct types and why `classify` here
// keeps them distinct.
func (h *Handler) open(slug, urlPath string) (foundAsset, error) {
	root, err := h.Roots.Get(slug)
	if err != nil {
		return foundAsset{}, classify(fmt.Errorf("content root for %s: %w", slug, err))
	}

	// `Root.At` and not `filepath.Join`: a path from a request is untrusted, and
	// the confinement is `os.Root`'s rather than a lexical normaliser's. The value
	// arrives already unescaped — `PathValue` returns `%2e%2e%2f` as `../` — so an
	// escaped separator is a separator here, and the only reason that is harmless
	// is the check this call performs.
	//
	// A literal `..` never reaches this line: `net/http` cleans the request path
	// and answers with a redirect before a handler runs, and only the escaped
	// spelling survives that. Which is exactly why the confinement is load-bearing
	// rather than a second opinion.
	target, err := root.At(urlPath)
	if err != nil {
		return foundAsset{}, classify(err)
	}

	rel := target.Path()

	// The name policies, on the confined and normalised path. After `At` rather
	// than before it, so that no policy decision is ever made about a path that
	// is not inside the campaign's root in the first place.
	if hiddenName(rel) {
		return foundAsset{}, classify(errHiddenName)
	}

	media, found, refused := mediaFor(rel)
	switch {
	case refused:
		// A kind of file this route declines to serve, whatever the reader is and
		// whatever the bytes say. See media.go for why `.md` and `.html` are here.
		return foundAsset{}, classify(errRefusedKind)
	case !found:
		// An extension nobody decided about. A 404 rather than a refusal, because
		// there is no decision to report: a closed table with a fallback is not a
		// closed table.
		return foundAsset{}, classify(errUnknownKind)
	}

	info, err := target.Stat()
	if err != nil {
		return foundAsset{}, classify(err)
	}

	// A regular file, and the check is not pedantry. A directory is the obvious
	// case and `ServeContent` would answer it with something unhelpful. The other
	// one is a named pipe, which is a legal directory entry on a POSIX host and
	// which this route would block on forever inside a read — one request holding
	// a connection until the handler budget fires, from a file an author created
	// by accident. Neither is something a browser can use.
	if !info.Mode().IsRegular() {
		return foundAsset{}, classify(errNotARegularFile)
	}

	file, err := target.Open()
	if err != nil {
		return foundAsset{}, classify(err)
	}

	return foundAsset{rel: rel, media: media, info: info, file: file}, nil
}

// writeAsset writes the headers the asset's kind implies, then hands the bytes to
// `http.ServeContent`.
//
// Every header is written *before* the call and none after it, because
// `ServeContent` commits the status line itself. That ordering is not a style
// choice: the `Content-Type` written here is what stops `ServeContent`'s sniffing
// branch from ever running, and a header set after `w.WriteHeader` is a header no
// client receives.
func (h *Handler) writeAsset(
	w http.ResponseWriter,
	r *http.Request,
	access campaigns.Access,
	found foundAsset,
) {
	header := w.Header()

	// The media type from the table, never from the bytes. See media.go.
	header.Set(contentTypeHeader, found.media.contentType)

	if found.media.attachment {
		header.Set(dispositionHeader, dispositionAttachment)
	}

	if found.media.sandbox {
		header.Set(contentPolicyHeader, sandboxPolicy)
	}

	// `nosniff` again, although `securityHeaders` already sets it on every
	// response. Written here so the guarantee is a property of this route rather
	// than of a middleware a future route might not be mounted behind: with
	// sniffing forbidden the declared type is the only type a browser will use,
	// which is what makes the closed table a boundary rather than a suggestion.
	header.Set(nosniffHeader, nosniffValue)

	// `Accept-Ranges` is set here and not left to `ServeContent`, which writes it
	// only on the paths that reach its copy loop. RFC 9110 §14.1 has an origin
	// server advertise range support on every answer, and the two answers a reader
	// most needs to see it on — a 416 and a 304 — are both decided before
	// `ServeContent` gets there.
	header.Set(acceptRangesHeader, acceptRangesBytes)

	// The validator names the campaign, so two campaigns holding a byte-identical
	// file advertise two validators (S-8.3). See assetValidator for the pre-image.
	header.Set(etagHeader, assetValidator(access.Campaign.ID, found.rel, found.info))
	header.Set(cacheControlHeader, cacheControlAssets)

	// `Vary: Cookie` on the bytes, and the reasoning is here because it is not
	// obvious: an asset's bytes are identical for every reader the gate admits, so
	// `Vary` is not claiming a variation that exists.
	//
	// ADR 0035's correction says the wiki document varies by reader because the
	// shell carries the reader's name and a sign-out form, and that `Vary: Cookie`
	// is required there. The same argument covers this route's *failure* states,
	// which are the same shell. The asset bytes are the interesting case, and the
	// reason to carry the header anyway is a cache rather than a browser: RFC 9110
	// §12.5.3 requires a cache to consider `Vary` when *selecting* a stored
	// response, and a stored response with no `Vary` matches any request — so a
	// cache holding this route's 404, written for a GM because a sync had not
	// written the file yet, may hand it to an anonymous reader of a public campaign
	// for whom the file exists. A `Vary` on only some of the responses does not
	// prevent that; it makes it likely.
	//
	// The cost is zero, which is what makes this the right answer rather than a
	// compromise: `private, no-cache` means no shared cache may store this response
	// at all, so there is no shared cache for a `Vary` to fragment and the caches
	// that remain hold one reader's copies anyway. `Vary` is free on a private
	// response, and only ever dangerous when it is absent.
	//
	// What it does **not** fix is the gate's own 404: `campaigns.Guard` writes
	// `{"error":"not found"}` with no `Cache-Control` and no `Vary`, so a shared
	// cache in front of an instance can pin that one for a URL a member should be
	// able to fetch. The gate is not this package's file.
	header.Set(varyHeader, cookieVary)

	// The size is not passed because `ServeContent` takes an `io.ReadSeeker` and
	// asks it, by seeking to the end and back. On a regular file that is two
	// syscalls and no `stat`, and it is the reason this route can hand over an
	// `*os.File` rather than a byte slice: the range algebra reads the same handle
	// the validator was derived from.
	http.ServeContent(w, r, found.rel, found.info.ModTime(), found.file)
}

// assetValidator returns the `ETag` for one asset: `W/"<sha256(...)"` over the
// campaign id, the path, the size and the modification time.
//
// **Weak**, and unlike `content.CacheKey.ETag`. That formula hashes the file's
// content because the route had to read the file to render it, so the hash was
// being produced anyway; this route reads tens of megabytes and does not, and
// hashing a battle map per request to produce a header would cost more than every
// other thing the route does. So the pre-image is the four inputs above and the
// weak prefix says exactly what that is: a hint, not a promise of byte-identity.
//
// The consequence is visible in a test, and is stated here so that a reader of the
// test does not think it is a bug: RFC 9110 §13.1.5 tells a client not to put a
// weak validator in `If-Range`, and `http.ServeContent` follows it — a weak `ETag`
// never strong-matches, so an `If-Range` carrying one is answered with the whole
// representation rather than a slice. A client that wants a conditional range
// sends the `Last-Modified` date, which is on every response for it to send.
//
// The residual risk is the one S-5.2 names, narrowed. A filesystem with
// one-second mtime granularity can present two different writes of the same size
// inside the same second, and the second is then revalidated as current. For a
// page that defect is a stale *render*, which is what the content hash exists to
// prevent; for an asset it is a stale *file* in a reader's own browser cache, it
// clears on the next edit, and no access decision depends on it.
//
// The campaign id and the path are in the pre-image and neither is in
// `CacheKey.ETag`'s, for opposite reasons worth stating side by side.
//
// The **campaign id** is S-8.3 made structural. There, adding it would change a
// validator when a page moved; here a moved file has a different URL anyway, and two
// campaigns holding identical bytes get two provably distinct validators rather than
// relying on a cache having keyed on the URL.
//
// The **path** is here and omitted there, and the difference is what an asset's
// validator is made of. A page's body is a function of its content hash, so two pages
// with one hash are one representation and one validator is correct. An asset's body
// is not a function of (size, mtime): two different maps can be the same size and
// arrive from a sync in the same second. Share a validator between those and a client
// holding `a/map.png` revalidates `b/map.png`, is told 304, and reads the first
// map's bytes out of its cache for the second — a wrong-bytes answer shaped exactly
// like a cache hit.
func assetValidator(campaignID int64, rel string, info fs.FileInfo) string {
	preimage := strconv.FormatInt(campaignID, 10) +
		validatorPreimageSep + rel +
		validatorPreimageSep + strconv.FormatInt(info.Size(), 10) +
		validatorPreimageSep + strconv.FormatInt(info.ModTime().UnixNano(), 10)

	digest := sha256.Sum256([]byte(preimage))

	return weakValidatorPrefix + hex.EncodeToString(digest[:]) + `"`
}
