package secrets

// The five responses this route writes, and what each one is a statement about.
//
// They are in one file because the distinctions between them *are* the distinctions
// between the requirements, and a reader who has them in five places has to reassemble
// the contract to know whether one of them is right:
//
//   - `writeRevealed` — 204 and the new validator. No body, because the byte was
//     flipped and the row was written and a JSON acknowledgement of that is a second
//     representation of a fact the `ETag` header already carries.
//   - `writeConflict` — 412, the current source, and its digest.
//   - `writePreconditionRequired` — 428, and the current validator if it can be read.
//   - `writeFailure` — everything else, with the classified status.
//   - `writeNotFound` — the gate's 404, byte for byte.
//
// # Every response here is `private, no-store`, and none is `Vary`-ed
//
// `private, no-store` on **every** status, including the 204 and including the 412
// that carries page source. The reason is that all three of those carry
// `include_secrets`-equivalent material:
//
//   - the 204 and the 428 carry a **GM-salted validator** (ADR 0016), and a stored
//     GM-salted validator is a stored answer to "what is this page's current version"
//     for a page that may hold secret text;
//   - the 412 carries **the page's source**, which on a `[!secret]` page is the secret
//     text, verbatim, in a JSON field.
//
// And `private, no-store` rather than `no-store` alone, which is a deliberate
// divergence from `edit.writeSaved`: S-5.4 asks for both words on every
// `include_secrets=true` response, `no-store` is the directive that stops the write,
// and `private` states the reason to a cache that honours only one of the two. The
// editor's 204 has no body and so arguably needed less; this route's 412 has the page
// in it, so it needed more.
//
// **No `Vary` on any of them**, and that is `edit.writeSaved`'s reasoning rather than
// a departure from it: none of these bodies varies with the reader *except* through
// the gate, and the gate's own refusals carry no `Vary` either. A `Vary` on a response
// that varies on nothing is a cache hint that costs a key.
//
// # What the 412 must not carry
//
// S-6.2 says "412 with the current body and its hash", and the editor renders a whole
// editor document for it because UI §4.7 puts the diff in the editor's preview slot.
// There is no editor here — the client is an HTTP caller holding a JSON body — so
// this response is the three facts the requirement names and nothing else:
//
//   - **the current source** as text rather than as rendered HTML, for the editor's
//     reason: a caller reconciling needs what is *in the file*;
//   - **its digest**, twice, in two forms. The `ETag` header is the salted validator
//     a retry must present, and the hex digest in the body is the same fact unsalted
//     — which is what a GM can paste into a diff tool, and which the validator itself
//     cannot be because it answers no question about content;
//   - **not** a second read of the file. The bytes and the digest are passed in from
//     the branch that compared them, because a sync client is free to write between
//     the read that found the conflict and the read that rendered it, and a response
//     with two different "current" texts is worse than one that is a moment stale.
//
// **And not a page path outside the campaign, and never the secret's own text.** The
// `Secret.Body` this route read to resolve the anchor is never put in a response; the
// source comes from the file as a whole, to a caller the gate already admitted as the
// campaign's GM, and S-12.3's rule about not carrying page content applies to every
// *other* caller — which is why the 412 is unreachable without `RequireEdit`
// (`TestTheConflictBodyNeverReachesANonGM`).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/httpapi/middleware"
)

// The header names this route writes, named because `net/http` has no constants for
// the first and the second is this project's own spelling.
const (
	contentTypeHeader  = "Content-Type"
	cacheControlHeader = "Cache-Control"
	jsonContentType    = "application/json; charset=utf-8"

	// noStore is the value on every response here. See the file comment: S-5.4
	// verbatim, and stricter than the editor's 204 for the stated reason.
	noStore = "private, no-store"
)

// conflictBody is what a 412 carries.
//
// `Content` is the page's source and `ContentHash` its unsalted hex digest, which is
// S-6.2's two facts. The field names are this route's own — there is no shared shape
// for "a 412" in this project, because the editor's 412 is an HTML document and this
// one's audience is an HTTP client — and `TestTheConflictCarriesTheCurrentSourceAndItsHash`
// holds both fields against the file on disk.
type conflictBody struct {
	// Error is the fixed sentence every other refusal on this route uses, so a client
	// parses one shape for every failure.
	Error string `json:"error"`
	// ContentHash is the hex SHA-256 of `Content`, which is `pages.content_hash` for
	// the same bytes.
	ContentHash string `json:"contentHash"`
	// Content is the page's source as it is on disk right now.
	Content string `json:"content"`
}

// errorBody is the shape every refusal on this route shares, including the gate's.
//
// The gate's `writeError` writes `{"error":%q}` by hand and `httpapi.writeJSON` writes
// the same field through the encoder, so both produce `{"error":"…"}`. A client that
// parses one shape for every failure — including the ones the gate answers — is a
// client that cannot tell a route's refusal from the router's by looking at the body,
// which is the property `TestTheRefusalsAreTheSameBytesAsNoAccessIs` asserts.
type errorBody struct {
	Error string `json:"error"`
}

// writeRevealed answers a completed reveal: 204 and the new validator, with no body.
//
// 204 rather than 200, and the empty body rather than a JSON acknowledgement of it,
// for the editor's reason: the only thing a client needs from a reveal is the
// validator to present next time, and that is a header. A body would put a second
// representation of the page's state on the wire — one that is neither the page nor
// the editor, and one a client would have to learn to ignore.
//
// **The validator is over the bytes that were just written rather than over a re-read
// of the file.** The rename replaced the file, so a re-read would be a second read of
// a tree a sync client is free to be writing to, and the validator a client is handed
// has to be the one its own bytes produce. When the byte did not change, it is the
// same validator it already held — which is correct, and is why a double-click on
// reveal does not need the client to fetch anything again.
func (h *Handler) writeRevealed(w http.ResponseWriter, newValidator string) {
	h.writePrivateHeaders(w)
	w.Header().Set(etagHeader, newValidator)

	w.WriteHeader(http.StatusNoContent)
}

// writeConflict answers a stale reveal: 412, the current source, and its digest.
//
// The arguments beyond the request are the three facts the response is made of,
// passed rather than re-read for the reason the file comment gives.
func (h *Handler) writeConflict(
	w http.ResponseWriter,
	r *http.Request,
	target *content.Target,
	current source,
	currentValidator string,
) {
	ctx := r.Context()

	h.reportConflict(ctx, campaigns.AccessFrom(ctx), target.Path())

	h.writePrivateHeaders(w)
	w.Header().Set(etagHeader, currentValidator)

	w.WriteHeader(http.StatusPreconditionFailed)

	h.encode(w, r, conflictBody{
		Error:       "precondition failed",
		ContentHash: current.hash,
		Content:     current.body,
	})
}

// writePreconditionRequired answers a reveal that named no validator: 428.
//
// **Not a 200, and that is the whole of the reason this function exists.** A `PUT`
// with no `If-Match` is a request to rewrite the marker unconditionally — which is
// what S-6.3 forbids ("no silent overwrite, no last-write-wins fallback") and what a
// file lock cannot prevent, because Obsidian is a separate process that has never
// heard of this server's state. Worse than the editor's version of the same answer,
// arguably: an unconditional reveal is not a lost edit, it is a disclosure performed
// on text the requestor may not have been shown to have changed. Answering 200 would
// make the *absence* of the precondition succeed while a stale one fails, which is a
// worse contract than either — a client that dropped the header would believe it had
// revealed something.
//
// RFC 6585's 428 is the status for exactly this: the origin server requires the
// request to be conditional, and it is a 4xx a client can act on by re-reading the
// page.
//
// **The current validator is read for the response, and that read can fail.** The
// header is a convenience — a client that can see what it should have sent can recover
// without a second round trip — so a page that has become unreadable since the
// request is answered 428 with no validator rather than with a different status.
// Failing the whole request because a *convenience* header could not be produced would
// turn a protocol error into a confusing one. And **no body is read**, so there is
// nothing to echo and no page source to refuse to send.
func (h *Handler) writePreconditionRequired(
	w http.ResponseWriter,
	r *http.Request,
	target *content.Target,
) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)

	currentValidator := ""
	if src, err := h.readSource(ctx, target); err == nil {
		currentValidator = validator(access.Campaign.ID, target.Path(), src.hash)
	} else {
		h.log(ctx, slog.LevelDebug, "secrets.precondition_hash_unavailable",
			slog.String("campaign", access.Campaign.Slug),
			slog.String("path", target.Path()),
			slog.String("error", err.Error()),
		)
	}

	h.writePrivateHeaders(w)

	if currentValidator != "" {
		w.Header().Set(etagHeader, currentValidator)
	}

	w.WriteHeader(http.StatusPreconditionRequired)

	h.encode(w, r, errorBody{Error: errMissingValidator.Error()})
}

// writeFailure answers everything that is neither a conflict nor a missing
// precondition: 400, 403, 404, 413 and 500.
//
// The load-bearing case is the 404, which goes through `writeNotFound` so that this
// route's "no such page" and "no such secret" and the gate's "no such campaign" are
// one set of bytes. See that function.
func (h *Handler) writeFailure(w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)
	status := statusFor(err)

	if status == http.StatusNotFound {
		h.writeNotFound(w, r, access, err)

		return
	}

	h.writePrivateHeaders(w)

	w.WriteHeader(status)
	h.encode(w, r, errorBody{Error: err.Error()})

	h.logFailure(ctx, access.Campaign.Slug, status, err)
}

// writeNotFound answers "there is nothing here", in the gate's exact bytes.
//
// **This is the function that makes the 404 an existence oracle's opposite**, and it
// is separate from `writeFailure` for that reason rather than for tidiness: it is the
// one response on this route that is asserted **byte for byte** against the gate's
// own, and a test can only do that if both come from the same shape.
//
// Three refusals converge here — a page that is not there, a secret on it that is not
// there, and (from the gate) a campaign this reader may not see — and the property is
// that **nothing distinguishes them**. Not the status, not the body, not the headers.
// A reader who could tell "the page exists but the secret does not" from "the page
// does not exist" would learn the page's existence from a URL they guessed, and that
// is precisely the fact S-8 withholds and precisely what ADR 0024 calls an existence
// oracle.
//
// `no-store` is not decoration either: the response is reader-dependent (200 for a
// member, 404 for a stranger), and a reverse proxy in front of a self-hosted instance
// that cached an anonymous 404 would serve it to the GM entitled to that page.
//
// The **404 from an unmatched route** — `httpapi.notFoundHandler` — writes the same
// `{"error":"not found"}` and sets `Cache-Control: private, no-store` too, which is
// why this route's own 404 is written by hand rather than through a shared helper
// that might one day gain a `Vary`. `TestTheRefusalsAreTheSameBytesAsNoAccessIs`
// compares the bodies and the two headers directly.
func (h *Handler) writeNotFound(
	w http.ResponseWriter,
	r *http.Request,
	access campaigns.Access,
	err error,
) {
	ctx := r.Context()

	h.writePrivateHeaders(w)

	w.WriteHeader(http.StatusNotFound)
	h.encode(w, r, errorBody{Error: errNotFound.Error()})

	// Debug, not error: a mistyped path is the GM's own, it is not an operator's
	// problem, and an operator's log that is a wall of ordinary misses is a log
	// nobody reads.
	h.log(ctx, slog.LevelDebug, "secrets.not_found",
		slog.String("campaign", access.Campaign.Slug),
		slog.Int("status", http.StatusNotFound),
		slog.String("error", err.Error()),
	)
}

// writePrivateHeaders sets the two headers every response on this route carries.
//
// One function rather than five call sites, because "every response is
// `private, no-store`" is a property of the *route* and a property with five places
// to forget it is a property that gets forgotten. `TestEveryResponseIsPrivateAndUnstorable`
// holds it over every status this route can answer.
func (h *Handler) writePrivateHeaders(w http.ResponseWriter) {
	w.Header().Set(contentTypeHeader, jsonContentType)
	w.Header().Set(cacheControlHeader, noStore)
}

// encode writes the JSON body.
//
// `DisallowUnknownFields` is not applicable in this direction, but a single encoder
// per response is: two `json.NewEncoder` calls in one branch would be two places to
// forget that the status line is already committed, which is the only thing this
// function has left to report. The status is committed by the caller, so a failure
// here cannot be turned into a second answer — it is a truncated body, which is worth
// a log line and nothing more.
func (h *Handler) encode(w http.ResponseWriter, r *http.Request, payload any) {
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		h.log(r.Context(), slog.LevelError, "secrets.encode_failed",
			slog.String("request_id", middleware.MustRequestID(r.Context())),
			slog.String("error", err.Error()),
		)
	}
}

// logFailure records a classified failure at a level that matches what it means.
//
// A 500 is the operator's: something is wrong that a request cannot fix. A 400 and a
// 413 are the caller's, and are logged at debug so an operator's log is not a wall of
// ordinary malformed requests. The access log already carries the status for every
// request, so this line exists to carry the *classified* reason — and the reason is
// this project's own vocabulary, never the caller's input (S-12.3).
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

	h.log(ctx, level, "secrets.refused", attrs...)
}
