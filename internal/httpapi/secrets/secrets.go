// Package secrets serves the reveal endpoint: `PUT /c/{slug}/secrets/{path...}`.
//
// Revealing a secret is a **write to a page**, and that fact is the whole design.
// §5.6.4 says a reveal "go[es] through the same authorization and `If-Match`
// precondition as any other write", and S-6.2/S-6.3 make that precondition
// load-bearing rather than decorative. So this route is the editor's write path
// with one byte of the body replaced, and it deliberately behaves like it in
// every respect a client can observe:
//
//   - **Confinement first.** The page path arrives in a URL and `Root.At` is the
//     boundary (S-3.5), before a body is read and before a validator is computed.
//   - **The precondition before the body.** A request with no `If-Match` is a 428
//     (RFC 6585) rather than a 200, and a mismatch is a 412 that has written
//     nothing and recorded nothing.
//   - **The write is one byte.** `content.SetMarker` splices exactly
//     `MarkerOffset..MarkerOffset+1` and leaves every other byte alone, through
//     `Target.WriteFile`'s temp-file/`fsync`/rename. The atomic write is not
//     reimplemented here: `content` owns it, and a second implementation would be
//     a second set of answers to "is this write atomic".
//   - **No forced render, no forced re-index.** S-6.4 says the watcher's own event
//     converges the caches. This route moves a byte and returns a validator.
//
// # What is different from the editor, and why
//
// One ordering, and it is the substance of this package.
//
// The editor appends its `page_revisions` row **before** the file write, and
// `edit.go` argues for it: a revision row for content that did not land is noise,
// while a file that landed with no row is a hole in the one part of the project
// state the vault cannot rebuild. **A reveal inverts both halves of that
// argument.** A ledger row is not history — it is *permission*. §5.6.2's
// reconciliation step 2 diffs the ledger "for previously-revealed secrets that
// are now `-`" and step 3 re-applies the `+`. So a `secrets_revealed` row whose
// file write failed is not a stale record; it is an instruction, sitting in the
// table, to publish that secret on a later reconciliation pass — a disclosure
// triggered by *our* failure and by *no* act of the GM's. That is the one outcome
// this subsystem must never produce, and §5.6.2's rule that "every failure path
// resolves toward hiding" is what decides the order.
//
// So the byte goes down first and the ledger second, and the consequence is
// stated rather than hidden: if the ledger write fails the byte is already
// flipped. What makes that the safe direction is a property of
// `store.RevealSecret` — the membership check, the insert and the audit row are
// **one transaction**, so a failed ledger write leaves **no row at all**, and a
// row that does not exist is something reconciliation will never re-apply. Every
// failure of this route therefore ends with either nothing changed or nothing
// recorded, and never with something recorded and not changed.
//
// There is deliberately **no compensating write** that puts the byte back. It
// would narrow the window rather than close it (the process can die between the
// two writes), and it would add a second failure mode to a route whose whole
// argument is that it has one. The state converges instead: the file is
// authoritative for whether a secret is revealed (§5.6.2's precedence table), so
// a GM whose ledger write failed retries, finds the byte already correct, skips
// the write and records the row. `TestTheLedgerIsWrittenAfterTheByteAndTheRetry`
// holds that convergence rather than asserting it.
//
// # Authorisation is not here
//
// The route mounts `campaigns.RequireEdit` (S-6.5, §5.6.4, ADR 0024) and this
// handler never asks whether the GM may edit: it reads the campaign and the
// account the gate resolved. A slug naming nothing never reaches this package —
// the gate has already answered 404, with the same body this route's own 404
// carries, so a private campaign's existence is not observable from here either.
//
// # The 404 is byte-identical on purpose
//
// Three refusals share one body and one status: a page that is not there, a
// secret on it that is not there, and a campaign the gate cannot show the reader.
// Two of those are this handler's and one is the gate's, and the property that
// matters is that **nothing in the response distinguishes them**. A body that
// said "no such secret" would be an existence oracle for a page whose path a
// reader guessed but whose secret they had not been shown.
//
// # Every status this route can return
//
//	PUT   204  matched: the byte is written if it had to be, the ledger row is
//	            written, the audit row goes with it, and the new validator is on
//	            the response
//	PUT   412  mismatched: the current source and its digest are in the body,
//	            the validator on the header, and nothing was written or recorded
//	PUT   428  no `If-Match` at all
//	PUT   400  a path that named no location, a body that named no secret or no
//	            state, or a reveal of a callout that nests another (§ below)
//	PUT   403  a path that leaves the campaign's root
//	PUT   404  a page that is not there, or a secret on it that is not there
//	PUT   500  the root, the write or the ledger failed
//
// 401, 403 and 404 also come from the gate, and they are the gate's to answer: a
// `player`'s `PUT` is 403 and an anonymous one is 401 (S-14.4), and a private
// campaign is 404 to everybody who is not a member.
//
// # A nested callout cannot be revealed, and that is a refusal rather than a
// # silent no-op
//
// `content.ScanSecrets` forces a callout that contains another to
// `SecretCollapsed` **whatever its marker byte says**, because there is no
// rendering of a revealed outer callout that withholds the nested body. So a
// reveal aimed at one would write a `+` that renders as collapsed and record a
// ledger row for a disclosure that never reached a reader — the same
// record-a-disclosure-that-did-not-happen defect the 412 branch exists to avoid.
// This route therefore **refuses** it with a 400, and says why in the body.
//
// Hiding one is allowed and is not symmetric with it: the byte on disk may be `+`
// while the reported state is collapsed, and hiding writes that `+` to `-`, which
// makes the disk agree with what the pipeline renders. Refusing that would leave
// the file in the one state that disagrees with itself.
package secrets

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/httpapi/campaigns"
	"github.com/semiplane/semiplane/internal/observability"
)

// pageExtension is the suffix a page's path gains on its way in from a URL.
//
// The same constant and the same reasoning as the editor's: a URL carries no
// extension — `content.WikiHref` writes `/c/{slug}/wiki/Some%20Page` — and the
// extension is put back once, here, so that the link a GM reads aloud and the
// link a browser follows are the same string. It is spelled rather than shared
// because a route does not import a sibling route.
const pageExtension = ".md"

// pageFileMode is the mode a revealed page is written with.
//
// 0o600, matching the editor, and for the editor's reason: the campaign's content
// root is created 0o700, so a world-readable file inside it would be consistent
// with that root only by accident, and a page that arrived from a sync client may
// already be 0o644 — in which case a write that inherited the existing mode would
// widen it.
const pageFileMode = 0o600

// RootLookup returns a campaign's confined content root.
//
// `*content.Registry` satisfies it as it stands. Narrower than the registry on
// purpose: a handler that could enumerate every campaign's root could be handed a
// slug it was never authorised for, and the only slug it is ever given is the one
// the access gate resolved.
type RootLookup interface {
	Get(slug string) (*content.Root, error)
}

// Handler serves the reveal route.
//
// Exported fields rather than a constructor with four parameters, matching
// `wiki.Handler` and `edit.Handler`: the composition root writes one literal and a
// test writes another, and a struct literal names what it sets. Every field is
// written once before the server starts and read on every request, so a Handler is
// safe for concurrent use — the same claim `content.Root` makes, resting on the
// same thing: no field is written after construction.
type Handler struct {
	// Roots is how a campaign's content root is found, and the confinement
	// boundary every path this route touches goes through. Required.
	Roots RootLookup

	// Ledger is where a reveal is recorded (S-5.8, §5.6.4). Required, and
	// required to be *absent from every refusal path*: a 412, a 428, an unknown
	// secret and a refused nested reveal must all leave the table untouched,
	// because each of them is a request that did not disclose anything.
	//
	// `*store.Store` satisfies it as it stands — `RevealSecret` and
	// `UnrevealSecret` are its own methods — so the composition root passes the
	// same handle it passes every other route, and this package writes no SQL.
	Ledger Ledger

	// Logger receives this route's lines. Nil is allowed and discards them, so a
	// test does not have to construct a logger to serve a request.
	Logger *slog.Logger
}

// Mount registers the reveal route on mux, behind the edit gate.
//
// **The gate is mounted here and not left to the caller**, for the reason
// `edit.Mount` gives and with the same failure in mind: a caller that had to
// remember `RequireEdit` would eventually register the route without it, and the
// failure mode of that is a `player` writing `+` over a campaign's secrets — which
// is S-6.5, §5.6.4 and S-14.4 all saying the same thing about one URL.
//
// Layering is harmless and intentional: the router wraps the whole campaign mux in
// `RequireRead`, so a request passes that first and this second. Both answers come
// from `campaigns.Guard`, which is the only place the S-8 matrix is written down.
//
// One pattern rather than a handler switching on the method, because `net/http`
// gives the 405 for a method a path does not take: a `GET` to this URL is a method
// that exists nowhere in the design, and answering it 405 with an `Allow` header is
// more honest than a branch that guesses. There is nothing to `GET` — the anchors
// are properties of a page's own text, and the client that wants them can read the
// page it is already looking at.
//
// mux is the campaign-scoped mux — the one `mountCampaignRoutes` fills — and the
// pattern carries the `/c/` prefix even though that mux is itself mounted there. A
// Go 1.22 mux routes on a prefix and then hands the *whole* path to what it
// matched, so a pattern written without the prefix would never fire;
// `TestTheRevealMountCarriesTheSlug` holds both directions of that.
func Mount(mux *http.ServeMux, handler *Handler) {
	mux.Handle("PUT /c/{slug}/secrets/{path...}", campaigns.RequireEdit(handler))
}

// ServeHTTP answers one reveal request.
//
// The order is the package comment's, and the comments on the calls are that
// order — a step that moves is a step whose comment no longer matches it.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)

	// 1. The campaign's root first, then the path inside it. Nothing about the
	// request's *body* is read before both have been settled, so a request that
	// names a path outside the campaign costs two operations rather than a body
	// upload. A slug the gate resolved but whose root is gone is an operator's
	// fault and a 500 — reporting it as a 404 would tell a GM their campaign has
	// no pages when it has no filesystem.
	root, err := h.Roots.Get(access.Campaign.Slug)
	if err != nil {
		h.writeFailure(w, r, fmt.Errorf("content root for %s: %w", access.Campaign.Slug, err))

		return
	}

	target, err := pageTarget(root, r.PathValue("path"))
	if err != nil {
		h.writeFailure(w, r, err)

		return
	}

	// 2. The precondition's *presence*, before the body. A 428 is reached without
	// a body read, so a request with no validator costs nothing.
	ifMatch := strings.TrimSpace(r.Header.Get(ifMatchHeader))
	if ifMatch == "" {
		h.writePreconditionRequired(w, r, target)

		return
	}

	// 3. The body: which secret, and which way.
	wanted, err := parseReveal(r)
	if err != nil {
		h.writeFailure(w, r, err)

		return
	}

	// 4. What is on disk now. Read after the precondition is known to be present
	// and before it is compared, because the comparison is against these bytes
	// and nothing else — S-5.2 makes validity a fact about content, so a validator
	// computed from a `stat` would be a validator about the filesystem, which a
	// sync client is free to change without the file changing.
	current, err := h.readSource(r.Context(), target)
	if err != nil {
		h.writeFailure(w, r, err)

		return
	}

	currentValidator := validator(access.Campaign.ID, target.Path(), current.hash)

	// 5. The comparison. A mismatch — including a header that cannot be parsed —
	// answers 412 with the current source and its digest and has reached neither
	// the write nor the ledger. There is no path from this branch to either, and
	// adding one would be a three-line change to a function whose every other line
	// is a comment about why it is where it is.
	if !matches(ifMatch, currentValidator) {
		h.writeConflict(w, r, target, current, currentValidator)

		return
	}

	h.reveal(w, r, target, current, wanted)
}

// reveal is the whole of §5.6.4's storage half, and its order is the package
// comment's argument.
//
// Every branch returns, and every refusal does so **before the ledger**: an unknown
// secret, a nested callout that cannot be revealed, and a failed splice all return
// before `record` is reachable, so there is no path from any of them to a disclosure
// row. Nothing here can reach the ledger without having first decided which callout it
// is about, which is the property S-5.9 needs: an anchor is resolved against the file,
// not taken from the request.
//
// # The window this route does not close
//
// The read and the write are not one atomic operation. Between `readSource` and
// `WriteFile`, a sync client can rewrite the page, and this route's rename then replaces
// whatever it wrote — a lost update, with no error and no conflict. **`If-Match` does not
// close that window**, because the comparison happened against bytes that are no longer
// the bytes on disk; it narrows the window to the length of one request, which is what
// S-6.1 and S-6.3 accept in exchange for there being no file lock.
//
// Stated rather than left implicit because the honest version of this function's
// guarantee is "the reveal lands on the page the GM was looking at when they pressed the
// button", not "the reveal is applied to whatever is on disk now". A filesystem-level
// compare-and-swap would close the window and would need `internal/content` to grow one;
// that is a decision for whoever owns it, and it is not this route's to take.
func (h *Handler) reveal(
	w http.ResponseWriter,
	r *http.Request,
	target *content.Target,
	current source,
	wanted selector,
) {
	ctx := r.Context()
	access := campaigns.AccessFrom(ctx)
	pagePath := target.Path()

	found := h.resolve(ctx, access.Campaign.ID, pagePath, current.body, wanted)
	if found.failure != nil {
		h.writeFailure(w, r, found.failure)

		return
	}

	// A reveal of a callout that nests another would write a `+` that the scanner
	// still reports as collapsed, so nothing would reach a reader while the ledger
	// recorded that the GM had disclosed it. Refusing is the direction §5.6.2
	// requires every failure path in this subsystem to resolve toward, and it is
	// the difference between a record of a disclosure and a disclosure. Hiding is
	// not refused: see the package header.
	if wanted.Revealed && found.nested {
		h.writeFailure(w, r, classify(errNestedReveal))

		return
	}

	state := content.SecretCollapsed
	if wanted.Revealed {
		state = content.SecretRevealed
	}

	// `SetMarker` re-derives the marker offset from a fresh scan rather than
	// trusting the one above, for the reason `content.SetMarker` gives: between
	// the scan and the call the file may have changed, and a stale offset would
	// rewrite a byte of somebody's prose. Passing the ordinal rather than a
	// computed byte range is what makes that re-derivation reachable at all.
	updated, err := content.SetMarker(current.body, found.ordinal, state)
	if err != nil {
		h.writeFailure(w, r, fmt.Errorf("splice the secret marker: %w", err))

		return
	}

	// The byte first, then the ledger — see the package header. And only when the
	// byte actually differs: re-revealing an already-public secret must not churn
	// the page's inode, and the ledger write below is idempotent and writes no
	// second audit row, so a double click records one disclosure rather than two.
	if updated != current.body {
		if err := target.WriteFile(ctx, []byte(updated), pageFileMode); err != nil {
			// Nothing has been recorded, so this is unambiguously toward hiding:
			// the ledger holds no row, so reconciliation has nothing to re-apply.
			// `content`'s own documentation says what a failed write means —
			// *durability unknown*, not *not written* — so the log has to carry it.
			h.log(ctx, slog.LevelError, "secrets.write_failed",
				slog.String("campaign", access.Campaign.Slug),
				slog.String("path", pagePath),
				slog.String("anchor", found.anchor),
				slog.String("error", err.Error()),
			)
			h.writeFailure(w, r, err)

			return
		}
	}

	// The record, and with it the audit row: `store.RevealSecret` writes both in
	// one transaction, so a 500 here means *no row at all* and therefore nothing
	// for reconciliation to re-apply. The status is a 500 rather than a 412
	// because the precondition did match — telling the GM their reveal conflicted
	// would send them to reconcile a conflict that does not exist — and rather
	// than a 204 because the byte may now be public and the ledger does not say
	// who revealed it. A retry converges; see the package header.
	if err := h.record(
		ctx,
		access.Campaign.ID,
		pagePath,
		found.anchor,
		wanted.Revealed,
	); err != nil {
		h.log(ctx, slog.LevelError, "secrets.ledger_failed",
			slog.String("campaign", access.Campaign.Slug),
			slog.String("path", pagePath),
			slog.String("anchor", found.anchor),
			slog.Int("revealed", boolToInt(wanted.Revealed)),
			slog.String("error", err.Error()),
		)
		h.writeFailure(w, r, err)

		return
	}

	h.log(ctx, slog.LevelInfo, "secrets.revealed",
		slog.String("campaign", access.Campaign.Slug),
		slog.String("path", pagePath),
		slog.String("anchor", found.anchor),
		slog.String("form", found.form.String()),
		slog.Int("ordinal", found.ordinal),
		slog.Bool("revealed", wanted.Revealed),
		slog.Bool("changed", updated != current.body),
	)

	h.writeRevealed(w, validator(access.Campaign.ID, pagePath, contentHash([]byte(updated))))
}

// record writes the ledger row, and the audit row with it.
//
// A pass-through rather than a branch, so the call site reads as one call and the
// two directions cannot drift into two spellings of one thing. The `userID` comes
// from the gate's resolved requestor rather than from anything in the request, so a
// client cannot attribute its own disclosure: `audit_log.actor_id` and
// `secrets_revealed.revealed_by` name the account that was authenticated.
func (h *Handler) record(
	ctx context.Context,
	campaignID int64,
	pagePath, anchor string,
	revealed bool,
) error {
	if revealed {
		if _, err := h.Ledger.RevealSecret(
			ctx, campaignID, pagePath, anchor, campaigns.Requestor(ctx).UserID,
		); err != nil {
			return fmt.Errorf("record the reveal: %w", err)
		}

		return nil
	}

	if err := h.Ledger.UnrevealSecret(
		ctx, campaignID, pagePath, anchor, campaigns.Requestor(ctx).UserID,
	); err != nil {
		return fmt.Errorf("record the unreveal: %w", err)
	}

	return nil
}

// source is a page's bytes as they were read, and its digest.
//
// A local copy of the same idea as `edit`'s `source` and `wiki`'s, and the reason it
// is not shared is the same as for the failure classification: the type is unexported
// there, and a route does not import a sibling route. The digest is taken here, over
// the bytes as read, because S-5.2 makes validity a fact about content and a `stat`
// answers a question about the filesystem, which a sync client is free to change
// without the file changing.
//
// **It holds the whole page, secrets and all, and it is a local.** It never becomes a
// log line, never reaches a response except in the 412's `content` field — to a caller
// the gate admitted as the campaign's GM — and never outlives the request. That is the
// S-12.3 boundary: this type is the one place on this route where a `[!secret]` body
// legitimately exists in memory, and it exists because the byte that hides it lives in
// these same bytes.
type source struct {
	body string
	hash string
}

// readSource reads a page's bytes through the confined root and digests them.
//
// `Target.Open` rather than `ReadFile` so the size cap applies while the bytes
// arrive: applying it afterwards bounds the parser but not the buffer, and the file
// came from a directory a sync client writes to. The close is checked rather than
// deferred and dropped, for the reason `edit`'s and `wiki`'s are: a read handle left
// open on a watcher-driven tree is a descriptor leak on a server that never restarts.
func (h *Handler) readSource(ctx context.Context, target *content.Target) (source, error) {
	file, err := target.Open()
	if err != nil {
		return source{}, classify(err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			h.log(ctx, slog.LevelWarn, "secrets.page_close_failed",
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

// selector is which secret a request is about, and which way it wants it.
//
// An anchor or an ordinal rather than a required pair: §5.6.3's two names differ in
// what they survive, and a client that has one should not be made to compute the other.
//
// **The anchor wins when both are present**, and the reason is stability rather than
// preference. A derived anchor is `sha256(campaign_id, path, ordinal,
// first-line-of-body)`, so it survives an edit anywhere below the first body line and
// dies on a reorder or an insertion above it; the ordinal survives nothing at all.
// Resolving the anchor first therefore *prefers the more stable name*, and the two cases
// in which the two disagree are exactly the cases in which trusting the ordinal would
// rewrite the wrong callout's byte. `resolve` is where that decision is made, and
// `TestTheAnchorIsPreferredOverTheOrdinal` holds it in both directions — a wrong anchor
// with a right ordinal is a 404, not a success.
type selector struct {
	// Anchor is the block id or derived hash the client resolved, or empty.
	Anchor string
	// Ordinal is the callout's position among the page's secrets, and is only
	// meaningful when `Anchor` is empty.
	Ordinal int
	// Revealed is which way the marker goes. A plain `bool` here and a `*bool` in the
	// wire type, so a request that simply omits it is distinguishable from one that
	// said `false` — see `revealBody.Revealed` and `parseReveal`.
	Revealed bool
}

// found is the callout a request resolved to, and everything the write needs about
// it that the request did not supply.
type found struct {
	// ordinal is the callout's position, which is what `content.SetMarker` takes.
	ordinal int
	// anchor is the **resolved** name for the ledger key, never the client's
	// string. They are equal by construction — `resolve` matched the client's
	// value against exactly this derivation — and the point of stating it is that
	// the key must come from the file rather than from the request: an anchor that
	// was not verified against the page is a name nothing can re-find later.
	//
	// **A mutation that substitutes the client's string is unobservable**, and that is
	// the property rather than a gap in a test: `resolve` matches an anchor only by
	// comparing it to `content.Resolve(...).Value`, so the two spellings cannot differ
	// for any input that reached here. The property a reader actually needs — that the
	// key is the resolver's name and not a string off the wire — is asserted directly
	// by `TestADerivedAnchorIsRecordedWhenTheAuthorWroteNoBlockID`, which recomputes
	// `content.Resolve` in the test and compares.
	anchor string
	// form is which of §5.6.3's two names it is, logged and never guessed from the
	// string's shape.
	form content.AnchorForm
	// nested reports whether another callout sits inside this one's body, which is
	// what makes a reveal of it a refusal. See `hasNested`.
	nested bool
	// failure is the classified refusal, or nil.
	failure error
}

// resolve finds the callout a request names, or the refusal that answers.
//
// **The request never names a callout; it names an anchor or a position, and this
// function is the only thing that turns either into one.** So a request that names
// nothing findable is refused here, before the write and before the ledger, and
// both of those are downstream of a non-nil `failure`.
//
// A first match rather than a "the anchors are unique" check, because
// `content.Reassociate` resolves an anchor to the first callout that carries it and
// a second rule here would let a reveal and its reconciliation disagree about which
// callout an anchor means. A page with two callouts sharing one block id is a fault
// in the vault, and resolving it consistently is better than refusing the write.
//
// **The lookup is a linear scan with a `sha256` per candidate**, which sounds worse
// than it is and is worth stating rather than optimising: it runs on a GM's click, not
// on a request path, `content.Resolve` derives a twelve-character digest rather than
// anything larger, and a page holding enough callouts for this to be noticeable is a
// page a human did not write. Caching the per-campaign anchors would save a few hundred
// nanoseconds on a page render that takes milliseconds.
func (h *Handler) resolve(
	ctx context.Context,
	campaignID int64,
	pagePath, body string,
	wanted selector,
) found {
	secrets := content.ScanSecrets(body)

	if len(secrets) == 0 {
		return found{failure: classify(errUnknownSecret)}
	}

	index := -1

	if wanted.Anchor != "" {
		for at, secret := range secrets {
			if content.Resolve(campaignID, pagePath, secret).Value == wanted.Anchor {
				index = at

				break
			}
		}
	} else {
		index = wanted.Ordinal
	}

	// One bound, for both directions, and it is `content`'s own rule: an ordinal is
	// a position among the page's callouts and an anchor is a name one of them
	// carries, so "not found" is the same fact either way. A negative ordinal lands
	// here too, which is correct — a request naming position −1 named nothing.
	if index < 0 || index >= len(secrets) {
		return found{failure: classify(errUnknownSecret)}
	}

	anchor := content.Resolve(campaignID, pagePath, secrets[index])

	h.log(ctx, slog.LevelDebug, "secrets.resolved",
		slog.String("anchor", anchor.Value),
		slog.Int("ordinal", index),
		slog.String("form", anchor.Form.String()),
	)

	return found{
		ordinal: index,
		anchor:  anchor.Value,
		form:    anchor.Form,
		nested:  hasNested(secrets, index),
	}
}

// hasNested reports whether a callout contains another one.
//
// **Containment, read off one scan, and it is the same relation
// `content/redact.go`'s `outermost` filters on** — "a nested span is contained in its
// parent's by construction, so cutting both would either double-cut or cut a range
// that has already moved". Reading it here rather than re-deriving the grammar means
// this route and the redactor agree about what "nested" means by construction, which
// is the property that matters: a reveal refused here is exactly a reveal whose bytes
// the redactor was going to cut whole.
//
// **It cannot be read off the reported state, and the reason is worth stating** because
// the obvious one-liner does not work. `content.ScanSecrets` forces a callout
// containing another to `SecretCollapsed` "whatever its marker byte says", so a `-` on
// disk and a forced `-` are indistinguishable — the very common case, where the outer
// is collapsed and nested, would not be detected at all. The forced state is a *report*;
// nesting is a *shape*, and the shape is what this reads.
//
// Linear rather than a containment scan per callout, and it runs once per request
// rather than once per secret: the loop is `O(n²)` over a page's callouts, which is a
// few dozen comparisons on any page a human writes, and a route that runs on a GM's
// click does not need the index.
func hasNested(secrets []content.Secret, index int) bool {
	outer := secrets[index]

	for at, candidate := range secrets {
		if at == index {
			continue
		}

		if candidate.BodyStart >= outer.BodyStart && candidate.BodyEnd <= outer.BodyEnd {
			return true
		}
	}

	return false
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
		// The route matched `/c/{slug}/secrets/` with nothing after it. A path that
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

// log writes one line, discarding it when no logger is configured.
//
// The context is threaded rather than replaced: the request's values are what tie
// this line to the access-log entry for the same request, and a log line that
// cannot be correlated with the request it describes is the failure slog exists to
// prevent.
//
// **Every attribute here is safe to log, and the one that is not obvious is the
// anchor.** `content.Secret` carries the author's `Title` and `Body`, and neither
// appears in any line this route writes: S-12.3 forbids an event or an error
// carrying page content, and a `[!secret]` header line is page content. The anchor
// is logged because it is not: it is either twelve hex characters of a digest or an
// identifier from `content`'s `[-_A-Za-z0-9]` block-id rule, and `store.RevealSecret`
// already persists it in `audit_log.detail`, so a log line is the less durable of
// the two places it is written.
func (h *Handler) log(ctx context.Context, level slog.Level, event string, attrs ...slog.Attr) {
	if h.Logger == nil {
		return
	}

	h.Logger.LogAttrs(ctx, level, "secrets",
		append([]slog.Attr{slog.String("event", event)}, attrs...)...)
}

// reportConflict records a refused reveal through `observability`, which is the only
// spelling `conflict.412` has anywhere in the project.
//
// Through `observability.Event` rather than `h.log`, and the reason is S-12.3: the
// `EventAttributes` type has no field a page body could be passed through, so an
// event emitted through it cannot carry file contents. The two attributes written
// here are the campaign's id and the page's path — the two S-12.3 names — and the
// `Detail` is a discriminator rather than a sentence, which is that type's contract.
// `Detail` is `"stale_if_match"` and not the anchor: an anchor is a key here, and
// this line exists to say how often a precondition fails rather than which secret a
// GM was reaching for.
//
// **No new event name is registered for the reveal itself**, deliberately.
// `observability.AllEventNames()` has a test asserting its length, so registering
// one is a change to `internal/observability` — another package's — and none of the
// three secret events §13.2 names describes this act. The success line goes through
// `h.log` above, which is `slog` plus a counter's worth of `event` attribute; see
// the phase PR for the decision about whether a reveal deserves a registered name.
func (h *Handler) reportConflict(ctx context.Context, access campaigns.Access, pagePath string) {
	observability.Event(ctx, h.Logger, observability.EventConflict412, slog.LevelWarn,
		observability.EventAttributes{
			CampaignID: strconv.FormatInt(access.Campaign.ID, 10),
			Path:       pagePath,
			Detail:     "stale_if_match",
		},
	)
}

// boolToInt is a bool as the 0/1 an integer attribute wants.
//
// `slog.Bool` exists and `slog.Int` on a bool would not compile, so this is only
// here for the one attribute where the *absence* of the value would be worse than a
// number: a failed ledger write is logged, and "revealed=0" is a fact about it
// while an omitted attribute is a question.
func boolToInt(value bool) int {
	if value {
		return 1
	}

	return 0
}
