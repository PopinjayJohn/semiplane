package secrets_test

// The release gate: S-14.1, as the delivery plan assigns it to this path, written
// as the table it specifies — **three readers × four carriers**, plus a fifth.
//
// `internal/content/redact_test.go` holds S-5.6 at the level of the redactor, and
// `internal/httpapi/wiki` holds it at the level of a rendered page. This file
// holds it at the level of the two surfaces a reader actually receives bytes from,
// mounted on one chain by `harness_test.go`:
//
//   - `GET /c/{slug}/wiki/{path}` — **the HTML body**, **the comments**, and a
//     reader-dependent `ETag`;
//   - `PUT /c/{slug}/secrets/{path}` — **the JSON payload**, and the GM-salted
//     validator, on every status the reveal endpoint can answer.
//
// A previous version of this file argued that the plan's carrier axis did not
// apply because "this route serves no HTML at all". That was true of the reveal
// endpoint and it was the wrong conclusion: the chain this package assembles serves
// **both** routes, and the HTML body is where a player's page is actually assembled.
// Two of the four carriers therefore live on the wiki route and two on the reveal
// endpoint, and the table runs over the union — which is what makes it a gate on
// the *product* rather than on one handler.
//
// # The assertion is about bytes, and about absence
//
// Every cell asserts that **no needle derived from the page's own source appears in
// that carrier**, for every reader below the GM. No cell in this file asserts a
// flag, a class, a `Vary`, or the return value of a redactor. §5.6.1 rules out
// `display:none`, a `hidden` attribute, an HTML comment and a `class` — four
// mechanisms that all render as "not there" and are all recoverable with `curl` —
// so the only claim that catches any of them is absence of the text, and a claim
// about a flag catches none of them.
//
// # The needles come from `content.ScanSecrets`, over the fixture's own source
//
// A hardcoded needle is a guess that passes if the author happened to write the same
// string. `content.ScanSecrets` is the authority on what a `[!secret]` callout is, so
// the needles are taken from **its output on the fixture** and then narrowed to words
// that **no reader below the GM can see** — a word that is already in a player's page
// is not evidence of a leak, it is there whether or not anything leaked.
//
// Three properties of that set are asserted rather than assumed, in
// `TestTheGateCanSeeASecretWhenTheGmGetsOne`: the fixture has both a collapsed and a
// revealed callout, every needle really is in the page, and every collapsed callout
// contributed at least two. A needle set that has drifted empty is the failure mode a
// substring gate cannot see, and it is the one this file's shape exists to make loud.
//
// # The positive control is a test, and it runs the same extractors
//
// `TestTheGateCanSeeASecretWhenTheGmGetsOne` proves the needles are found in the GM's
// HTML body and in the GM's 412 JSON. `TestEveryCarrierFindsASecretWhenThereIsOne`
// drives every carrier's extractor with a synthetic value known to carry a needle and
// requires it to object — **including the two carriers that find nothing even for the
// GM**: a validator and a comment are not places a GM should find page text either, so
// those columns have no response-level control available and a meta-test is the only
// thing that makes them non-vacuous. That is not theoretical: a comment in a response
// is unreachable by any single-file mutation of this pipeline, and the column is empty
// on every response the product produces.
//
// # Parsed, never substring-matched — with one deliberate exception per carrier
//
// Every carrier parses: the HTML through `golang.org/x/net/html` (text nodes,
// attribute values and comment nodes separately), the headers into individual values
// with `ETag` further parsed into opaque entity-tags, the JSON through `encoding/json`
// into every string it decodes at any depth. `internal/content` records four checks in
// this repository that read raw text and reported a violation where there was none.
//
// **The exception is the raw body, carried deliberately alongside the parsed view in
// the HTML and JSON carriers**, and the reason is the opposite one: parsing has *false
// negatives* — it normalises away what it did not model — while a substring scan has
// none, so for an absence claim the blunt check is the one with no blind spot.
// `internal/content`'s `absentFromEveryByte` argues the same pairing for the same
// reason. A leak has to get past both to survive this gate.
//
// # The one exception, and it is a real one
//
// A **GM's** 412 carries the page's source, because S-6.2 requires "412 with the current
// body and its hash" and the current body of a `[!secret]` page is the secret. That
// response is asserted **positively** by the control rather than excused, and the GM is
// in the absence table rather than skipped: the carriers that must never carry page
// text — the headers, the comments, the log lines — are asserted to carry **none of it
// even for the GM**. A secret in a validator or a log line reaches a proxy's disk and an
// operator's terminal, which is a disclosure to somebody the page's access gate never
// consulted.
//
// # The status sweep is kept, and it is a different axis
//
// `TestTheSecretTextNeverReachesAReaderWhoMayNotSeeIt` below is over {reader} × {status}
// and its job is to reach every branch this route has, which the carrier table does
// not: it proves a cell is not satisfied by a 403 that never ran the handler. The
// carrier table's job is to prove that *no* status, *no* carrier and *no* reader below
// the GM carries the text. Both are needed and neither subsumes the other.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/domain"
)

// gatePage is the fixture the release gate runs over, and every choice in it is load
// bearing for the table rather than decoration.
//
//   - **Two collapsed callouts, and two is the number that matters.** A single-secret
//     fixture satisfies every assertion against a redactor that cuts only the first.
//     This fixture has its own rather than sharing `withSecrets` with `reveal_test.go`:
//     the needles are derived from it, and a shared fixture would make the gate's
//     needles move every time an unrelated reveal test changed a word.
//   - **The first body carries inline `*emphasis*`.** That is what makes word-level
//     needles necessary rather than convenient: goldmark splits `*Aldric*` across nodes,
//     so a whole-body needle would not be found in the GM's own page and the control
//     would fail for the right reason. Every needle here is therefore a single word.
//   - **The first body carries a bare passphrase.** A word appearing in no other line of
//     the page, so "the passphrase is in the response" is a claim about this secret and
//     not about the word "passphrase".
//   - **The second callout has no title and no block id**, which is the shape with the
//     fewest places for a formatter to reach for — and it carries a sentence of words
//     that appear in no part of the product, because every one of them is checked by the
//     needle filter and the per-callout count is asserted.
//   - **The block id is `strongroom-first`, and no word of any secret is in it.** That is
//     load bearing and it was not obvious. The anchor is **logged on every reveal** and
//     persisted to `audit_log.detail` — `secrets.go` argues it is safe because an anchor
//     is a digest or an identifier rather than page content — and a block id is an
//     author's own naming. The first version of this fixture used `^strongroom-traitor`,
//     and because `traitor` is also a word of the first callout's *title* the log column
//     went permanently red for the GM.
//   - **No secret word is one of the words this route logs** — `ledger`, `precondition`,
//     `revealed`, `resolved`, `refused`, `anchor`, `path`, `campaign`, `error`, `status`,
//     `changed`, `ordinal`, `form`. The same trap as the anchor one level up: a needle
//     reading "ledger" is reported in the GM's log lines by `secrets.ledger_failed`
//     whether or not anything leaked. The gate reports that as a fixture collision
//     rather than hiding it, because a gate that quietly dropped the colliding needle is
//     the silent pass this file exists to prevent — but a fixture that does not collide
//     is better.
//   - **The third callout is `+`, already public.** Two reasons. A gate that asserted
//     "no secret-shaped text anywhere" would be satisfied by a redactor that deleted
//     everything, and this callout is what makes a deletion a failure. And it is where
//     the needle derivation earns its keep: the public body survives in the document a
//     player is entitled to, so every word in it is **filtered out of the needle set** by
//     the rule below.
//
// **A word shared between the public callout and a collapsed one is excluded from the
// needle set by construction**, because it survives in the player's document, so the
// fixture must not share one. That is why `TestTheGateCanSeeASecretWhenTheGmGetsOne`
// requires every collapsed callout to contribute at least two needles: it catches a
// fixture that has drifted into that shape and quietly reduced the gate to one word.
const gatePage = "---\ntitle: The strongroom\n---\n" +
	"The strongroom door is iron, and it has never been opened with a key.\n" +
	"\n" +
	"> [!secret]- The traitor  ^strongroom-first\n" +
	"> Captain *Aldric* replaced the eastern signal fire, and the passphrase is hunter2.\n" +
	"\n" +
	"The hinges are greased. The grease is from a decade ago.\n" +
	"\n" +
	"> [!secret]-\n" +
	"> The harbourmaster on the quaystone lists Bryn, and the vault combination is mackerel.\n" +
	"\n" +
	"> [!secret]+ Already public\n" +
	"> The cellar floods at the spring tide, which everybody at the table knows.\n"

// gateBarePage is a page holding no collapsed secret at all: `gatePage` with **both
// collapsed callouts deleted**, which is the whole of what a player is entitled to on this
// page — the prose, the public callout, and the shell every reader gets.
//
// **A literal constant, and that is the point.** It is not produced by the redactor under
// test and not derived from `gatePage`, so no redaction decision can influence which words
// the gate searches for. See `gateBareDoc`.
const gateBarePage = "---\ntitle: The strongroom\n---\n" +
	"The strongroom door is iron, and it has never been opened with a key.\n" +
	"\n" +
	"The hinges are greased. The grease is from a decade ago.\n" +
	"\n" +
	"> [!secret]+ Already public\n" +
	"> The cellar floods at the spring tide, which everybody at the table knows.\n"

// The two probe names the positive control looks up by.
//
// **Named rather than matched on a substring**, because a control that says "some probe
// carried the secret" cannot distinguish the one response the requirements put page text
// into from a refusal that happened to echo it.
const (
	probeWikiRead = "the wiki read"
	probeConflict = "the conflict"
)

// gateNeedle is one string that must not reach a reader below the GM.
//
// `secret` is the scanner's ordinal rather than a hand-numbered one, so a failure message
// names the callout the way `content`'s own errors do and a GM can go and look at it.
type gateNeedle struct {
	text   string
	secret int
}

// minNeedleLength is the shortest string the derivation will accept.
//
// **Four, for the reason `internal/content/redact_test.go` gives for choosing a single
// word there.** "The" is a word inside a secret, and asserting its absence would assert
// that the response contains no English. Four is where the fixture's distinctive words
// clear the bar.
const minNeedleLength = 4

// gateNeedles derives the strings this gate searches for, from `gatePage`'s own source,
// through `content.ScanSecrets`.
//
// Two filters, each with a failure it prevents:
//
//   - **only collapsed callouts.** A `+` callout is public by §5.6 and its body is in the
//     document a player is entitled to, so a needle taken from one asserts nothing. This
//     is the filter that makes the fixture's third callout useful.
//   - **no markdown punctuation.** A word carrying `*`, `_`, `[` or `(` is one goldmark
//     may split across nodes, turn into a link, or escape, so the rendered page would not
//     contain the word at all and the needle would match nothing — in the GM's response
//     and in a leaker's alike.
//
// The rule, restated because the filter's independence depends on it:
//
//	**A needle must discriminate.** It has to appear in what the GM receives and in
//	**nothing a reader below the GM can see, in any carrier, in any response.** A word
//	that is already in a player's page is not evidence of a leak: it is there whether
//	or not anything leaked, and asserting its absence would assert the absence of the
//	English language.
//
// The filter is measured over **a player's whole response set against `gateBareDoc`** —
// a page with no collapsed callout at all — rather than against `gatePage`. That is not
// tidiness and it is the second version of this file getting it wrong:
//
//   - **Filtered against the redacted source of `gatePage`.** Cannot see the campaign
//     shell at all — the shell is in no source string and is served identically to every
//     reader. It reported **"second"** as a leak on every page, because the shell's own
//     inline script says "it is a second element rather than a second statement";
//   - **Filtered against `gatePage`'s own player responses.** Correct words, but it
//     reads the responses the gate is asserting about, so **a mutation that leaks makes
//     every needle non-discriminating and empties the set**. The gate still went red —
//     loudly, with "the fixture yields no needles" — but the diagnosis the GM needs, the
//     one naming the reader and the carrier and the word, was destroyed by the act of
//     detecting it. A gate that eats its own failure message is a gate that gets fixed
//     the wrong way.
//
// Measuring against a page with **no collapsed secret** fixes that: redaction cannot
// change what a bare page renders, so the filter is structurally independent of the
// behaviour under test. It also needs the *whole* response set rather than one 200,
// because the failure documents carry their own vocabulary — filtering against a single
// 200 reported **"code"** as a leak on the 404.
//
// A player is used rather than an anonymous reader because a member of the campaign
// receives the richer refusal set, so the filter is the stronger of the two.
//
// The count is returned rather than asserted here so that one place
// (`TestTheGateCanSeeASecretWhenTheGmGetsOne`) states the whole contract.
func gateNeedles(t *testing.T) []gateNeedle {
	t.Helper()

	seenByAPlayer := gateVisibleToAPlayer(t)

	needles := make([]gateNeedle, 0, 24)

	for _, secret := range content.ScanSecrets(gatePageDoc.source) {
		if secret.State.IsRevealed() {
			continue
		}

		for _, word := range needleWords(secret.Body, secret.Title) {
			if strings.Contains(seenByAPlayer, word) {
				continue
			}

			needles = append(needles, gateNeedle{text: word, secret: secret.Ordinal})
		}
	}

	slices.SortFunc(needles, func(a, b gateNeedle) int {
		return strings.Compare(a.text, b.text)
	})

	return needles
}

// gateVisibleToAPlayer is every string a player receives from `gateBareDoc`, across every
// carrier of every response.
//
// **Joined, and that is deliberate rather than lazy.** The rule a needle has to satisfy is
// about the union: a word that survives anywhere a player can see is not a needle. Joining
// the carriers is how the rule is stated, and the failure it prevents — a word absent from
// the body but present in a header — is exactly the shape the two empty columns exist to
// catch.
func gateVisibleToAPlayer(t *testing.T) string {
	t.Helper()

	var seen strings.Builder

	carriers := gateCarriers()

	for _, reader := range gateReaders(t) {
		if reader.entitled {
			continue
		}

		for _, probe := range gateProbes(t, reader, gateBareDoc) {
			for _, carrier := range carriers {
				if !carrier.applies(probe.header) {
					continue
				}

				for _, value := range carrier.extract(t, probe) {
					seen.WriteString(value)
					seen.WriteString("\n")
				}
			}
		}

		// One non-entitled reader is enough: a player and an anonymous reader receive the
		// same documents and the same refusals from this chain, and joining both would
		// only cost a second round of harnesses to learn nothing.
		break
	}

	return seen.String()
}

// gateDoc is one page the gate issues requests against: what to write, where to read it,
// and which anchor a reveal names.
//
// **A parameter rather than two copies of the probe list**, because the needle filter and
// the table must ask the *same* questions of two different pages, and two hand-written
// lists would drift — and a drift here would show up as a needle that suddenly is or is
// not in a player's page, which is exactly the kind of thing nobody notices.
type gateDoc struct {
	// name identifies the page in a failure message.
	name string
	// path is where the page is written on disk, and what the reveal route is aimed at.
	path string
	// wiki is the wiki route's URL segment for it: no extension, the base name.
	wiki string
	// source is the page's markdown.
	source string
	// anchor is the block id a reveal request names, empty where the page has none.
	anchor string
}

// gatePageDoc is the fixture the release gate asserts about.
var gatePageDoc = gateDoc{
	name:   "the strongroom",
	path:   "Strongroom.md",
	wiki:   "Strongroom",
	source: gatePage,
	anchor: "strongroom-first",
}

// gateBareDoc is the control the needle filter is measured against: `gatePage` with both
// collapsed callouts deleted, which is the whole of what a player is entitled to on this
// page — the prose, the public callout, and the shell every reader gets.
//
// **A literal constant, and that is the point.** It is not produced by the redactor under
// test and not derived from `gatePage`, so no redaction decision can influence which words
// the gate searches for. The first version derived it from `gatePage` with the redactor's
// own help, which made a leak erase the needles that catch it.
var gateBareDoc = gateDoc{
	name:   "the bare strongroom",
	path:   "Bare.md",
	wiki:   "Bare",
	source: gateBarePage,
	anchor: "",
}

// needleWords is every plausible needle in one callout's own text: the words of its body
// and of its title, taken separately because a title is a distinct leak surface from a
// body — it is the one a "which callout did that" question answers, and `secrets.go`'s
// own comment says so about logging.
func needleWords(body, title string) []string {
	words := make([]string, 0, 24)

	for _, field := range []string{body, title} {
		for _, word := range strings.FieldsFunc(field, isNeedleBoundary) {
			if len(word) >= minNeedleLength {
				words = append(words, word)
			}
		}
	}

	return words
}

// isNeedleBoundary reports whether a rune ends a needle word.
//
// **Letters, digits and nothing else.** A word carrying punctuation is one goldmark may
// split, linkify or escape, and the rendered page then does not contain the word at all
// — so the needle would match nothing, in the GM's response and in a leaker's alike.
// Excluding them costs nothing here: the fixture's distinctive words are plain words,
// and the words this excludes are exactly the ones a substring assertion cannot see.
func isNeedleBoundary(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z':
		return false
	case r >= 'A' && r <= 'Z':
		return false
	case r >= '0' && r <= '9':
		return false
	default:
		return true
	}
}

// gateReader is one of the three readers the table is over.
//
// **`gm`, `player` and `anonymous`, and the names are the product's** — AGENTS.md fixes
// the vocabulary, and a gate that names a reader differently from the interface is a
// gate whose failure messages a GM cannot act on.
type gateReader struct {
	name      string
	requestor domain.Requestor
	// entitled says whether the reader may see a `[!secret]` body at all. It is the
	// access gate's whole answer, restated here so the absence sweep and the validator
	// test read the same fact from one place.
	entitled bool
}

// gateReaders is the reader axis of the table, in the order a failure reads best: the GM
// first, so a reader who has been shown the secret sees it.
//
// **It takes `t` and seeds the accounts first, and the reason is an evaluation order
// rather than a design choice.** `gmRequestor`, `playerRequestor` and
// `anonymousRequestor` build their `domain.Requestor` from a package variable that
// `newHarness` fills on first use — `TestMain` cannot, because seeding needs a
// `*testing.T`. So `gateProbes(t, gateReaders(t)[0], doc)` evaluates the arguments
// first, reads
// `gmUser` while it is still zero, and hands every probe a requestor with `UserID: 0`.
// `domain.honoursMembership` refuses a non-positive id, the GM resolves to
// `TierReadOnly`, and the gate answers **403** to the GM's own reveal.
//
// That is the worst shape a mistake in this file could take: it is silent, it is the
// *safe* direction, and it turns the whole table into an assertion about refusals. It was
// found because the positive control — which requires the GM's 412 to carry the page —
// went red while the absence table stayed green.
//
// So the seeding is a parameter rather than a convention, and a caller that forgets it
// gets a compile error instead of a green gate.
func gateReaders(t *testing.T) []gateReader {
	t.Helper()

	seedAccounts(t)

	return []gateReader{
		{name: "the GM", requestor: gmRequestor(), entitled: true},
		{name: "a player", requestor: playerRequestor(), entitled: false},
		{name: "an anonymous reader", requestor: anonymousRequestor(), entitled: false},
	}
}

// gateCarrier is one place a secret could be carried in a response.
//
// The two functions are separate because they answer different questions and the table
// needs both. `applies` says whether a response carries this carrier **at all** — a 204
// has no JSON payload, and a JSON response has no HTML comments — and `extract` returns
// **every value** the carrier holds. An extractor that returned one joined blob would be
// a substring search with extra steps, and the reason `headersOf` in
// `authorisation_test.go` *is* written as a join is that it is answering "is there a
// secret in here" over values it cannot structure. Here the values are structured, and a
// joined blob would only make a failure message worse.
type gateCarrier struct {
	// name is the table's column header and appears in every failure message.
	name string
	// applies reports whether a response carries this carrier.
	applies func(header http.Header) bool
	// extract returns everything the carrier carries, parsed.
	extract func(t *testing.T, probe *gateProbe) []string
	// entitledMayCarry says the requirements put the page's own text into this carrier
	// for a reader who **may** see it: S-6.2's 412 carries the source in a JSON field,
	// and S-5.6's callout is rendered in the HTML body. Those two cells are skipped for
	// the GM and asserted positively, by probe name, in
	// `TestTheGateCanSeeASecretWhenTheGmGetsOne`.
	//
	// **A field rather than a name in a list, because the two claims differ in kind.**
	// The other three carriers must carry no page text for *anybody*, and a carrier that
	// forgot this flag would be exempt everywhere — which is the direction a gate must
	// not fail in.
	entitledMayCarry bool
}

// gateCarriers is the carrier axis of the table.
//
// **Five columns, and the plan names four.** The fifth is the log line, kept because
// this file was the only version of this gate that covered one and dropping it would
// lose real coverage: a disclosure to a log aggregator is the same disclosure with a
// longer lifetime, and this route writes its own lines. It is also the one carrier whose
// extractor is trivially uninteresting, which is precisely why it deserves a column — a
// route that started logging `Secret.Body` would satisfy no other column in the table.
func gateCarriers() []gateCarrier {
	return []gateCarrier{
		{
			name:             "the HTML body",
			applies:          isHTMLResponse,
			extract:          htmlBodyValues,
			entitledMayCarry: true,
		},
		{name: "the headers", applies: alwaysCarries, extract: headerValues},
		{name: "the HTML comments", applies: isHTMLResponse, extract: htmlCommentValues},
		{
			name:             "the JSON payload",
			applies:          isJSONResponse,
			extract:          jsonPayloadValues,
			entitledMayCarry: true,
		},
		{name: "the log lines", applies: alwaysCarries, extract: logLineValues},
	}
}

// gateProbe is one response and the log lines the request that produced it wrote.
//
// **The log lines travel with the response** rather than in a separate capture, because
// the table's unit is a carrier of a response and a log line is a carrier of the same
// disclosure. A separate capture would also have a race: the table's subtests run
// concurrently and the capture is shared.
type gateProbe struct {
	// name identifies the request in a failure message.
	name string
	// status, header and body are what the reader received.
	status int
	header http.Header
	body   string
	// logged is everything this route wrote while answering the request.
	logged string
}

// isHTMLResponse reports whether the response carries an HTML body.
//
// **The `Content-Type` decides it, and the media type is compared parsed** rather than by
// searching the header for `text/html`, for the reason every other carrier in this file
// parses: a value that happens to contain the word is not a value that says it.
func isHTMLResponse(header http.Header) bool {
	return isMediaType(header, "text/html")
}

// isJSONResponse reports whether the response carries a JSON payload.
func isJSONResponse(header http.Header) bool {
	return isMediaType(header, "application/json")
}

// isMediaType compares a response's `Content-Type` to a media type, parameters and case
// excluded.
func isMediaType(header http.Header, want string) bool {
	mediaType := header.Get("Content-Type")

	if at := strings.IndexByte(mediaType, ';'); at >= 0 {
		mediaType = mediaType[:at]
	}

	return strings.EqualFold(strings.TrimSpace(mediaType), want)
}

// alwaysCarries is `applies` for the carriers every response has.
func alwaysCarries(http.Header) bool { return true }

// htmlBodyValues returns every string the HTML body carries: each text node's data, each
// attribute value, **and the raw bytes**.
//
// The attribute walk is not optional. A secret pasted into a `title`, an `aria-label` or a
// `data-` attribute is in the response exactly as much as one in a text node, and no
// amount of `display:none` changes that — which is the whole of §5.6.1's argument against
// every mechanism it rules out.
//
// The raw bytes are the deliberate exception to "parse, never substring-match", and the
// reason is the opposite one: `html.Parse` normalises away what it did not model, so a
// secret somewhere the model does not reach is invisible to it, while `strings.Contains`
// on the bytes has no such gap.
//
// **A parse failure is fatal rather than a silent empty result**, because an extractor
// that cannot read the document is an extractor that cannot see a leak, and a cell
// satisfied by one is exactly the silent pass this file exists to make impossible.
func htmlBodyValues(t *testing.T, probe *gateProbe) []string {
	t.Helper()

	values := []string{probe.body}

	root, err := html.Parse(strings.NewReader(probe.body))
	if err != nil {
		t.Fatalf("%s: parse the response as HTML: %v", probe.name, err)
	}

	var walk func(node *html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			values = append(values, node.Data)
		}

		if node.Type == html.ElementNode {
			for _, attribute := range node.Attr {
				values = append(values, attribute.Val)
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return values
}

// htmlCommentValues returns every HTML comment's data.
//
// **`<!-- … -->` is one of the four mechanisms §5.6.1 rules out**, and it is the one no
// attribute walk and no text-node walk would see: a comment is not text to a browser and
// is one `curl | grep` away from a player.
//
// It is also the carrier most likely to be **empty in practice**, and the reason is
// measured rather than assumed: goldmark runs without `html.WithUnsafe()` (ADR 0028) and
// so drops every raw HTML construct in the source, and `policy.go` allows no comments so
// the sanitiser drops any that survived. **Two independent defences**, and together they
// mean no single-file mutation of this pipeline can put a comment in a response. That is
// why `TestEveryCarrierFindsASecretWhenThereIsOne` drives this extractor with a document
// carrying a comment and requires it to object, and why the mutation run records the
// comment carrier as reachable only through the route rather than through the redactor.
func htmlCommentValues(t *testing.T, probe *gateProbe) []string {
	t.Helper()

	root, err := html.Parse(strings.NewReader(probe.body))
	if err != nil {
		t.Fatalf("%s: parse the response as HTML: %v", probe.name, err)
	}

	values := make([]string, 0, 4)

	var walk func(node *html.Node)

	walk = func(node *html.Node) {
		if node.Type == html.CommentNode {
			values = append(values, node.Data)
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}

	walk(root)

	return values
}

// weakETagPrefix and entityTagQuote are the two decorations an `ETag` may carry (RFC 9110
// §8.8.3). Named because parsing one is the difference between comparing a validator and
// comparing a string that happens to look like one; the quotes are named for the same
// reason `precondition.go`'s `opaque` keeps them.
const (
	weakETagPrefix = "W/"
	entityTagQuote = `"`
)

// headerValues returns every string the header set carries: each value verbatim, and — for
// the two headers whose grammar has parts — each part **parsed**.
//
// The parsed parts exist for `ETag` and `Cache-Control` and nothing else, and the reason
// is that a validator is compared by its opaque tag. ADR 0016's whole claim is that a
// GM's and a player's validators must differ, and a check that compared the raw header
// strings would be comparing the weak marker and the quotes as well as the values.
// `TestTheGmAndANonGmNeverShareAValidator` does that comparison; this function is what
// makes the raw string available to it.
//
// **Header names are excluded, and the reason is `headersOf`'s**: a name is this
// project's vocabulary and a secret cannot be in one, so pairing a name with its value
// would let a match report a leak that is not there — and a false failure is worse than an
// unrun check, because the next real one gets ignored.
func headerValues(_ *testing.T, probe *gateProbe) []string {
	values := make([]string, 0, 16)

	for name, entries := range probe.header {
		for _, value := range entries {
			values = append(values, value)

			switch {
			case strings.EqualFold(name, "Etag"):
				values = append(values, opaqueEntityTags(value)...)
			case strings.EqualFold(name, "Cache-Control"):
				values = append(values, cacheControlDirectives(value)...)
			default:
			}
		}
	}

	slices.Sort(values)

	return values
}

// cacheControlDirectives returns each directive of one `Cache-Control` value.
//
// Split on the separator and trimmed, which is the grammar: `private, no-store` is two
// directives. Parsing them is what stops the substring trap — a value reading
// `no-secret-store` contains the characters of `no-store`, and a check that searched the
// raw value for a directive would call it present.
func cacheControlDirectives(value string) []string {
	directives := make([]string, 0, 4)

	for directive := range strings.SplitSeq(value, ",") {
		if trimmed := strings.TrimSpace(directive); trimmed != "" {
			directives = append(directives, trimmed)
		}
	}

	return directives
}

// opaqueEntityTags returns each entity-tag's **opaque value** in one `ETag`.
//
// **Quotes kept, weak marker dropped**, which is the comparison RFC 9110 §8.8.3 defines
// for a weak validator and the comparison `secrets`' own `opaque` makes. Keeping the
// quotes is what stops `"x"` and `x` comparing equal, so a value that is not an entity-tag
// at all cannot match one that is.
func opaqueEntityTags(value string) []string {
	tags := make([]string, 0, 2)

	for candidate := range strings.SplitSeq(value, ",") {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}

		tags = append(tags, strings.TrimPrefix(trimmed, weakETagPrefix))
	}

	return tags
}

// jsonPayloadValues returns every string the JSON payload decodes to, at any depth, plus
// the raw bytes.
//
// **The recursive walk is what this carrier is for.** A secret that reached a JSON body
// would most plausibly arrive nested — inside `content`, inside an error's `detail`,
// inside an array element — and a search of the raw body for a phrase would miss it the
// moment the encoder escaped a character. `json.Unmarshal` decodes those first, so the
// needle is compared against what a client parses.
//
// An empty body is not a JSON payload and is not a failure: a 204 has no body by design,
// which `write.go` argues for at length. A body that is **not** JSON when the
// `Content-Type` says it is *is* a failure, because it means this carrier cannot read the
// payload it was told about.
func jsonPayloadValues(t *testing.T, probe *gateProbe) []string {
	t.Helper()

	if strings.TrimSpace(probe.body) == "" {
		return nil
	}

	values := []string{probe.body}

	var decoded any
	if err := json.Unmarshal([]byte(probe.body), &decoded); err != nil {
		t.Fatalf("%s: the response declares JSON and does not parse as JSON: %v\n"+
			"This carrier cannot see a payload it cannot read, so every absence "+
			"assertion it makes would be vacuous.", probe.name, err)
	}

	values = jsonStrings(values, decoded)

	slices.Sort(values)

	return values
}

// jsonStrings appends every string reachable from a decoded JSON value, keys included.
//
// **Keys as well as values.** A key is a carrier: `{"hunter2":"…"}` puts the secret in a
// field name, and it is in the response.
func jsonStrings(values []string, value any) []string {
	switch typed := value.(type) {
	case string:
		return append(values, typed)
	case []any:
		for _, item := range typed {
			values = jsonStrings(values, item)
		}

		return values
	case map[string]any:
		for key, item := range typed {
			values = append(values, key)
			values = jsonStrings(values, item)
		}

		return values
	default:
		return values
	}
}

// logLineValues returns the log lines this route wrote for this request.
//
// **The wiki route's lines are not here**, and the reason is structural rather than an
// omission: `harness_test.go` builds that handler without a logger, so a wiki read
// produces no lines to capture. The reveal endpoint is the route that writes its own lines
// — `resolve` logs the anchor, `revealed` logs the new state — and it is where "which
// callout did that" would be answered with the callout's own text.
func logLineValues(_ *testing.T, probe *gateProbe) []string {
	if probe.logged == "" {
		return nil
	}

	return strings.Split(probe.logged, "\n")
}

// TestTheGateCanSeeASecretWhenTheGmGetsOne is the positive control, and it exists because
// an absence assertion nobody has ever seen fail is a green light wired to nothing.
// `internal/content/redact_test.go` records this failure having shipped once on this
// repository.
//
// It proves four things, and each is a different way the needle set could be worthless:
//
//  1. **The fixture has collapsed callouts to look for.** A page the scanner no longer
//     recognises would leave the table searching for nothing.
//  2. **The fixture has a public callout**, so a redactor that deleted everything would
//     fail the table rather than satisfy it.
//  3. **Every needle really is in the page** — derived from the source, so close to a
//     tautology, and here because the derivation walks two filters and the filters are
//     what could be wrong.
//  4. **The needles are found in the GM's own bytes**, through the same two extractors the
//     table uses, in the two responses the requirements put page text into. Without this,
//     points 1–3 are all satisfiable by a page and two filters that happen to agree with
//     each other and with nothing the reader receives.
//
// The fourth is why this cannot live inside the table: the table's cells are absence claims
// and a control has to be a presence claim.
func TestTheGateCanSeeASecretWhenTheGmGetsOne(t *testing.T) {
	t.Parallel()

	needles := gateNeedles(t)
	if len(needles) == 0 {
		t.Fatal("the fixture yields no needles, so every absence assertion in this " +
			"file is being made with a detector that cannot see a secret")
	}

	found := content.ScanSecrets(gatePageDoc.source)

	collapsed, publicBodies := 0, 0

	for _, secret := range found {
		if secret.State.IsRevealed() {
			publicBodies++
		} else {
			collapsed++
		}
	}

	if collapsed == 0 {
		t.Fatal("the fixture holds no collapsed callout, so the gate has nothing to " +
			"assert the absence of")
	}

	if publicBodies == 0 {
		t.Fatal("the fixture holds no revealed callout, so a redactor that deleted " +
			"every callout would satisfy every absence assertion in this file")
	}

	for _, needle := range needles {
		if !strings.Contains(gatePageDoc.source, needle.text) {
			t.Errorf("the needle %q is not in the page, so asserting its absence "+
				"proves nothing", needle.text)
		}
	}

	// Two per collapsed callout, so a fixture that has drifted into sharing every one of
	// its distinctive words with the public callout cannot quietly reduce the gate to one
	// word.
	perSecret := map[int]int{}
	for _, needle := range needles {
		perSecret[needle.secret]++
	}

	for ordinal := range collapsed {
		if perSecret[ordinal] < 2 {
			t.Errorf("collapsed callout %d contributed %d needles; at least two are "+
				"needed for the gate to be more than one word wide",
				ordinal, perSecret[ordinal])
		}
	}

	// The control itself: the GM's own bytes, through the table's own extractors.
	probes := gateProbes(t, gateReaders(t)[0], gatePageDoc)
	byName := map[string]*gateProbe{}

	for _, probe := range probes {
		byName[probe.name] = probe
	}

	for _, required := range []struct{ carrier, probe string }{
		{"the HTML body", probeWikiRead},
		{"the JSON payload", probeConflict},
	} {
		probe, present := byName[required.probe]
		if !present {
			t.Fatalf("no probe named %q, so the control for %q has nothing to read",
				required.probe, required.carrier)
		}

		if !carrierCarriesAny(t, gateCarriers(), required.carrier, probe, needles) {
			t.Errorf("%s carries none of the page's %d needles in the GM's %s, so "+
				"every absence assertion this gate makes through that extractor is "+
				"being made by a detector that cannot see a secret",
				required.carrier, len(needles), required.probe)
		}
	}
}

// carrierCarriesAny reports whether one carrier of one response carries any needle,
// looked up by the carrier's name.
//
// **By name rather than by struct**, because the control names its two required pairs in a
// table and carrying a `gateCarrier` value it never uses would be worse.
func carrierCarriesAny(
	t *testing.T,
	carriers []gateCarrier,
	name string,
	probe *gateProbe,
	needles []gateNeedle,
) bool {
	t.Helper()

	for _, carrier := range carriers {
		if carrier.name != name || !carrier.applies(probe.header) {
			continue
		}

		for _, value := range carrier.extract(t, probe) {
			for _, needle := range needles {
				if strings.Contains(value, needle.text) {
					return true
				}
			}
		}
	}

	return false
}

// TestEveryCarrierFindsASecretWhenThereIsOne is the meta-test over the carriers, and it is
// the reason the two empty columns are not decoration.
//
// Each carrier's extractor is driven with a synthetic value **known to carry a needle**,
// in the place that carrier reads, and is required to object. A `Contains` loop cannot
// really be wrong, so the risk is not that an extractor is broken — it is that a carrier
// reads a place the product never populates, which makes its column vacuously green
// forever. `internal/content`'s `TestTheAbsenceChecksAreNotVacuous` is the same test for
// the same reason, and it exists there because three mutations reached the redactor and
// none of them turned the response-level assertions red.
//
// **A clean document is asserted not to object either.** An extractor that objects to
// everything is not a better gate; it is a gate that will be turned off, and a false
// failure is worse than an unrun check.
func TestEveryCarrierFindsASecretWhenThereIsOne(t *testing.T) {
	t.Parallel()

	const secret = "hunter2"

	for _, testCase := range []struct {
		carrier string
		leaky   *gateProbe
	}{
		{
			carrier: "the HTML body",
			leaky: &gateProbe{
				name:   "a secret in a text node",
				status: 200,
				header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
				body:   "<p>The passphrase is " + secret + ".</p>",
			},
		},
		{
			carrier: "the HTML body",
			leaky: &gateProbe{
				name:   "a secret in an attribute",
				status: 200,
				header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
				body:   `<p title="The passphrase is ` + secret + `.">Prose.</p>`,
			},
		},
		{
			carrier: "the HTML body",
			leaky: &gateProbe{
				// A character reference: the bytes do not contain the word, the DOM does.
				// This is the one case `strings.Contains` cannot see and the parse can.
				name:   "a secret written with a character reference",
				status: 200,
				header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
				body:   "<p>The passphrase is hunter&#50;.</p>",
			},
		},
		{
			carrier: "the headers",
			leaky: &gateProbe{
				name:   "a secret in a header value",
				status: 200,
				header: http.Header{"X-Note": {"The passphrase is " + secret + "."}},
			},
		},
		{
			carrier: "the headers",
			leaky: &gateProbe{
				name:   "a secret inside a validator",
				status: 200,
				// The ETag parses, so this also proves the entity-tag path sees the value
				// rather than reporting the quotes and the `W/`.
				header: http.Header{
					"Etag": {weakETagPrefix + entityTagQuote + secret + entityTagQuote},
				},
			},
		},
		{
			carrier: "the HTML comments",
			leaky: &gateProbe{
				name:   "a secret in a comment",
				status: 200,
				header: http.Header{"Content-Type": {"text/html; charset=utf-8"}},
				body:   "<p>Prose.</p><!-- The passphrase is " + secret + " -->",
			},
		},
		{
			carrier: "the JSON payload",
			leaky: &gateProbe{
				name:   "a secret in a JSON field",
				status: 412,
				header: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
				body:   `{"error":"precondition failed","content":"` + secret + `"}`,
			},
		},
		{
			carrier: "the JSON payload",
			leaky: &gateProbe{
				// **A JSON key, and a value escaped by the encoder.** The second is the case
				// a raw substring search cannot see at all: `json.Marshal` writes `<` as
				// `<`, so a needle containing one is not in the bytes and is in what a
				// client parses.
				name:   "a secret in a JSON key, escaped in the bytes",
				status: 412,
				header: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
				body:   `{"error":"<x>","` + secret + `":"a"}`,
			},
		},
		{
			carrier: "the log lines",
			leaky: &gateProbe{
				name:   "a secret in a log line",
				status: 204,
				logged: "secrets event=secrets.revealed passphrase=" + secret,
			},
		},
	} {
		t.Run(testCase.carrier+"/"+testCase.leaky.name, func(t *testing.T) {
			t.Parallel()

			for _, carrier := range gateCarriers() {
				if carrier.name != testCase.carrier {
					continue
				}

				if !carrier.applies(testCase.leaky.header) {
					t.Fatalf("%s does not apply to a response with Content-Type %q, so "+
						"the case cannot be driven", carrier.name,
						testCase.leaky.header.Get("Content-Type"))
				}

				for _, value := range carrier.extract(t, testCase.leaky) {
					if strings.Contains(value, secret) {
						return
					}
				}

				t.Errorf("%s did not find %q in %s.\n"+
					"This carrier reads a place the product may never populate, so "+
					"its column in the gate is satisfied by nothing and cannot fail.",
					carrier.name, secret, testCase.leaky.name)
			}
		})
	}

	// The negative arm: a real 200, served by the product, that every reader is entitled
	// to. **It is not tautological for the two empty columns.** The needle filter reads
	// the player's carriers, so it has looked at this response's body — but the two
	// carriers that find nothing even for the GM are precisely the ones whose emptiness
	// has to be proven rather than assumed, and a validator is the one place a carrier
	// could invent a leak and get the gate turned off.
	entitled := gateBarePageAsAPlayerReadsIt(t)
	needles := gateNeedles(t)

	for _, carrier := range gateCarriers() {
		if !carrier.applies(entitled.header) {
			continue
		}

		for _, value := range carrier.extract(t, entitled) {
			for _, needle := range needles {
				if strings.Contains(value, needle.text) {
					t.Errorf("%s objected to a page a player is entitled to: it "+
						"reported %q in %q.\n"+
						"A carrier that reports a leak where there is none will be "+
						"turned off, and the next real one goes with it.",
						carrier.name, needle.text, value)
				}
			}
		}
	}
}

// gateReaderNamed returns one reader by name, so a caller that wants a particular one
// does not reach for an index into a slice whose order is only a failure-message
// nicety. An index would be one reordering away from a test that silently stopped
// testing what it says it tests.
func gateReaderNamed(t *testing.T, name string) gateReader {
	t.Helper()

	for _, reader := range gateReaders(t) {
		if reader.name == name {
			return reader
		}
	}

	t.Fatalf("no reader named %q in the gate's reader axis", name)

	return gateReader{}
}

// gateBarePageAsAPlayerReadsIt is the bare page's 200, as a player receives it.
//
// **The first probe of the bare doc's set**, which is `probeWikiRead`. It is a real 200
// from the product, so the negative arm in
// `TestEveryCarrierFindsASecretWhenThereIsOne` runs against the shell, the navigation and
// the campaign chrome rather than against a hand-written document that might not resemble
// what this product serves.
func gateBarePageAsAPlayerReadsIt(t *testing.T) *gateProbe {
	t.Helper()

	probes := gateProbes(t, gateReaderNamed(t, "a player"), gateBareDoc)

	for _, probe := range probes {
		if probe.name == probeWikiRead {
			return probe
		}
	}

	t.Fatalf("no %q probe in the bare page's response set", probeWikiRead)

	return nil
}

// TestTheSecretTextReachesTheGmAndNobodyElse is the release gate, as a table over **three
// readers × five carriers**.
//
// Every cell asserts the same thing in the same words: **no needle derived from the page's
// own source appears in that carrier, in that response, to that reader**. No cell asserts
// a flag, a status, a class or a redactor's return value, because none of those catches a
// leak — `display:none`, a `hidden` attribute, an HTML comment and a `class` are the four
// ways §5.6.1 rules out precisely because each renders as "not there" and is one `curl`
// away.
//
// The GM is in the table and **is not excused**: the two carriers the requirements put page
// text into are skipped with a named reason and asserted positively in the control above,
// and the other three — the headers, the comments, the log lines — are asserted to carry
// **none of it even for the GM**.
//
// Each probe gets its own harness and its own campaign. That is not tidiness: the 204
// probe **writes** the page, and a shared harness would leave the later probes reading a
// page whose first callout is public — a page where showing the secret is *correct*, and a
// table that cannot tell the two apart proves nothing.
func TestTheSecretTextReachesTheGmAndNobodyElse(t *testing.T) {
	t.Parallel()

	needles := gateNeedles(t)
	if len(needles) == 0 {
		t.Fatal("no needles were derived, so this table asserts nothing")
	}

	carriers := gateCarriers()

	for _, reader := range gateReaders(t) {
		t.Run(reader.name, func(t *testing.T) {
			// Not `t.Parallel()`. Every probe opens its own campaign against the one
			// process-wide store `TestMain` opened — ADR 0004's single-instance rule — and
			// two tests in one package that opened a store would collide on it.
			probes := gateProbes(t, reader, gatePageDoc)

			for _, carrier := range carriers {
				t.Run(carrier.name, func(t *testing.T) {
					if reader.entitled && carrier.entitledMayCarry {
						t.Skipf("%s is one of the two carriers the requirements put the "+
							"page's own text into for a reader who may see it — the "+
							"rendered callout and S-6.2's 412 — so \"absent\" is the "+
							"wrong claim for %s here. The exemption is asserted "+
							"positively, by probe name, in "+
							"TestTheGateCanSeeASecretWhenTheGmGetsOne.",
							carrier.name, reader.name)
					}

					examined := 0

					for _, probe := range probes {
						if !carrier.applies(probe.header) {
							continue
						}

						examined++

						assertNoNeedle(t, needles, carrier, reader, probe)
					}

					if examined == 0 {
						t.Errorf("%s carries nothing %s could have been sent, so the "+
							"column is satisfied by a table that looked at nothing",
							carrier.name, reader.name)
					}
				})
			}
		})
	}
}

// assertNoNeedle is one cell: it fails naming the reader, the carrier, the response and
// the needle.
//
// **The message has to say what a leak means**, because the reader who gets this failure
// is a GM who shipped a build in which a player could read their secret, and "the response
// carries hunter2" is a fact while "a test failed" is not.
func assertNoNeedle(
	t *testing.T,
	needles []gateNeedle,
	carrier gateCarrier,
	reader gateReader,
	probe *gateProbe,
) {
	t.Helper()

	for _, value := range carrier.extract(t, probe) {
		for _, needle := range needles {
			if !strings.Contains(value, needle.text) {
				continue
			}

			t.Errorf("%s received %q in %s, from %s (HTTP %d).\n"+
				"§5.6.1 requires a `[!secret]-` body to be **absent** from every response "+
				"a reader who may not see it receives — not hidden, not `display:none`, "+
				"not commented out. Callout %d of the page is its source; the text is here.",
				reader.name, needle.text, carrier.name, probe.name, probe.status,
				needle.secret)

			return
		}
	}
}

// gateProbes returns every response one reader receives from the two routes, across the
// statuses that answer differently to different readers.
//
// **The status axis is here so the carrier table is not satisfied by refusals.** A cell
// that only ever saw a 403 has proved nothing about redaction, and a reader who may read
// the campaign gets a 200 from the wiki route and a 403 from the reveal endpoint — so the
// same carrier is asserted against a refusal and against a body, which is the only way a
// carrier column can tell them apart.
//
// Each probe builds its own harness, for the reason
// `TestTheSecretTextReachesTheGmAndNobodyElse` gives.
func gateProbes(t *testing.T, reader gateReader, doc gateDoc) []*gateProbe {
	t.Helper()

	requests := gateRequests(t, doc)
	probes := make([]*gateProbe, 0, len(requests))

	for _, request := range requests {
		fixed := newHarness(t)

		if request.ledgerRefuses {
			fixed.failing(errLedgerRefused)
		}

		fixed.write(doc.path, doc.source)

		capture := &logCapture{}
		fixed.logger = slog.New(capture)

		recorder := fixed.request(request.method, request.target(fixed), reader.requestor,
			request.header(fixed), request.body)

		probes = append(probes, &gateProbe{
			name:   request.name,
			status: recorder.Code,
			header: recorder.Header(),
			body:   recorder.Body.String(),
			logged: capture.captured(),
		})
	}

	return probes
}

// gateRequest is one request the table issues, described so its target and headers can be
// built **after** the harness exists: every harness has its own campaign slug, so a literal
// target would name no campaign and earn a 404 that asserts nothing.
type gateRequest struct {
	name   string
	method string
	target func(h *harness) string
	header func(h *harness) http.Header
	body   string
	// ledgerRefuses swaps the shared ledger for one that refuses, which is the only way to
	// reach the 500 branch.
	ledgerRefuses bool
}

// gateRequests is the status axis: one row per response the reader might be sent, across
// both routes.
//
// **Every status the two routes can answer**, read off `wiki/handler.go`'s three writers
// and `secrets/write.go`'s five, so a new writer is a missing row rather than an uncovered
// one. `current` is the header for a row that must get past the precondition: a row that
// sends a stale one is answered 412 instead of reaching the branch it names, which is how
// two rows of the older table in this file were wrong before they were fixed.
//
// The conflict row sends a validator that names nothing rather than a stale one derived
// from a rewrite, so **no probe mutates the page** — the isolation each probe gets from
// its own harness is then belt and braces rather than the only thing standing between the
// table and a leak it would read as correct.
func gateRequests(t *testing.T, doc gateDoc) []gateRequest {
	t.Helper()

	wiki := func(path string) func(*harness) string {
		return func(h *harness) string { return "/c/" + h.campaign.Slug + "/wiki/" + path }
	}

	reveal := func(path string) func(*harness) string {
		return func(h *harness) string { return "/c/" + h.campaign.Slug + "/secrets/" + path }
	}

	current := func(h *harness) http.Header {
		return http.Header{"If-Match": {h.validatorFor(doc.path)}}
	}

	encoded, err := json.Marshal(map[string]any{"anchor": doc.anchor, "revealed": true})
	if err != nil {
		t.Fatalf("encode the reveal body: %v", err)
	}

	return []gateRequest{
		{
			name:   probeWikiRead,
			method: http.MethodGet,
			target: wiki(doc.wiki),
			header: func(*harness) http.Header { return nil },
		},
		{
			name:   "the wiki read revalidated",
			method: http.MethodGet,
			target: wiki(doc.wiki),
			header: func(*harness) http.Header {
				return http.Header{"If-None-Match": {"*"}}
			},
		},
		{
			name:   "the wiki read of a page that is not there",
			method: http.MethodGet,
			target: wiki("Nowhere"),
			header: func(*harness) http.Header { return nil },
		},
		{
			name:   "a matched reveal",
			method: http.MethodPut,
			target: reveal(doc.path),
			header: current,
			body:   string(encoded),
		},
		{
			name:   probeConflict,
			method: http.MethodPut,
			target: reveal(doc.path),
			// A validator that names nothing. `matches` compares the opaque tag, so this is
			// a mismatch rather than an absent check — the same direction a genuinely
			// stale validator takes, and the one S-6.2's 412 is written for.
			header: func(*harness) http.Header {
				return http.Header{"If-Match": {weakETagPrefix +
					entityTagQuote + "not-the-page" + entityTagQuote}}
			},
			body: string(encoded),
		},
		{
			name:   "a reveal with no precondition",
			method: http.MethodPut,
			target: reveal(doc.path),
			header: func(*harness) http.Header { return nil },
			body:   string(encoded),
		},
		{
			name:   "a reveal that named no state",
			method: http.MethodPut,
			target: reveal(doc.path),
			header: current,
			body:   `{"anchor":"` + doc.anchor + `"}`,
		},
		{
			name:   "a reveal of a secret that is not there",
			method: http.MethodPut,
			target: reveal(doc.path),
			header: current,
			body:   `{"anchor":"admiral","revealed":true}`,
		},
		{
			name:          "a reveal into a ledger that refuses",
			method:        http.MethodPut,
			target:        reveal(doc.path),
			header:        current,
			body:          string(encoded),
			ledgerRefuses: true,
		},
	}
}

// TestTheGmAndANonGmNeverShareAValidator is ADR 0016, asserted by **comparing parsed
// entity-tags** rather than raw header strings.
//
// The record exists because the architecture overview's cache rules conflict: two body
// variants of one page cannot share a validator, or a cache holding both serves whichever
// it stored first and a player receives the GM's page in full. The salt that prevents it
// is one boolean in a hash preimage, so the property cannot be asserted on the hash
// function — only on the consequence.
//
// Two assertions, and the second is the one a mutation breaks:
//
//  1. **The two variants mint different validators.** Necessary, and satisfied by two
//     implementations minting different strings.
//  2. **A reader holding the other variant's validator is told to fetch.** This is the
//     consequence, through the same `If-None-Match` comparison a browser makes, and it
//     is what a mutation removing the salt from the preimage actually breaks: with one
//     validator for both variants the player revalidates, is answered 304, and keeps the
//     unredacted body they already had.
//
// The comparison uses `opaqueEntityTags`, so `W/` and the quotes are compared away and the
// **values** are what is compared. A test comparing raw headers would be comparing the
// weak marker too, and would pass for the wrong reason if the two implementations
// disagreed about it.
func TestTheGmAndANonGmNeverShareAValidator(t *testing.T) {
	t.Parallel()

	fixed := newHarness(t)
	fixed.write(gatePageDoc.path, gatePageDoc.source)

	variants := make(map[string]string, 2)

	for _, reader := range gateReaders(t) {
		recorder := fixed.get("/c/"+fixed.campaign.Slug+"/wiki/"+gatePageDoc.wiki,
			reader.requestor)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s's read of the page = %d, want 200; body:\n%s",
				reader.name, recorder.Code, recorder.Body)
		}

		tags := opaqueEntityTags(recorder.Header().Get("ETag"))
		if len(tags) != 1 {
			t.Fatalf("%s's response carries %d entity-tags, want 1: %q",
				reader.name, len(tags), recorder.Header().Get("ETag"))
		}

		variants[reader.name] = tags[0]
	}

	if variants["the GM"] == variants["a player"] {
		t.Errorf("the GM and a player are both given the validator %q.\n"+
			"ADR 0016: the salt is the whole of the fix, and a cache holding both "+
			"variants serves whichever it stored first — so the player receives the "+
			"GM's page, secret text and all.", variants["the GM"])
	}

	for _, reader := range gateReaders(t) {
		t.Run(reader.name+" holding the GM's validator", func(t *testing.T) {
			// The stored value is the **opaque tag, quotes included** — `opaque` drops the
			// weak marker and keeps the quotes, so sending it back needs no re-quoting.
			// Wrapping it a second time sends `W/""84de…""`, which matches nothing, and the
			// GM's own revalidation then answers 200 and this assertion reports the wrong
			// defect.
			header := http.Header{"If-None-Match": {variants["the GM"]}}

			recorder := fixed.request(http.MethodGet,
				"/c/"+fixed.campaign.Slug+"/wiki/"+gatePageDoc.wiki, reader.requestor,
				header, "")

			want := http.StatusOK
			if reader.entitled {
				want = http.StatusNotModified
			}

			if recorder.Code != want {
				t.Errorf("%s revalidating the GM's variant = %d, want %d.\n"+
					"The GM's validator names the unredacted page; a reader who may not "+
					"see one must be told to fetch, not told its copy is current.",
					reader.name, recorder.Code, want)
			}
		})
	}
}

// logCapture is a `slog.Handler` that keeps every line it is given, so the assertions can
// read them.
//
// **Attributes included, and that is the whole point.** The route's lines carry the path
// and the anchor as attributes; a capture that kept only `record.Message` would see
// "secrets.revealed" and nothing else, and a route that had put a secret in an attribute
// would pass every log assertion in this package.
//
// **A mutex, not a bare slice.** The route logs from the request goroutine and
// `t.Parallel()` runs cells concurrently, so an unsynchronised append here is a data race
// that `-race` would report as a flaky failure in a file it has nothing to do with.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

// Enabled reports that the handler wants every level, so a test can capture a debug line.
//
// Every level, including debug: `secrets.resolved` is a debug line and it is one of the two
// places a route could most plausibly put the anchor's context, so a capture that filtered
// at Warn would miss it.
func (*logCapture) Enabled(_ context.Context, _ slog.Level) bool { return true }

// Handle records one rendered log line, message and attributes alike.
func (c *logCapture) Handle(_ context.Context, record slog.Record) error {
	var out strings.Builder

	out.WriteString(record.Message)
	out.WriteString(" ")

	record.Attrs(func(attr slog.Attr) bool {
		out.WriteString(attr.Key)
		out.WriteString("=")
		out.WriteString(attr.Value.String())
		out.WriteString(" ")

		return true
	})

	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, out.String())

	return nil
}

// WithAttrs implements `slog.Handler` and returns the receiver: the capture keeps whole
// lines, so there is nothing to add to a later one.
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }

// WithGroup implements `slog.Handler` and returns the receiver, for the same reason.
func (c *logCapture) WithGroup(string) slog.Handler { return c }

// captured returns every line recorded so far, one per line.
func (c *logCapture) captured() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.Join(c.lines, "\n")
}

// revealRequest is one request that produces one of this route's statuses. The status
// sweep below is over these; the carrier gate is over `gateProbes`.
type revealRequest struct {
	// name identifies the row in the table and in a failure message.
	name string
	// target is the page path after `/secrets/`.
	target string
	// body is the request body.
	body string
	// want is the status the GM must receive.
	want int
	// build assembles a fresh harness and the header this request needs. A function rather
	// than a string because three of the rows need the page's *own* validator — and a row
	// that sends a stale one is answered 412 instead of reaching the branch it names, which
	// is how two rows of this table were wrong before they were fixed.
	build func(t *testing.T) (*harness, http.Header)
	// mayCarryPage is true for exactly one row, and the reason is S-6.2.
	mayCarryPage bool
}

// revealRequests is the status axis of the older sweep.
//
// **Every status this route can answer**, and the list is read off `write.go`'s five
// writers rather than off the handler's branches, so a new writer is a missing row rather
// than an uncovered one.
//
// The two rows that are *not* statuses the route raises on its own — the 403 for a path
// that leaves the root and the 500 for a ledger that refuses — are here because they are
// the two that reach a writer a careless reader would not think of.
func revealRequests(t *testing.T) []revealRequest {
	t.Helper()

	const pagePath = "Vault.md"

	// The nested-callout page, whose refusal is a 400 rather than a 404.
	const nestedPath = "Nested.md"

	nestedPage := "Prose.\n\n> [!secret]- Outer\n> > [!secret]- Inner  ^inner\n> > Body.\n"

	// `current` is the header for a row that must get past the precondition, and it takes
	// the **target's** path rather than a closed-over one. Two rows below aim at a page
	// other than `Vault.md`, and sending them `Vault.md`'s validator earns them a 412
	// instead of the status they name — which is what happened, and why the parameter is
	// here.
	current := func(fixed *harness, target string) http.Header {
		return http.Header{
			"If-Match": {fixed.validatorFor(strings.TrimSuffix(target, ".md"))},
		}
	}

	return []revealRequest{
		{
			name:   "204 a matched reveal",
			target: pagePath,
			body:   revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:   http.StatusNoContent,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
		{
			name:         "412 a stale precondition",
			target:       pagePath,
			body:         revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:         http.StatusPreconditionFailed,
			mayCarryPage: true,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, http.Header{"If-Match": {`W/"nope"`}}
			},
		},
		{
			name:   "428 no precondition at all",
			target: pagePath,
			body:   revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:   http.StatusPreconditionRequired,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, http.Header{}
			},
		},
		{
			name:   "400 a body that named no state",
			target: pagePath,
			body:   `{"anchor":"traitor"}`,
			want:   http.StatusBadRequest,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
		{
			name:   "400 a reveal of a nested callout",
			target: nestedPath,
			body:   revealBody(t, map[string]any{"ordinal": 0, "revealed": true}),
			want:   http.StatusBadRequest,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)
				fixed.write(nestedPath, nestedPage)

				return fixed, current(fixed, nestedPath)
			},
		},
		{
			name:   "404 a secret that is not there",
			target: pagePath,
			body:   revealBody(t, map[string]any{"anchor": "admiral", "revealed": true}),
			want:   http.StatusNotFound,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
		{
			name:   "404 a page that is not there",
			target: "Nowhere.md",
			body:   revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:   http.StatusNotFound,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
		{
			name:   "403 a path that leaves the campaign",
			target: "..%2f..%2fetc%2fpasswd",
			body:   revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:   http.StatusForbidden,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
		{
			name:   "500 a ledger that refuses",
			target: pagePath,
			body:   revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}),
			want:   http.StatusInternalServerError,
			build: func(t *testing.T) (*harness, http.Header) {
				fixed := newHarness(t).failing(errLedgerRefused)
				fixed.write(pagePath, withSecrets)

				return fixed, current(fixed, pagePath)
			},
		},
	}
}

// TestTheSecretTextNeverReachesAReaderWhoMayNotSeeIt is the **status** sweep: the same
// absence claim, over {reader} × {status}, for the endpoint that makes secrets public.
//
// **The positive control comes first, and it is a control rather than a formality.** The
// fixture's GM is shown a 412 that legitimately carries the page, and the test proves the
// detector finds the secret in such a response *before* using that detector to assert its
// absence everywhere else. Without it the whole table would be satisfied by a detector
// that cannot see anything — the exact failure `internal/content/redact_test.go` records
// having shipped once (`TestTheAbsenceChecksAreNotVacuous`).
//
// Three levels per cell, and the third is the one this package adds:
//
//   - **the raw body bytes**, because a secret escaped, split across a JSON string or
//     hidden in an attribute is still in the bytes, and `strings.Contains` over the whole
//     value cannot be fooled by structure;
//   - **every header value**, because a secret in a header is a secret in a log aggregator
//     and in every echo a proxy makes;
//   - **every log line**, because this route writes its own and the temptation is specific:
//     "which callout did that" is a question whose answer is the callout's own text, and
//     the project's rule is that no event and no error may carry page content (S-12.3).
func TestTheSecretTextNeverReachesAReaderWhoMayNotSeeIt(t *testing.T) {
	t.Parallel()

	// --- The positive control. ---------------------------------------------------
	control := newHarness(t)
	control.write("Vault.md", withSecrets)

	stale := control.validatorFor("Vault.md")
	control.write("Vault.md", firstHalfRevealed)

	controlCapture := &logCapture{}
	control.logger = slog.New(controlCapture)

	controlResponse := control.reveal("Vault.md", gmRequestor(), stale,
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))

	if controlResponse.Code != http.StatusPreconditionFailed {
		t.Fatalf("the control answered %d, want 412; body:\n%s",
			controlResponse.Code, controlResponse.Body)
	}

	if !carriesAnyFragment(controlResponse.Body.String()) {
		t.Fatalf("the GM's 412 does not carry the page at all, so every absence "+
			"assertion below is being made with a detector that cannot see a secret:\n%s",
			controlResponse.Body)
	}

	// And the detector is not simply matching the request back: the request named the
	// anchor `traitor`, which is a block id and **not** one of the fragments, and the
	// fragments are body text and a title.
	if carriesAnyFragment(controlCapture.captured()) {
		t.Errorf("the route's log lines carry a secret fragment:\n%s", controlCapture.captured())
	}

	// --- The table. ---------------------------------------------------------------
	readers := map[string]domain.Requestor{
		"the GM":                 gmRequestor(),
		"a player":               playerRequestor(),
		"an anonymous reader":    anonymousRequestor(),
		"a signed-in non-member": strangerRequestor(),
	}

	for readerName, reader := range readers {
		for _, request := range revealRequests(t) {
			t.Run(readerName+" / "+request.name, func(t *testing.T) {
				t.Parallel()

				fixed, header := request.build(t)
				capture := &logCapture{}
				fixed.logger = slog.New(capture)

				recorder := fixed.request(http.MethodPut,
					"/c/"+fixed.campaign.Slug+"/secrets/"+request.target,
					reader, header, request.body)

				// The status first, so a cell that never reached the branch it names fails
				// loudly instead of passing its absence assertions vacuously. **The gate's
				// own statuses are accepted for the non-GM columns**: a player is refused
				// 403 and a stranger 404 before the route runs, and a gate response is
				// still a response that must carry no secret text.
				if readerName == "the GM" && recorder.Code != request.want {
					t.Fatalf("the GM's %s = %d, want %d; body:\n%s",
						request.name, recorder.Code, request.want, recorder.Body)
				}

				if readerName != "the GM" && recorder.Code < 400 {
					t.Fatalf("%s's %s = %d, which is not a refusal at all",
						readerName, request.name, recorder.Code)
				}

				if request.mayCarryPage && readerName == "the GM" {
					// The one cell allowed to carry the page, asserted **positively**, so the
					// exception is a fact this suite holds rather than a hole in a rule. A
					// future change that stopped sending the source fails here rather than
					// passing silently.
					if !carriesAnyFragment(recorder.Body.String()) {
						t.Errorf("the GM's 412 no longer carries the page's source, so "+
							"S-6.2's \"412 with the current body\" is not being answered:\n%s",
							recorder.Body)
					}

					return
				}

				where := readerName + " / " + request.name
				assertNoSecretText(t, where+" (the body)", recorder.Body.String())
				assertNoSecretText(t, where+" (the headers)", headersOf(recorder.Header()))
				assertNoSecretText(t, where+" (the log)", capture.captured())
			})
		}
	}
}

// TestEveryResponseIsPrivateAndUnstorable is the `Cache-Control` half of S-5.4 and of
// ADR 0016, over **every** status this route can answer.
//
// A property of the route rather than of a branch, so it is asserted over the whole status
// set in one place instead of being repeated in each test that happens to look at a
// response. The mutation it exists for is a branch that forgets `writePrivateHeaders` — and
// that mutation would leave every other test in this package green, because a missing
// header is invisible to a status assertion.
//
// Three responses carry material that must never be stored by a shared cache: the 204's and
// the 428's GM-salted validators (a stored GM-salted validator answers "what is this
// page's current version" for a page that may hold secret text), and the 412's page
// source. All of them are responses a proxy would keep.
func TestEveryResponseIsPrivateAndUnstorable(t *testing.T) {
	t.Parallel()

	for _, request := range revealRequests(t) {
		t.Run(request.name, func(t *testing.T) {
			t.Parallel()

			fixed, header := request.build(t)

			recorder := fixed.request(http.MethodPut,
				"/c/"+fixed.campaign.Slug+"/secrets/"+request.target,
				gmRequestor(), header, request.body)

			if recorder.Code != request.want {
				t.Fatalf("%s = %d, want %d; body:\n%s",
					request.name, recorder.Code, request.want, recorder.Body)
			}

			if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
				t.Errorf("%s carries Cache-Control: %q, want %q.\n"+
					"Every response on this route is reader-dependent or carries a "+
					"GM-salted validator, and a reverse proxy in front of a self-hosted "+
					"instance is the ordinary deployment — one that stored any of these "+
					"would serve it to a reader who may not have it.",
					request.name, got, "private, no-store")
			}
		})
	}
}

// TestNoRouteLineCarriesASecretBodyOrTitle is the log half of S-12.3, after a
// **successful** reveal.
//
// The table above checks the log for every refusal; this one checks it after the request
// whose log line has the most tempting fields to fill in — the anchor's form, the ordinal,
// the new state. A route that logged the resolved callout's `Title` or `Body` on success
// would satisfy every other assertion in this package and put a secret in a log aggregator,
// which is the one destination S-12.3 exists to keep text out of.
//
// Asserted as an **absence over a positive frame**: the line is captured, and it must both
// carry the facts it is supposed to carry (the campaign, the path, the anchor, the new
// state) and carry none of the page's text. A route that logged nothing would satisfy the
// absence half alone.
func TestNoRouteLineCarriesASecretBodyOrTitle(t *testing.T) {
	t.Parallel()

	const pagePath = "Vault.md"

	fixed := newHarness(t)
	fixed.write(pagePath, withSecrets)

	capture := &logCapture{}
	fixed.logger = slog.New(capture)

	recorder := fixed.reveal(pagePath, gmRequestor(), fixed.validatorFor(pagePath),
		revealBody(t, map[string]any{"anchor": "traitor", "revealed": true}))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("a matched reveal = %d, want 204; body:\n%s", recorder.Code, recorder.Body)
	}

	lines := capture.captured()

	assertNoSecretText(t, "the log after a successful reveal", lines)

	// The frame: the line names what happened, so the absence above is an absence of secret
	// text rather than an absence of a line.
	for _, wanted := range []string{"secrets.revealed", fixed.campaign.Slug, pagePath} {
		if !strings.Contains(lines, wanted) {
			t.Errorf("the success line does not mention %q, so the absence assertion above "+
				"would be satisfied by a route that logged nothing:\n%s", wanted, lines)
		}
	}
}

// TestNoRefusalNamesThePageOrTheSecret is the error half of S-12.3, read from the
// responses rather than from the sentinels.
//
// Every refusal this route raises is a **constant**, and that is not tidiness. An error
// whose text is a function of the caller's input is a channel, and on an endpoint about
// secrets the caller's input is a page. `errUnknownSecret`'s alternative spelling —
// `content.UnknownSecretOrdinalError`, which carries the ordinal and the count — is a
// perfectly reasonable sentence and a leak, because "the page has 3 secrets" is a fact
// about callouts a reader may never have been shown. The content layer's own errors carry
// no path for the same reason, and a route that echoed one would undo that.
//
// **Read from the response** rather than from the package's internals because the response
// is what a reader and a log aggregator actually see: a second implementation of the
// refusal inside a test would be a second thing to keep in step with the first.
func TestNoRefusalNamesThePageOrTheSecret(t *testing.T) {
	t.Parallel()

	for _, request := range revealRequests(t) {
		t.Run(request.name, func(t *testing.T) {
			t.Parallel()

			// The GM's 412 is the one response that carries the page on purpose (S-6.2),
			// and `TestTheConflictBodyCarriesThePageOnlyToTheGm` asserts that positively
			// **and** asserts the absence for every reader the gate refuses. Asserting it
			// here as well would only restate it.
			if request.mayCarryPage {
				t.Skip("the GM's 412 carries the page by requirement; the exception is " +
					"asserted in TestTheConflictBodyCarriesThePageOnlyToTheGm")
			}

			fixed, header := request.build(t)

			recorder := fixed.request(http.MethodPut,
				"/c/"+fixed.campaign.Slug+"/secrets/"+request.target,
				gmRequestor(), header, request.body)

			body := recorder.Body.String()

			// The page's path, which the content layer keeps out of its errors because "a
			// 403 that echoed the attempted path would undo `content.Root`'s confinement in
			// one string". `Vault.md` is the one path a refusal may never name; the
			// traversal row's own path is the caller's input rather than a page in the
			// vault, so it is not asserted here.
			if strings.Contains(body, "Vault.md") {
				t.Errorf("%s names the page in its refusal body: %q.\n"+
					"The path is in the access log, which the middleware wrote before this "+
					"handler ran; a body that repeats it hands the vault's layout to "+
					"whoever asked.", request.name, body)
			}

			// And the callout's own text, which `assertNoSecretText` covers — repeated here
			// in one line so this test reads as the pair it is: a refusal names neither the
			// page nor the callout.
			assertNoSecretText(t, request.name+" (the refusal body)", body)
		})
	}
}
