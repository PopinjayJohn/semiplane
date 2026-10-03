package content

// `[!secret]` callouts, as they appear in the **source**.
//
// # Why the scanner, and why here rather than in the renderer
//
// §5.6.1 is the constraint that decides the shape of this file. Redaction runs on
// the source, before the render, so a non-GM response never constructs the string
// — see `redact.go` for why every other position is worse. That means the thing
// the pipeline needs is not "a renderer that knows about secrets" but **a scanner
// that can find a callout's byte span in the author's file**, because omission
// means cutting that span out and never rendering it.
//
// A goldmark extension cannot do this. By the time goldmark has parsed, the
// source offsets are gone and the blockquote has been assembled from lines whose
// original bytes — including the callout header — this package would have to
// recover by guessing. So:
//
//   - **`internal/content/secret.go` (this file)** finds the callouts, their state,
//     their ordinal, their block id and their byte span, and rewrites the marker.
//     It is the substrate for S3 (anchors), S5 (redaction), S6 (the reveal
//     endpoint) and S7 (reconciliation).
//   - **`internal/content/ext/secret.go`** renders one for a GM. It never decides
//     *whether* to render it; by the time it runs, the callout is either present
//     in the document or it is not.
//
// Splitting it that way is what makes §5.6.1 structural rather than a convention:
// a caller cannot reach the renderer with a secret a viewer may not see, because
// the only path to the renderer is a source string that has already been through
// the redactor.
//
// # The grammar, and the one byte that is the state
//
// ```markdown
// > [!secret]-
// > The traitor is Captain Aldric.
//
// > [!secret]+ Revealed to the party   ^traitor
// > *He replaced the eastern signal fire.*
// ```
//
// `-` is a secret, collapsed and GM-only; `+` is revealed and public. **The single
// character is the whole edit** (§5.6), which keeps a reveal's diff to one byte
// and minimises the window for a collision with Obsidian's own line handling.
//
// Everything after the marker is optional and is *not* state: a title on the
// header line, and an Obsidian block id (`^traitor`) at the end of it. A callout
// with neither is legal and is anchored by derivation (S3).
//
// # What this parser deliberately does not do
//
// It does not resolve or judge. A `[!secret]` inside a fenced code block is text,
// and so is one inside an inline code span — `TestSecretsInCodeAreText` holds it,
// because "documented in the design record" is not a test.
//
// It also never fails. A page mentioning `[!secret]` in prose, or a callout with
// no body, still renders: the alternative is a page destroyed by a typo in a
// marker, and the failure would be indistinguishable from a vault corruption.
//
// # Nesting, which this file used to describe wrongly
//
// This header used to say that a callout inside another callout "is found by its
// own header and reported separately, because a secret nested in a secret has no
// state a viewer could act on". **The claim was false and the reasoning inverted.**
// The body loop below consumed every line that was still quoted, so a nested
// callout was swallowed by its parent, never reported, and — once S5's redactor put
// this scanner on the request path — a `[!secret]-` inside a revealed `[!secret]+`
// reached a player as a `secret--collapsed` callout with its body in it. The reason
// the old sentence gave is the opposite of the reason the code needed: the nesting
// does have state a viewer can act on, because the outer callout's own state is the
// thing that decides whether anybody but a GM ever sees those bytes.
//
// So the scanner recurses, and **an outer callout that contains one is collapsed
// whatever its own marker byte says**. There is no rendering of a revealed outer
// callout that withholds the nested body, so a GM who wrote `+` over a `-` made a
// mistake and this subsystem resolves it the way §5.6.2 requires every failure path
// to resolve — toward hiding. An accidentally revealed secret is a far worse
// outcome than a reveal that arrives late or not at all.
//
// The nested callout is reported **as well**, with its own span, ordinal and body,
// rather than being folded into the parent. Three things need it to exist:
//
//   - A ledger row has to be able to name it. §5.6.3's resolution order runs over
//     the scanner's output, and a secret the scanner does not report is a secret
//     nothing downstream can refer to.
//   - Two nesting levels are representable, and only reporting the leaf lets a
//     caller walk outward.
//   - It is what makes the spans *nest*, which is the shape `redact.go` has to cut
//     correctly. `TestTheOutermostSpansAreTheOnesThatGetCut` is that property, and
//     it is stated as a property of the redactor rather than of the scanner on
//     purpose: it is what makes the redactor safe against a scanner that does
//     report nesting, whether or not the one it is handed today does.
//
// What this does **not** change: the file is not rewritten. A nested `+` stays `+`
// on disk, so `SetMarker` and S7's reconciliation read the byte the author wrote.
// Only the *reported* state is forced. That is deliberate — rewriting an author's
// prose as a side effect of indexing a page is not this package's business — and it
// leaves a real gap for S6/S7 to close, which is that revealing a callout that
// nests another will report itself as revealed and render as collapsed.
//
// The false sentence is left in this header deliberately rather than deleted, so
// that the next reader who remembers it finds the correction beside it.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// SecretState is whether a secret is currently revealed, and it is the state of
// **one byte in the author's file**.
//
// # The file is authoritative, and this type is why that is not a convention
//
// §5.6.2's precedence table puts "is this secret revealed right now?" to the
// file and "who revealed it, when, and was it reverted?" to `secrets_revealed`.
// `content` cannot see the table — it is above this package's reach and has no
// business reaching it — so the split is expressed where the two halves meet:
// this type answers the file's question, and `store.SecretReveal` answers the
// other's. A caller that wants to know *whether* a secret is revealed reads this;
// a caller that wants to know *who* disclosed it reads the ledger.
type SecretState byte

const (
	// SecretCollapsed is `[!secret]-`: present, hidden from non-GMs.
	SecretCollapsed SecretState = '-'

	// SecretRevealed is `[!secret]+`: public.
	SecretRevealed SecretState = '+'
)

// IsRevealed reports whether the secret is currently public.
func (state SecretState) IsRevealed() bool { return state == SecretRevealed }

// String is the marker as it is written, so a caller round-tripping a state into
// a file writes the byte the author would have written.
func (state SecretState) String() string { return string(rune(state)) }

// Secret is one `[!secret]` callout, located in the source.
//
// **Byte offsets, not line numbers.** §5.6.2's reconciliation rewrites the
// marker through the ordinary atomic write, and a write needs a byte range to
// splice. Line numbers would mean re-deriving them from a possibly-different
// read, and a reveal applied to the wrong bytes is a secret disclosed to a player.
type Secret struct {
	// State is the marker's byte.
	State SecretState

	// Ordinal is this callout's position among the secrets on the page, counting
	// from zero in document order.
	//
	// §5.6.3's derived anchor hashes it, and the reconciliation's fallback
	// re-association uses it, so it is part of a secret's identity rather than a
	// convenience for a caller that happens to be iterating.
	Ordinal int

	// BlockID is the Obsidian block id from the header line's trailing `^name`,
	// or empty when the author supplied none.
	//
	// **Empty is not the same as "no anchor".** §5.6.3's resolution order puts
	// the block id first and the derived hash second, and S3 owns the second. This
	// package reports which of the two it found and refuses to guess: a derived
	// anchor needs the campaign id, which is not a property of a page's text, and
	// inventing one here would put a hash in two places.
	BlockID string

	// Title is the header line's text after the marker, trimmed, or empty.
	Title string

	// Body is the callout's body with the `>` markers and one space of quoting
	// removed, joined with newlines.
	//
	// The author's own text, unescaped and unrendered. **It is a field on this
	// type and that is deliberate**: S5's redactor cuts the span and never reads
	// it, and §5.6.1 is about what reaches a response rather than about what this
	// package is capable of holding. A field nothing needed would be a secret in
	// a struct that logs itself.
	Body string

	// MarkerOffset is the byte offset of the state byte itself — the one byte a
	// reveal rewrites. `HeaderStart` and `HeaderEnd` bracket the whole header
	// line including its `> ` quoting, for a caller that needs to replace the
	// line rather than the byte.
	MarkerOffset int
	HeaderStart  int
	HeaderEnd    int

	// BodyStart and BodyEnd bracket the body lines, excluding the trailing
	// newline. They are equal for a callout with no body, which is legal.
	BodyStart int
	BodyEnd   int
}

// calloutPrefix is the marker an Obsidian callout carries, and it is a constant
// for the reason every hook in this repository is a constant: a marker spelled two
// ways is a marker that appears twice, and the second one is invisible.
const calloutPrefix = "[!secret]"

// scanSecrets is the single walker, and `ScanSecrets` and `SetMarker` are both
// built on it.
//
// One walker rather than two passes because the two answers have to agree: a
// `SetMarker` that found a different callout than a `ScanSecrets` would rewrite a
// byte that is not the marker's, and that is a disclosure.
//
// **It recurses, once per level of nesting.** `scanRange` walks a byte range
// looking for callouts and, when it finds one, scans that callout's own body for
// the next level down. Two consequences, and the second is the point:
//
//   - A nested callout is reported as its own `Secret`, with its own span, ordinal
//     and unquoted body, rather than being absorbed. See the package header for why
//     the previous version's claim to the contrary was both false and backwards.
//   - A callout whose body contains one comes back **forced to `SecretCollapsed`**.
//     Not because the grammar says so — the grammar says `-` is a secret and `+` is
//     public — but because there is no rendering of a public outer callout that
//     withholds the nested body, so a public outer is a disclosure with no
//     equivalent that does not disclose. Every other failure path in this subsystem
//     resolves toward hiding for the same reason (§5.6.2), and the outer callout's
//     own marker byte is the only thing standing between a GM's typo and a player's
//     view of the secret.
//
// Ordinals are assigned once, at the end, over the whole pre-order walk. Pre-order
// is document order here because a parent's header always precedes its child's, and
// the subtrees are entered left to right — which is what makes `Ordinal` and
// `data-ref-index` the same kind of number everywhere else in the package.
func scanSecrets(source string) []Secret {
	found := scanRange(source, 0, len(source), 0)

	for index := range found {
		found[index].Ordinal = index
	}

	return found
}

// scanRange finds the callouts in `source[from:to]` that sit at nesting level
// `nesting`, together with everything nested inside them. Level 0 is the top of the
// document; level *n* is inside *n* callouts.
//
// **The range is a hard bound**, and it is what makes recursion safe rather than
// merely terminating: a nested walk cannot run past its parent's body, so a nested
// callout's span is always contained in the outer one. That containment is the
// property `redact.go`'s `outermostSpans` relies on, and it is why this is a range
// walk and not a second pass over the whole source that would have to rediscover
// which callouts belong to which.
func scanRange(source string, from, limit, nesting int) []Secret {
	var found []Secret

	var openFence string

	offset := from

	for offset < limit {
		lineEnd := strings.IndexByte(source[offset:limit], '\n')
		if lineEnd < 0 {
			// **The rest of the range, not the length of the source.** A file
			// whose last line has no trailing newline takes this branch, and
			// `len(source)` here made `offset+lineEnd` run past the end of the
			// string for every line after the first — so `ScanSecrets` panicked
			// with a slice-bounds error on `"Before.\nHello"`, and so did
			// `SetMarker`, `Reassociate`, and S5's redactor, which is what found
			// it. Only a file that is a single unterminated line survived, because
			// there `offset` is zero.
			//
			// The body's own loop below already spells this the right way, which is
			// the reason this was easy to miss: the two lines read as a pair, and
			// one of the pair is wrong.
			lineEnd = limit - offset
		} else {
			lineEnd += 1 // keep the newline
		}

		// The trailing newline can push the line one past `limit` when `limit` lands
		// mid-line, which it does whenever a parent ended its body on the last
		// byte of a line. Clamped rather than trusted, because a slice out of range
		// here is a crash on the request path.
		if offset+lineEnd > limit {
			lineEnd = limit - offset
		}

		raw := source[offset : offset+lineEnd]
		trimmed := strings.TrimRight(raw, "\r\n")

		if marker, isFence := fenceLine(trimmed); isFence {
			// A fence closes on the same character that opened it, and the run
			// must be at least as long: an opening ```` and a closing ``` does not
			// close it, which is CommonMark's rule and the reason a document that
			// opens one fence character and closes another has an *unterminated*
			// fence rather than a second one.
			switch {
			case openFence == "":
				openFence = marker
			case strings.HasPrefix(openFence, marker):
				openFence = ""
			}

			offset += lineEnd

			continue
		}

		// A callout inside a fence is text, and so is one inside an indented
		// code block — which is why this is checked before the header is.
		if openFence == "" {
			// **`nesting + 1`, and the `+1` is the whole of the grammar.** `nesting`
			// counts the *enclosing callouts*; the `> ` that makes a call out of a
			// quoted paragraph is one more level than that. So a top-level callout's
			// header line carries one level of quoting and a callout nested in it
			// carries two.
			//
			// It is also what keeps an unquoted line from being a callout: at zero
			// levels `unquoteDepthLine` succeeds on any line at all, so passing `nesting`
			// straight through would make a bare `[!secret]-` in a paragraph a secret
			// — the one thing `parseCalloutHeader`'s "must be quoted" rule exists to
			// prevent.
			if secret, ok := parseCalloutHeader(trimmed, offset, nesting+1); ok {
				secret.BodyStart = offset + lineEnd
				secret.BodyEnd = offset + lineEnd

				// The body runs while the lines keep quoting, which is what makes
				// a callout a callout rather than a one-line paragraph. A blank
				// line *inside* the quote does not end it, because Obsidian keeps
				// the `>` on those lines; a line with no `>` at all does.
				consumed := lineEnd

				for offset+consumed < limit {
					bodyLineEnd := strings.IndexByte(source[offset+consumed:limit], '\n')
					if bodyLineEnd < 0 {
						bodyLineEnd = limit - offset - consumed
					} else {
						bodyLineEnd += 1
					}

					body := strings.TrimRight(
						source[offset+consumed:offset+consumed+bodyLineEnd], "\r\n",
					)

					if !isQuote(body) {
						break
					}

					secret.BodyEnd = offset + consumed + bodyLineEnd
					consumed += bodyLineEnd
				}

				secret.Body = unquoteDepth(
					strings.TrimRight(
						source[secret.BodyStart:secret.BodyEnd], "\r\n",
					),
					nesting+1,
				)

				// **Into the body, one level deeper, before the parent is recorded.**
				// The order matters: `found` holds `Secret` values, so appending
				// `secret` first and then forcing its state would leave the forced
				// copy behind and the appended one carrying the author's own byte —
				// a redaction decision that was computed and then thrown away, and
				// one that fails open. It was written the wrong way round first and
				// `TestTheOuterCalloutIsCollapsedWhateverItsMarkerByte` is what caught
				// it.
				nested := scanRange(source, secret.BodyStart, secret.BodyEnd, nesting+1)

				if len(nested) > 0 {
					// The one line in this file that overrides the grammar. The comment
					// above says why; the reason it is here and not in
					// `parseCalloutHeader` is that the header parser stays a pure
					// reading of one line, so there is no second answer to "what does
					// this line say" for `SetMarker`'s offsets to disagree with.
					secret.State = SecretCollapsed
				}

				found = append(found, secret)

				// The nested results come after the parent, which is document order
				// and is what the ordinal pass in `scanSecrets` relies on.
				found = append(found, nested...)

				offset += consumed

				continue
			}
		}

		offset += lineEnd
	}

	return found
}

// ScanSecrets finds every `[!secret]` callout in source, in document order.
//
// **Never an error.** A page with a malformed marker still has a page, and a
// parser that refused the document would turn a typo into a 500 — which, on a
// page a GM cannot read to find the typo, is the worst possible failure. The
// malformed forms are covered by `TestAMalformedMarkerIsNotACallout` and they are
// simply not callouts.
func ScanSecrets(source string) []Secret {
	return scanSecrets(source)
}

// parseCalloutHeader reads one line and answers whether it opens a callout quoted
// exactly `depth` levels deep.
//
// **Four shapes accepted, and the fourth is the interesting one.** `> [!secret]-`,
// `> [!secret]+`, either with a title after the marker, and either with a trailing
// `^block-id`. What is *not* accepted is what looks most like an attack: a line
// with a second `[!secret]` in it, or a `[!secret]` that is not at the start of
// the quoted line. Obsidian requires the marker at the start of the first
// paragraph, and matching anywhere else would let a sentence mentioning the
// keyword become a secret with a body.
//
// The depth is a parameter rather than a constant for two reasons, and the second
// one is a limitation rather than a feature.
//
// First, the marker byte's offset depends on how many bytes of quoting precede it,
// so a nested callout's marker has to land on the right byte for `SetMarker` like
// any other. See the `MarkerOffset` comment below, which is an off-by-one this
// change fixes.
//
// Second, it *restricts* where a callout may be found rather than merely measuring.
// A caller asks for the quoting level its callout is at, and `scanRange` is what
// recurses to ask one level deeper. So a callout inside a plain block quote, with
// no callout around it, is still not found: a lone `> > [!secret]-` is two nested
// block quotes, and the inner one is a paragraph rather than a callout. The
// recursion follows the grammar, and the grammar's callout is the outermost quote.
//
// Note what that restriction costs and why it is not fixed here: a `[!secret]` two
// levels deep inside ordinary quoted prose is invisible to this scanner, so a
// secret written that way renders as a plain block quote. Widening the walk to
// every quoting depth would find it, and would also mean deciding what a callout
// inside a callout inside a plain quote *is* — a third grammar question with no
// answer in §5.6. Recorded rather than silently left.
//
// It reads a line and returns what the line says. **It does not judge**, and in
// particular it does not force `SecretCollapsed` for a callout with something
// nested inside it: that is the walker's decision, made in `scanRange` where the
// nested results are in hand. A header parser that also overrode the state would be
// a second place answering "what is this callout's state", and the two would
// disagree the first time either was changed.
func parseCalloutHeader(line string, lineStart, depth int) (Secret, bool) {
	quoted, quotedAt, ok := unquoteDepthLine(line, depth)
	if !ok {
		return Secret{}, false
	}

	rest, ok := strings.CutPrefix(quoted, calloutPrefix)
	if !ok {
		return Secret{}, false
	}

	if rest == "" {
		return Secret{}, false
	}

	state := SecretState(rest[0])
	if state != SecretCollapsed && state != SecretRevealed {
		return Secret{}, false
	}

	secret := Secret{
		State:       state,
		HeaderStart: lineStart,
		HeaderEnd:   lineStart + len(line),
		// **`quotedAt + len(calloutPrefix)`, not `1 + 1 + len(calloutPrefix)`.**
		// The old constant assumed every line is `> ` — two bytes — which is wrong
		// for `>[!secret]-` with no space after the marker, and for every level of
		// nesting beyond the first. That was a live off-by-one in `MarkerOffset` on a
		// legal callout, and it is the byte `SetMarker` rewrites: a reveal applied to
		// the wrong offset is a secret disclosed to a player. Fixed by the same
		// change that made `depth` a parameter, and held by
		// `TestSetMarkerFindsTheMarkerWithoutASpaceAfterTheQuote`.
		MarkerOffset: lineStart + quotedAt + len(calloutPrefix),
	}

	remainder := strings.TrimSpace(rest[1:])

	// The block id is a **trailing** `^name`, and trailing is the whole rule: a
	// `^` in the middle of a title is a caret in a title. §5.6.3 spells it at the
	// end of the header line, and Obsidian's own block ids are always last.
	if at := strings.LastIndex(remainder, " ^"); at >= 0 {
		candidate := strings.TrimSpace(remainder[at+2:])
		if isBlockID(candidate) {
			secret.BlockID = candidate
			remainder = remainder[:at]
		}
	}

	secret.Title = strings.TrimSpace(remainder)

	return secret, true
}

// SetMarker rewrites one secret's marker byte and returns the new source.
//
// **One byte, or it does not return.** §5.6's reason for the design is that a
// reveal is a single character so the diff is one byte and the collision window
// with Obsidian's own line handling is as small as it can be; a function that
// rewrote the line, or normalised CRLF while it was there, would break that. So
// this splices exactly `MarkerOffset..MarkerOffset+1` and leaves every other byte
// of the author's file alone — including its line endings.
//
// The offset is re-derived from a **fresh scan** rather than reused from the
// caller's `Secret`, because between the caller's scan and this call the file may
// have changed. §5.6.2 step 3 is an `If-Match` write, so the caller re-checks the
// precondition; re-deriving here is the same instinct one layer down, and a stale
// offset would rewrite a byte of somebody's prose.
func SetMarker(source string, ordinal int, state SecretState) (string, error) {
	if state != SecretCollapsed && state != SecretRevealed {
		return "", &InvalidSecretMarkerError{Marker: string(rune(state))}
	}

	secrets := scanSecrets(source)
	if ordinal < 0 || ordinal >= len(secrets) {
		return "", &UnknownSecretOrdinalError{Ordinal: ordinal, Found: len(secrets)}
	}

	secret := secrets[ordinal]

	return source[:secret.MarkerOffset] +
		string(rune(state)) +
		source[secret.MarkerOffset+1:], nil
}

// InvalidSecretMarkerError is a marker byte that is neither `-` nor `+`.
type InvalidSecretMarkerError struct{ Marker string }

func (err *InvalidSecretMarkerError) Error() string {
	return "content: " + err.Marker + " is not a secret marker; the two are - and +"
}

// UnknownSecretOrdinalError is an ordinal with no callout behind it.
type UnknownSecretOrdinalError struct {
	Ordinal int
	Found   int
}

func (err *UnknownSecretOrdinalError) Error() string {
	return "content: secret " + itoa(int64(err.Ordinal)) + " does not exist; the page has " +
		itoa(int64(err.Found))
}

// DerivedAnchor is §5.6.3's fallback: `sha256(campaign_id, path, ordinal,
// first-line-of-body)[:12]`.
//
// **In `content`, and not in `store`, because it is a property of a secret's
// position in a page and not of the ledger.** S2 deliberately gave the ledger an
// opaque `anchor` column with no CHECK, on the argument that the resolver owns
// the shape; this is that resolver.
//
// The shape of the input matters and is stated here because it is the part a later
// reader could get wrong: the fields are joined with a byte that cannot appear in
// a path or an ordinal, so `("a", "b", 1)` and `("a/b", "", 1)` cannot collide.
// Without a separator the hash is a function of a concatenation, and a
// concatenation has more preimages than its parts.
func DerivedAnchor(campaignID int64, path string, secret Secret) string {
	firstLine := secret.Body
	if at := strings.IndexByte(firstLine, '\n'); at >= 0 {
		firstLine = firstLine[:at]
	}

	firstLine = strings.TrimSpace(firstLine)

	digest := sha256.New()
	for _, field := range []string{
		itoa(campaignID),
		path,
		itoa(int64(secret.Ordinal)),
		firstLine,
	} {
		digest.Write([]byte(field))
		digest.Write([]byte{0})
	}

	return hex.EncodeToString(digest.Sum(nil))[:12]
}

// unquoteDepthLine strips `depth` levels of `>` quoting from one line, and answers
// whether the line carried that much quoting at all.
//
// **The offset comes back with the text, and that is the reason this function
// exists in the shape it does.** Every byte stripped is a byte the caller's
// `MarkerOffset` has to skip to land on the state character, and a nested callout's
// marker is two quoting levels in with an indentation on top. Computing the offset
// as `depth * 2` — or as the constant `2` this replaced — is an off-by-one waiting
// for the first line that is not exactly `> `, and the byte it lands on is the one
// `SetMarker` rewrites.
//
// The rules are CommonMark's and Obsidian's: `>` followed by an **optional** space.
// A line of `>>>` at depth one quotes to `>`, and a line of `>` quotes to the empty
// string — so an empty callout body line is `>` or `>` plus a space, and both must
// be recognised as *continuing* the callout rather than ending it. That is why
// `isQuote` asks "is this line quoted at all" rather than "does this line have
// content".
func unquoteDepthLine(line string, depth int) (string, int, bool) {
	quotedAt := 0

	for range depth {
		// Leading spaces are quoting-neutral padding, not part of the content, so
		// they are consumed without counting as a level — which is also why `at`
		// below counts them for the offset.
		for quotedAt < len(line) && line[quotedAt] == ' ' {
			quotedAt++
		}

		if quotedAt >= len(line) || line[quotedAt] != '>' {
			return "", 0, false
		}

		quotedAt++

		// The optional space, and **a tab counts** — CommonMark allows either, and
		// Obsidian reads `>\t[!secret]-` as a callout. The function this replaces had a
		// comment saying it ate tabs and code that stripped spaces only, so
		// `>\t[!secret]-` was not a callout here while being one everywhere else: a
		// secret written with a tab rendered as a plain block quote for every reader.
		//
		// Recognising one more shape is the safe direction. The check is for the
		// optional space only — a second `>` is left in place, because `>>> ` is a
		// *deeper* quote and is what `depth` is counting.
		if quotedAt < len(line) && (line[quotedAt] == ' ' || line[quotedAt] == '\t') {
			quotedAt++
		}
	}

	end := len(line)
	if end > quotedAt && line[end-1] == '\r' {
		end--
	}

	return line[quotedAt:end], quotedAt, true
}

// unquoteDepth removes `depth` levels of quoting from every line of a body.
//
// **A line that carries fewer levels than asked for is left alone**, which is what
// keeps a nested body readable: the last line of a nested body that runs out of
// `>` is already the author's text, and stripping nothing is right. The top-level
// case never gets here — `unquoteDepth(body, 1)` on a body every line of which is
// quoted is `unquote(body)` exactly.
func unquoteDepth(body string, depth int) string {
	if body == "" {
		return ""
	}

	lines := strings.Split(body, "\n")

	for index, line := range lines {
		if unquoted, _, ok := unquoteDepthLine(line, depth); ok {
			lines[index] = unquoted
		}
	}

	return strings.Join(lines, "\n")
}

// isQuote reports whether a line continues a block quote, **including an empty
// one**.
//
// The empty case is the whole function. A callout's body routinely contains a
// blank line, and Obsidian writes it as `>` — so a body line that quoted to the
// empty string continues the callout, while a line with no `>` at all ends it. A
// check on the *content* of the unquoted line would end every callout at its first
// paragraph break, which is the common shape.
func isQuote(line string) bool {
	trimmed := strings.TrimLeft(line, " ")

	return strings.HasPrefix(trimmed, ">")
}

// fenceLine reports whether a line opens or closes a fenced code block.
//
// **Both, and the marker is returned** because ``` and ~~~ do not close each
// other: a document that opens ``` and closes ~~~ has an unterminated fence, and
// treating it as closed is how a `[!secret]` in the second half of a document
// becomes a callout.
func fenceLine(line string) (marker string, isFence bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(trimmed) < 3 {
		return "", false
	}

	// The whole run of markers, not three of them: the closing comparison is
	// `HasPrefix(openFence, marker)`, so an opening fence of five and a closing
	// fence of three correctly fails to close. An info string after the run is
	// irrelevant to both halves, so it is not read.
	for _, fence := range []string{"```", "~~~"} {
		if !strings.HasPrefix(trimmed, fence) {
			continue
		}

		run := 0
		for run < len(trimmed) && string(trimmed[run]) == string(fence[0]) {
			run++
		}

		return trimmed[:run], true
	}

	return "", false
}

// isBlockID reports whether a candidate is an Obsidian block id.
//
// **Letters, digits, `-` and `_` only**, which is Obsidian's own rule. The check
// is here rather than deferred to S3 because this is where the candidate is
// *separated* from the title, and a `^` in the middle of a sentence must not
// become a block id — so a permissive rule would move the boundary, and the
// boundary is what decides whether a title loses its last word.
func isBlockID(candidate string) bool {
	if candidate == "" {
		return false
	}

	for _, r := range candidate {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}

	return true
}

// itoa is `strconv.Itoa` for the two integer shapes this file formats.
//
// Not an import for its own sake: `strconv` is already a dependency of this
// package, and a local helper that formats `int64` and `int` without a
// conversion at every call site is one fewer thing to get wrong in an error
// message that a log line will carry.
func itoa(value int64) string {
	if value < 0 {
		return "-" + utoa(-value)
	}

	return utoa(value)
}

func utoa(value int64) string {
	if value == 0 {
		return "0"
	}

	var digits [20]byte

	// Written from the end, so `write` walks *backwards* and the result has to be
	// sliced from where it stopped rather than from zero.
	write := len(digits)

	for value > 0 {
		write--
		digits[write] = byte('0' + value%10)

		value /= 10
	}

	return string(digits[write:])
}
