// Package theme serves a campaign's own stylesheet: `GET /c/{slug}/theme.css`.
//
// # What this package is
//
// The theme layer's server half, and the half that carries the whole of §4.12.2's
// claim. A campaign cannot ship CSS. It ships `theme.yaml`, this package reads it
// through the campaign's confined `os.Root`, checks every token name against the
// ownership table in ownership.go, measures the brand pair against WCAG's floors,
// and writes the stylesheet itself. Every byte a browser receives from this route
// was written by `generate` — which is the difference between a campaign theme and
// a campaign stylesheet, and the reason the interesting code here is refusals
// rather than output.
//
// # Three properties, and each one is the reason a thing is where it is
//
//  1. **Per-campaign, never per-user.** §4.12.4 settles it and ADR 0035 is what
//     happens when somebody forgets: the `sp_ui` cookie is what the head resolver
//     reads, so anything a per-user theme touched would make the document vary by
//     cookie. The route therefore reads **nothing** from the request but its slug,
//     and the sheet it writes is a function of the campaign and the manifest alone.
//     The assertion that holds it is byte-identity across every reader and every
//     cookie value — `TestTheSheetIsByteIdenticalForEveryReaderAndPreference` —
//     rather than an assertion about headers, because a test that checks a header
//     is absent cannot see the variation that header was protecting.
//  2. **Overridable versus not, as data.** ownership.go is the fixed constant, the
//     manifest is validated against it, and the generator emits names by iterating
//     it. "Don't set it" would be a comment; "the generator's names come from the
//     allowlist" is a property of the code, and a token added to tokens.css
//     without a decision here fails the build.
//  3. **Failing toward the product's own theme.** §4.12.3: a manifest that will
//     not apply leaves the last good theme in effect, or the core theme if there
//     never was one. A rejected brand is a campaign that looks slightly less
//     branded; an accepted unreadable one is a product that cannot be read.
//
// # The retention, and why it is derived state
//
// Keeping the last good sheet means a map from campaign id to sheet, guarded by a
// mutex. It is the only state this package holds, and it is **derived**: it can be
// rebuilt from the manifest, and every process computes the same answer for the
// same bytes. That is what keeps it clear of the single-instance rule — the hub's
// in-memory game state is authoritative and must not be duplicated, whereas this
// is a cache of a file and a second instance computes an equal one.
//
// One case is deliberately *not* retained: a manifest that has been **deleted**.
// Withdrawal is not a failure, and a GM who removes `theme.yaml` has said the
// campaign is unbranded. Keeping the remembered sheet after a deletion would mean a
// brand a GM has deleted follows them forever, which is the opposite of what
// deleting the file means.
//
// # What is not here, and why
//
//   - **Fonts.** §4.12.1 makes `--font-prose` and `--font-ui` overridable through
//     `@font-face`. The product declares both as a system stack in shell.css and
//     there is no `--brand-font-*` token for an override to land in, so honouring
//     the override needs a shell.css change that is not this work item's file. Until
//     it exists, `--font-*` is in the protected list and a manifest naming it is
//     refused rather than accepted and dropped — a silent drop is worse than a
//     refusal, because it leaves a GM believing their font applied.
//   - **The GM notice.** §4.12.3's third requirement is a notice on the campaign
//     overview naming the rejected pair. `RefusalError` carries the token and the
//     reason for exactly that surface; the surface itself belongs to the campaign
//     route, and the accessor it will need is not exported here because nothing in
//     this package would call it.
//   - **`observability.EventThemeBrandInvalid`.** The refusal is logged at error
//     level under that name, and the name is not yet in `AllEventNames()`: adding a
//     seventh name beyond §13.2 needs a decision record (ADR 0032 is the precedent),
//     and that is the integrator's to make rather than this file's.
package theme

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/observability"
)

// The header names this route writes.
//
// Spelled out rather than repeated as literals, because a response writer that
// sets `Cache-Control` under one spelling and `ETag` under another is a response
// whose caching behaviour is a guess.
const (
	contentTypeHeader  = "Content-Type"
	nosniffHeader      = "X-Content-Type-Options"
	cacheControlHeader = "Cache-Control"
	etagHeader         = "ETag"
	varyHeader         = "Vary"
)

const (
	// stylesheetContentType is what a stylesheet is served as. An explicit charset
	// because the only text this route writes is ASCII, and an explicit type
	// because `http.ServeContent` sniffs anything whose type it does not know —
	// which on a sheet the server generated would be a sniffing branch reached by
	// a bug rather than by design.
	stylesheetContentType = "text/css; charset=utf-8"

	// plainTextContentType is what the failure state is served as. Plain text and
	// never the shell: see writeSheetFailure.
	plainTextContentType = "text/plain; charset=utf-8"

	// nosniffValue forbids content-type sniffing, as on every other route in the
	// product.
	nosniffValue = "nosniff"

	// cookieVary is the `Vary` value, and why it is here when the bytes do not
	// vary by reader is worth a paragraph — see writeSheet.
	cookieVary = "Cookie"

	// cacheControlSheet is UI §6.6's row for this route with `private` applied to
	// every campaign rather than only to a private one.
	//
	// The record says `public`, and this is a **deviation** from it, in the same
	// direction `assets` takes and for the same reason: a theme inherits its
	// campaign's visibility, so the gate's 404 for a private campaign is
	// reader-dependent in exactly the way S-8.3 is about. A shared cache that
	// stored this response for a URL a member could fetch would serve it to a
	// stranger who cannot — and while the body is two colour literals, the
	// *decision* is not the campaign's to publish.
	//
	// `no-cache` rather than `no-store` because the `ETag` makes revalidation
	// correct: the sheet is a few hundred bytes, and the browser revalidates it
	// once per campaign navigation rather than downloading it again. The same pair
	// `assets` uses, and for the same reason a `Vary` on a private response is
	// free: no shared cache may store this, so there is no shared cache for the
	// header to fragment.
	cacheControlSheet = "private, no-cache"

	// sheetName is the filename `ServeContent` is told about, which is the last
	// path segment of the URL it is given and nothing a caller chooses.
	sheetName = "theme.css"

	// invalidEvent is the §4.12.3 event name, at error level. See the package
	// clause: the name is not in `observability.AllEventNames()` yet.
	invalidEvent = "theme.brand_invalid"

	// failureBody is the one sentence a failure state carries. A subresource
	// response is never rendered, so its body exists to be logged by a browser
	// rather than read by a person; a fixed string is both enough and the only
	// kind that cannot carry anything out of the campaign.
	failureBody = "theme unavailable\n"

	// noStore is the `Cache-Control` a failure carries. There is no validator for
	// a failure, and a pinned 500 outlives the fault that caused it.
	noStore = "no-store"
)

// RootLookup returns a campaign's confined content root.
//
// `*content.Registry` satisfies it, and it is the same one-method interface
// `assets.RootLookup` is, narrowed for the same reason: a handler that can
// enumerate every campaign's root can be handed a slug it was never authorised
// for, and the only slug it is ever given is the one the gate resolved.
type RootLookup interface {
	Get(slug string) (*content.Root, error)
}

// Handler serves the campaign theme route.
//
// Exported fields rather than a constructor, matching `wiki.Handler` and
// `assets.Handler`: the composition root writes one literal and a test writes
// another, and a struct literal names what it sets. Every field is written once
// before the server starts, so a Handler is safe for concurrent use — no field is
// written after construction, and the one piece of mutable state it owns is behind
// its own lock.
type Handler struct {
	// Roots is how a campaign's content root is found, and the confinement
	// boundary the manifest path goes through. Required.
	Roots RootLookup

	// Logger receives this route's lines. Nil is allowed and discards them, so a
	// test does not have to construct a logger to serve a request — but a refusal
	// is logged, so a nil logger means a refusal nobody can see, which is why the
	// refusal tests pass a real one.
	Logger *slog.Logger

	// remembered holds each campaign's last good sheet. Zero value usable, which
	// is why it is not a constructor parameter.
	remembered lastGood
}

// Mount registers the theme route on mux.
//
// mux is the campaign-scoped mux — the one `mountCampaignRoutes` fills — and the
// pattern carries the `/c/` prefix even though that mux is itself mounted there,
// because a Go 1.22 mux routes on a prefix and then hands the *whole* path to
// whatever matched.
//
// The route is mounted here and nowhere else. Authorisation is a gate the mount
// list applies (ADR 0024): `mountCampaignRoutes` fills a mux the router wraps in
// `campaigns.RequireRead`, so a private campaign's theme is a 404 to a stranger
// for the same reason its pages are — and this handler never asks who is asking,
// which is what makes that true rather than aspirational.
//
// **One pattern, and it is a literal.** Not `{path...}`. The route reads one
// fixed filename inside the campaign's root and the request's only input is the
// slug; a wildcard here would be a route whose URL space is a function of a
// campaign's directory listing, which is a different route wearing this one's
// name.
func Mount(mux *http.ServeMux, handler *Handler) {
	mux.Handle("GET /c/{slug}/theme.css", handler)
}

// ServeHTTP answers one theme request.
//
// Two outcomes: the sheet, or a failure. The failure is a bare plain-text body
// rather than the shell's failure state, and the reason is worth stating because
// `assets` does the opposite: a stylesheet is fetched by the browser as a
// subresource, and a subresource that returns a complete HTML document is a
// document the browser parses and discards with an error nobody sees. The shell
// belongs to the document that linked it.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)

	sheet, err := h.sheet(ctx, access.Campaign)
	if err != nil {
		h.log(ctx, slog.LevelError, "theme.resolve_failed",
			slog.Int64("campaign_id", access.Campaign.ID),
			slog.String("slug", access.Campaign.Slug),
			slog.String("error_class", observability.ErrorClass(err)),
		)

		writeSheetFailure(w)

		return
	}

	h.writeSheet(w, r, access.Campaign.ID, sheet)
}

// sheet resolves the stylesheet this campaign serves right now.
//
// The five cases, and each one is a decision about what a *reader* sees rather
// than about what the manifest says:
//
//  1. **No manifest, or an empty one.** The core theme, and any remembered sheet
//     is forgotten. Withdrawal is a decision, not a failure.
//  2. **A manifest that validates.** The generated sheet, remembered as good.
//  3. **A manifest over the size cap, or one the parser would not read.** The
//     remembered sheet, plus one error line.
//  4. **A manifest whose content is refused.** The remembered sheet, plus one
//     error line. This is §4.12.3's first requirement and it is the whole reason
//     `lastGood` exists: a GM who fixes one bad colour should not lose the theme
//     they had while they were fixing it.
//  5. **A manifest that could not be read.** A 500. The campaign may have a good
//     theme on disk, and reporting "no theme" for our own I/O failure would be a
//     lie served to every reader of the campaign.
func (h *Handler) sheet(ctx context.Context, campaign domain.Campaign) (string, error) {
	root, err := h.Roots.Get(campaign.Slug)
	if err != nil {
		return "", fmt.Errorf("content root for %s: %w", campaign.Slug, err)
	}

	src, err := readManifest(root)

	switch {
	case err == nil:
	case errors.Is(err, ErrNoManifest):
		h.remembered.forget(campaign.ID)

		return generate(brand{}), nil

	case manifestRefused(err):
		h.logRefusal(ctx, campaign, err)

		return h.remembered.refuse(campaign.ID), nil

	default:
		return "", err
	}

	manifest, err := Parse(src)
	if err != nil {
		h.logRefusal(ctx, campaign, err)

		return h.remembered.refuse(campaign.ID), nil
	}

	if !manifest.Branded() {
		h.remembered.forget(campaign.ID)

		return manifest.Sheet(), nil
	}

	h.remembered.remember(campaign.ID, manifest.Sheet())

	return manifest.Sheet(), nil
}

// logRefusal reports a manifest semiplane would not apply.
//
// **Error level**, because §4.12.3 says so and because a silently unreadable brand
// colour is a silent failure: the campaign looks unbranded and nothing says why.
// The attributes are the campaign's identity and a reason that is a fixed sentence
// plus at most one validated token name — never a byte from the manifest, for the
// reason `errNotAColour` gives. A YAML or hex error quoted here would put campaign
// file contents into a log aggregator, which is what S-12.3 forbids.
//
// The error's own text is not logged. `RefusalError.Reason` is read instead, which
// is the sentence that names the rule; anything else falls back to the error's
// class. So a value from a campaign file cannot reach the line even if a future
// refusal type forgets to be careful about it.
func (h *Handler) logRefusal(ctx context.Context, campaign domain.Campaign, err error) {
	h.log(ctx, slog.LevelError, invalidEvent,
		slog.Int64("campaign_id", campaign.ID),
		slog.String("slug", campaign.Slug),
		slog.String("reason", refusalReason(err)),
	)
}

// refusalReason is the text a refusal contributes to a log line.
//
// `errors.As` rather than `err.Error()`, for the reason in `logRefusal`. The
// `Token` is deliberately *not* included: the reason names the rule, and the token
// is the one thing here that came out of the campaign's file — validated, but
// still theirs, and a name an operator can get from the manifest.
func refusalReason(err error) string {
	if refusal, isRefusal := errors.AsType[*RefusalError](err); isRefusal {
		return refusal.Reason
	}

	return observability.ErrorClass(err)
}

// manifestRefused reports whether err is a decision about the manifest's contents
// rather than a failure to read it.
//
// The distinction decides the response: a refusal keeps the campaign's last good
// theme, and a read failure answers 500. Both are logged, at error level, and
// they are the same line deliberately — an operator wants "this campaign's theme is
// wrong" and the reason, not two dashboards.
func manifestRefused(err error) bool {
	_, isRefusal := errors.AsType[*RefusalError](err)

	return errors.Is(err, ErrManifestTooLarge) ||
		errors.Is(err, ErrMalformedManifest) ||
		isRefusal
}

// log writes one line, or discards it when no logger was configured.
func (h *Handler) log(ctx context.Context, level slog.Level, message string, attrs ...any) {
	if h.Logger == nil {
		return
	}

	h.Logger.Log(ctx, level, message, attrs...)
}

// writeSheet writes the headers the sheet's kind implies, then the bytes.
//
// The ordering is the same one `assets.writeAsset` uses and for the same reason:
// `http.ServeContent` commits the status line itself, so a header set after this
// function returns is a header no client receives. Setting `Content-Type` here is
// also what keeps `ServeContent`'s sniffing branch unreachable.
//
// **`Vary: Cookie` on bytes that do not vary by reader**, copied from the assets
// route's argument rather than invented here: an asset's bytes are identical for
// every reader the gate admits, and the header is still right, because RFC 9110
// §12.5.3 requires a cache to consider `Vary` when *selecting* a stored response —
// and a stored response with no `Vary` matches any request. A cache holding this
// route's 404, written for a GM because a sync had not written the manifest yet,
// would otherwise hand it to an anonymous reader of a public campaign.
//
// The cost is zero, which is what makes it right rather than a compromise:
// `private, no-cache` means no shared cache may store this response at all.
func (h *Handler) writeSheet(
	w http.ResponseWriter,
	r *http.Request,
	campaignID int64,
	sheet string,
) {
	header := w.Header()

	header.Set(contentTypeHeader, stylesheetContentType)
	header.Set(nosniffHeader, nosniffValue)
	header.Set(etagHeader, sheetValidator(campaignID, sheet))
	header.Set(cacheControlHeader, cacheControlSheet)
	header.Set(varyHeader, cookieVary)

	// A zero modification time, so `ServeContent` writes no `Last-Modified`. The
	// validator is the strong one over the generated bytes, which is a stronger
	// statement than a file's mtime, and a second date that could disagree with it
	// is a second answer.
	//
	// The reader is a `*strings.Reader` rather than an `*os.File` because the sheet
	// was just generated: there is no file to open, and the range algebra
	// `ServeContent` implements is answered from memory in microseconds.
	http.ServeContent(w, r, sheetName, time.Time{}, strings.NewReader(sheet))
}

// writeSheetFailure answers a sheet that could not be resolved.
//
// A bare 500 with no shell, and the status is 500 rather than the 404 the assets
// route uses for its refusals because this is not a refusal: it is a fault in
// reading a file that may well be a perfectly good manifest. A 404 would tell the
// campaign's readers that their theme is gone.
//
// The body is plain text and a fixed string. A subresource response is never
// rendered, so its body exists to be logged by a browser rather than read by a
// person, and a fixed string is both enough and the only kind that cannot carry
// anything out of the campaign.
func writeSheetFailure(w http.ResponseWriter) {
	header := w.Header()

	header.Set(contentTypeHeader, plainTextContentType)
	header.Set(nosniffHeader, nosniffValue)
	header.Set(cacheControlHeader, noStore)
	w.WriteHeader(http.StatusInternalServerError)

	if _, err := fmt.Fprint(w, failureBody); err != nil {
		slog.Error("write response", slog.String("error", err.Error()))
	}
}

// lastGood is each campaign's most recent validated sheet.
//
// A mutex-guarded map rather than a `sync.Map`, for the reason `content.Registry`
// gives: the *type* matters more than the shape of the traffic, because a
// `sync.Map` stores `any` and every read is a type assertion that can fail at a
// request instead of at a construction.
//
// Keyed by campaign id rather than slug, because the slug is the request's input
// and the id is the campaign's identity. A slug is validated at registration and is
// the URL; the id is what the validator is salted with, so keying the retention the
// same way means one campaign is one sheet under one identity throughout.
type lastGood struct {
	mu     sync.RWMutex
	sheets map[int64]string
}

// remember records a campaign's validated sheet.
func (l *lastGood) remember(campaignID int64, sheet string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.ensure()[campaignID] = sheet
}

// refuse is what a refused manifest calls, and it returns the sheet the campaign
// keeps serving.
//
// Two things happen, and the second is the one that is easy to lose: the campaign's
// remembered sheet is returned unchanged, **and** a campaign that never had one is
// recorded anyway, so "this campaign has a refused manifest" is a state the
// process can answer rather than only a log line somebody has to correlate. §4.12.3's
// GM notice needs exactly that answer, and reading it from the retention map is
// why the refusal is stored at all.
func (l *lastGood) refuse(campaignID int64) string {
	l.mu.Lock()
	defer l.mu.Unlock()

	sheets := l.ensure()

	sheet, known := sheets[campaignID]
	if !known {
		sheet = generate(brand{})
		sheets[campaignID] = sheet
	}

	return sheet
}

// ensure returns the map, allocating it on first use.
//
// Called with the write lock held, and only from the two methods that write: the
// zero value of `lastGood` is usable, so the map is created on the first campaign
// that needs one rather than in a constructor.
//
// Both writers go through here, which is the point. Allocating in `remember` and not
// in `refuse` is a nil-map write that panics on exactly one campaign — the one whose
// *first* theme was a refused one — and `TestARefusedManifestWithNoGoodOneFallsBack
// ToTheCoreTheme` found it.
func (l *lastGood) ensure() map[int64]string {
	if l.sheets == nil {
		l.sheets = make(map[int64]string)
	}

	return l.sheets
}

// forget drops a campaign's remembered sheet, because the campaign withdrew it.
func (l *lastGood) forget(campaignID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.sheets, campaignID)
}
