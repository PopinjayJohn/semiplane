---
title: "0054 — A campaign sets its brand, and the server renders the stylesheet"
description: "UI §4.12's theme layer: a `theme.yaml` manifest in the content root, `/c/{slug}/theme.css` generated from a closed vocabulary, two contrast floors measured against each theme's own page, and a refusal that keeps the last known-good sheet while a fault answers 500. `theme.brand_invalid` is recorded here as the twenty-fifth signal beyond §13.2, and the six seams this work item cannot wire are named rather than assumed."
lede: "A campaign may change four things about how the product looks, and nothing else — and \"nothing else\" is a property of the generator rather than of a filter. The manifest is attacker-reachable (Obsidian sync writes it), so the question is not whether a validator catches a protected name but whether a code path exists that could emit one. The other half of the decision is what happens when something is wrong anyway: a refusal keeps the theme the campaign already had, an I/O fault answers 500 rather than \"no theme\", and which of the two a GM sees decides whether they fix a colour or file a bug."
weight: 3
date: "2026-10-03"
status: "accepted"
supersedes: []
superseded_by: ""
---

## Context

The two design records disagree with each other before either disagrees with the code. Plan §10.1
defines the theme layer as "plain files in the campaign content root — CSS, header background,
fonts"; revision 1 of the UI specification specified system stacks and no webfont at all; §4.12
resolves the conflict in the record's own words — "the theme layer is real, and it is
token-constrained" — and then states the mechanism: a *manifest*, validated by the server, from
which `/c/{slug}/theme.css` is generated. This record is about what "token-constrained" has to
mean before it is true, and about the two directions a failure can take.

§4.12.1 splits the custom-property namespace in two. On the left, four names a campaign may set:
`--brand-accent`, `--brand-accent-ink`, `--brand-header-image`, and the two font slots. On the
right, the accessibility contract: `--text-*`, `--border*`, `--focus-ring`, `--callout-*`,
`--type-scale`, `--space-scale`, `--target-min`, `--dur-*`, `--radius-*`. The record's argument
for the right-hand column is that "no gate could catch it across every campaign", which is an
argument for making the column a property of the *writer* of the stylesheet rather than a rule a
reviewer enforces — a campaign that sets `--focus-ring` breaks SC 1.4.11 on pages no test in this
repository will ever load.

The manifest is front matter's situation exactly: Obsidian sync writes it, so it is untrusted
input, and it gets front matter's treatment. Alias-expansion limits, every path resolved inside
`os.Root`, a 16 KiB cap (`MaxManifestBytes`), a token-name length bound because the name reaches
a log line, and refusals that quote no byte of the file back (S-12.3).

The failure directions are where the design record is most specific and where the cost of getting
it wrong is a GM's afternoon. §4.12.3 says the last known-good theme stays in effect, that
`theme.brand_invalid` is logged at error level, and that the campaign overview shows a GM a
notice — then closes with the sentence the whole layer is aimed at: "A rejected theme degrades to
the default. It never ships an unreadable UI." That is one failure direction. The other is the
file failing to *read*, which is not a rejection of anything: it is a fault in this process
looking at a file that may be perfectly good, and answering "no theme" for it would be a lie
served to every reader of the campaign.

Two things in this layer were measured rather than reasoned about, and both shaped a decision
below. A closed `os.Root` reports no error and no entries, and `content.classify` has exactly two
answers — `ErrNotExist`, and `ErrOutsideRoot` for everything else — so a root this process has
closed arrives as a path outside the campaign, which a naive classifier answers with **200 and
the core sheet** for a campaign whose content is gone. And the route serves no document at all,
so §10.2's audit has to assert an *absence*, which is the kind of claim an audit cannot
demonstrate about itself without fixtures built to violate it.

## Decision

**The generator iterates a closed vocabulary; nothing filters a deny-list.** `campaignVocabulary`
and `fontSlots` are the only loops in `generate`, so there is no code path through which
`--focus-ring` can reach the output — not a check that catches it, a loop that cannot name it.
`TestTheGeneratedStylesheetCarriesNoProtectedVariable` reads the forbidden set out of the
*product's* stylesheets rather than out of a copy here, so a token added to `tokens.css` is
protected in this package without this package being told. A raw `.css` in a content root is an
ordinary vault file: the assets route serves it because a page may embed one, and no response
this route produces names it — `TestTheGeneratedSheetNeverNamesAStylesheetInTheVault` checks
every sheet the route can write, core and refused and font-bearing alike. `ownership.go` holds
§4.12.1's table as data and `ownership_test.go` holds that table against the built stylesheet,
because a table written in a comment rots in the direction nobody notices.

**The brand pair is two floors, measured against each theme's own page.** `--brand-accent` must
clear 4.5:1 against `--brand-accent-ink` — SC 1.4.3, and the ink sits *on* the accent — and 3:1
against `--bg`, which is SC 1.4.11's non-text floor. Against **both** pages: §4.12.3 names `--bg`
without naming a theme, and a mid-tone that clears 3:1 on the light page fails on the dark one,
so the accent is checked twice and refused once. Only `#rgb` and `#rrggbb` are accepted, and the
seven-character grammar is load-bearing rather than pedantic — a floor cannot be evaluated
against `var(--x)` or `navy`, so a value the validator cannot *compute* is a refusal with a
reason that fits instead of a floor it declines to check. The pair is both-or-neither.

**A refusal and a fault are different answers, and only one of them keeps the sheet.** Five cases
in `Handler.sheet`: no manifest → the core sheet, because withdrawal is a decision and not a
failure; a manifest that validates → the generated sheet, remembered; over the cap or unreadable
as YAML → the remembered sheet plus one error line; content refused — a floor, a protected name,
a malformed key, a path outside the root, an alias bomb, a font slot that is neither `prose` nor
`ui` → the remembered sheet plus one error line; and a manifest this process could not *read* →
500. The retention is `lastGood`, keyed by campaign id rather than slug because the id is what
the validator is salted with, so one campaign's remembered sheet cannot answer another campaign's
request. `Handler.Notice` is the accessor §4.12.3's third requirement needs and answers nothing a
gate has not already admitted — the caller gates, as ADR 0024 has it.

**The event is `theme.brand_invalid`, and it is recorded here rather than by editing §13.2.**
`observability.AllEventNames()` holds 25 names where the architecture record's table lists 18:
ADR 0032 recorded six, and this is the twenty-fifth. It is error level, its attributes are the
campaign's id, its slug and one fixed sentence naming the rule that broke, and nothing a campaign
wrote is among them — a manifest is attacker-reachable and S-12.3 forbids putting its bytes in a
log aggregator. It is also the only signal in this package fired by a *refusal* rather than by a
fault, which is what makes its attribute set the narrowest in the file.

**The sheet is generated per request, from memory, and is byte-identical for every reader.** A
strong `ETag` over the generated bytes, salted with the campaign id — ADR 0016's answer to two
responses sharing one validator — `private, no-cache`, and a zero modification time so that
`http.ServeContent` writes no `Last-Modified` that could disagree with the validator. `Vary:
Cookie` is set on bytes that do not vary, for the reason the assets route gives: this route's
*refusals* vary by reader, and a stored response with no `Vary` matches any request. What is
asserted is the bytes — `TestTheSheetIsByteIdenticalForEveryReaderAndPreference` holds the sheet
across readers and `sp_ui` values, because a test of the header's absence cannot see the
variation it was protecting.

**The product's half of the cascade is `theme.css`, and it is weaker than the generated sheet on
purpose.** The generated declarations live under `:root[data-theme]`, specificity (0,2,0), so a
campaign's brand outranks every default in `theme.css` while being unable to reach anything in
`tokens.css`: the cascade expresses the same direction as the two floors. What `theme.css` holds
is what a campaign may not change — the scrim that keeps `--brand-header-image` from deciding the
contrast of the text above it (opacity 0.05, 0.03 under TV, `isolation: isolate` and
`z-index: -1` so the image is behind the header rather than under it), the header wash, and the
system-stack fallback the fonts sit on. It carries no `@media` naming `min-width`, `orientation`,
`pointer` or `hover`, because TV is a mode and not a width (ADR 0034); its only queries are the
two preferences §6.7 requires in *every* mode, `forced-colors` — which drops the image and the
scrim — and reduced motion, which drops both transitions.

**The route serves no document, so §10.2's audit is the absence, and the absence is proved by
fixtures.** Four responses — the core sheet, the generated sheet, the gate's JSON 404, the
plain-text 500 — are each run against four rules (`ContentTypeIsNotHTML`, `NoHeading`,
`NoLandmark`, `NoFocusStop`) plus §10.6's `TargetClass`. The vocabulary rule reads the **bytes**
rather than the parsed tree: `html.Parse` on `text/css` builds a document the browser never has,
and the AGENTS.md rule about parsing the DOM exists so a word hidden in a comment or an
`aria-label` is not missed — a byte scan sees the comment, which is the stronger check here
rather than the weaker one it would be in markup. Every rule was shown failing by mutating the
route, and each mutation was restored: a `text/html` content type; a failure body carrying
`<h1>`, `<main>` and `<a href>`; the same body with `class="target"`, which is why `NoFocusStop`
and `TargetClass` are separate rules; the word "world" in a failure body; `assertNoHeading` given
an immediate `return`, which turns control 1 red while control 2 stays green — silence is what a
vacuous rule produces, so the two controls are not interchangeable; and the vocabulary rule
narrowed to the parsed tree's visible text, which stops three fixtures being rejected at once.
The mutations and their observed failures are recorded in the two test files' headers.

## Consequences

**Six seams sit outside this work item's paths, and a reader who assumes they are wired finds
inert code.** First, `internal/web/static/css/app.css` needs `@import "./theme.css";` — the
import belongs to the integrator, and until it lands the sheet is a file nothing loads. It fails
toward the product rather than toward an unstyled shell, and `TestTheBrandTokensAreInTheBuiltStylesheet`
derives the tokens the product must declare from `app.css`'s own import lines, so the assertion
strengthens by itself the moment the line lands. Second, `./internal/httpapi/theme` belongs in
`A11Y_ROUTE_PKGS` in the `Makefile`, which is also the integrator's under the one-integrator rule;
the package ships the test names `A11Y_TESTS` searches for (`EveryRoute`, `Structural`,
`Vocabulary`, `Target`) and the negative controls the gate's own guard requires. Third, the shell
must emit `<link rel="stylesheet" href="/c/{slug}/theme.css">` in a campaign document, and the
shell's `<link>` list is a template's business — this route asserts what its own output never
names, not what a template links. Fourth, **§4.12.3's third requirement has an accessor and no
surface**: `Handler.Notice` returns the token and the reason to any caller, and no campaign
overview exists to show it, so today a GM whose theme was refused learns it from an error-level
log line. Fifth, validation runs when the sheet is read rather than "at registration, and again
whenever the watcher sees the manifest change" — reading-time follows the file wherever a sync
client puts it with no hook to install, and a registration-time check belongs to whoever owns
registration. Sixth, `content.Root` has no liveness probe, so `rootIsLive` walks the root with
`fs.SkipAll` — one `Stat`, and the only liveness-distinguishing operation `content.Root`
exposes — because `classify` cannot tell "outside the root" from "the root is closed"; a `Root.Live()`
is the right answer and this walk is the workaround until it exists.

**Adding an overridable token is two edits, and stopping at one is a build failure.** The
vocabulary and the product's own declaration of the name are both required:
`TestEveryCampaignTokenIsDeclaredByTheProduct`, `TestEveryBrandTokenTheProductDeclaresIsInTheCampaignVocabulary`
and `TestNoCampaignTokenIsAlsoProtected` fail between them if only one of the two lands. That is
deliberate: the failure direction §4.12.1 cares about is a name being *declared* without being
owned, and the three tests answer three different questions about the same pair.

**Fonts come from the generated sheet and never from `theme.css`.** `@font-face` has no place in
the product's half of the cascade, because a face is a campaign's file resolved through `os.Root`
and rewritten to `/c/{slug}/assets/…`; the system stack is composed in Go from `font.go`'s
`systemStacks` and is always retained as §4.12.1 requires, so a refused or missing face degrades
to a stack rather than to nothing. The face's `slot` (`prose` or `ui`) names the token it
declares, which is why `faceFor` reads the slot and not the first entry: two faces in one
manifest land in two tokens, and `TestTwoFacesLandInTheirOwnSlots` is what a single-face fixture
cannot see.

**`Mount` registers nothing when it is handed a nil handler.** `router.go` builds each campaign
route's handler as a field, and a field can legitimately be nil in a read-only wiring or in a
test; returning early keeps the mount list a list of calls rather than a list of
`handler != nil` branches, and a mount list that has to be edited to add a route is a mount list
somebody forgets. The pattern is a literal `GET /c/{slug}/theme.css` rather than a wildcard: the
route reads one fixed filename and the request's only input is the slug, and a wildcard would
make its URL space a function of a campaign's directory listing.

## Alternatives considered

**Filter protected names out of a campaign's declarations.** Rejected, and the reason is the same
one ADR 0047 gives for a denylist of addresses: a filter is a list of names somebody has thought
of, and the next token added to `tokens.css` is not in it. The failure would be a protected
variable silently settable — precisely what §4.12.1 exists to prevent — with every test in this
package green, because the filter would be doing exactly what it was written to do. Iterating the
vocabulary inverts the property: the question a reviewer asks is no longer "did we list them all"
but "is there a loop that could name one".

**Let a campaign ship `.css` and sanitise it after rendering.** Rejected: there is no
declarations-level allowlist equivalent to bluemonday's for HTML, `--focus-ring: red` is one
declaration rather than one tag, and a sanitiser that misses one property breaks 1.4.11 across
every campaign with no gate anywhere — the same asymmetry that makes
`html.WithUnsafe()` a security boundary makes CSS sanitising a research problem. The manifest is
also the smaller surface by construction: four names, a hex grammar, and paths that must resolve
inside the campaign root.

**Write `theme.css` to disk when the manifest changes and serve it as a file.** Rejected: a file
on disk is a second answer to what the manifest says, and keeping it in step needs a writer, a
watcher hook and a cleanup path — the shape of the `front_matter` column ADR 0027 refuses, one
level up. Generating per request keeps one answer, and the sheet is two colours and a URL: the
measured cost is a `sha256` over a few kilobytes, which is cheaper than the disagreement it
cannot have.

**Inline the campaign's declarations into the document.** Rejected on caching granularity and on
what is coming: a `<style>` in the shell makes the brand a property of every page's bytes, so one
GM's colour edit invalidates every cached page instead of one subresource; and
`shell.Resolver`'s doc comment records that a strict `Content-Security-Policy` is expected, under
which an inline block needs a nonce or a hash — campaign bytes in a response header. A
subresource that fails to load leaves the page unbranded and correct; an inline block that fails
to parse leaves a parser error in the document.

**Validate only at registration.** Rejected: the manifest's ordinary writer is a sync client, and
registration is one moment in a file's life. The check has to follow the file, which is why it
runs where the sheet is produced and remembers the last sheet that passed.

**Refuse to serve a campaign whose brand pair misses a floor.** Rejected, and §4.12.3 settles it:
this is the same fail-toward-safety direction as the reconciliation cap's — a GM who sets an
unreadable accent keeps a readable UI and a notice naming the pair, while a route that refused
would take the campaign's whole appearance hostage over a contrast ratio. The notice is what makes
the degradation a fixable state rather than a silent one, which is why its absence (seam four
above) is the gap worth closing first.
