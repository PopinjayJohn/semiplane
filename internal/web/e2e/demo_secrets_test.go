package e2e_test

// Claim two: both `[!secret]` states, as the Game Master and as a player — and the
// refusal to a player is **omission**, not hiding.
//
// # Why this file exists when `internal/content` already tests the redactor
//
// `internal/content`'s own tests are about the redactor: given a source and a
// viewer, what comes back. They are thorough, they are mutation-checked, and they
// pass on a build where **nothing ever asks for a redacted page**.
//
// What they cannot see is the whole chain: a secret's body, cut out of the source
// by `OmitSecrets`, parsed, rendered, resolved, sanitised, written into a template
// and sent over a socket. A stage in that chain that re-admitted the text would
// leave every unit test green. So this file asks the only question that answers it
// — *what bytes does the player receive* — and asks it of the shipped binary over
// HTTP, against the artefact a first-time operator actually installs.
//
// # "Omission, not hiding", and why the bytes are the right thing to assert
//
// AGENTS.md's security invariants say it in one line: "Secret redaction is
// omission, not hiding. Not `display:none`, not a comment, not a class." §5.6.1
// gives the reason: each of those puts the secret in the response body, and a
// response body is something a player can read with `curl`.
//
// **So the primary assertion enumerates bytes, not hiding techniques.** A page that
// hid its secrets behind `display:none`, behind a comment, behind an `aria-hidden`,
// behind a placeholder or behind a live region would all still carry the text, and
// `demoCarry` walks text nodes, every attribute value and every comment to prove it
// does not. The technique-shaped assertions after it are *in addition*, because they
// give a reader a legible failure rather than a list of node types.
//
// # The needle, and why it is read from the artefact rather than written here
//
// Every needle comes from `content.ScanSecrets` over the page's **own source** — the
// same scanner the pipeline runs, asked for the same spans. Nothing here hardcodes
// "hunter2" or "Marden Mill", for two reasons:
//
//   - a hardcoded needle is a golden value a vault edit silently invalidates, and the
//     failure would be "this page leaks" on a page that does not; and
//   - the artefact contains prose that *mentions* a secret's words.
//     `public-post/town-notice.md` writes "The ferry rota for the coming week" in
//     ordinary prose **and** uses it as a callout title; a title-based needle would
//     find the prose and report a leak on a page with none.
//
// **The needle's first line is required to be present in the Game Master's
// response**, and that is the non-vacuity. "Absent from the player's bytes" is
// satisfied by a needle absent from every byte of the response including the Game
// Master's — so each page is fetched twice and the Game Master's fetch is the
// control. If a needle is not findable the test fails rather than passing quietly,
// and says which page and which line.
//
// # What is unreachable, and why that is worth saying here
//
// Two mutations a reader would expect to be caught are **not**, and both are
// properties of the product rather than of these tests. Each was run; each reported
// zero failures.
//
//   - **A redactor that emitted raw HTML in place of the callout.** Both the
//     "hide the text with `display:none`" version and the "leave a placeholder shell"
//     version. `content.Renderer` never enables unsafe HTML (ADR 0028), so goldmark
//     omits `ast.RawHTML` from the output entirely -- tags and content both -- and the
//     mutation has no observable effect at all. **A redactor in this pipeline cannot
//     hide anything by emitting markup; it can only omit.**
//   - **A secret element marked `hidden`, `aria-hidden` or styled.** Run by adding
//     `hidden` to `ext/secret.go`'s collapsed div: `internal/content/policy.go` allows
//     `class`, `data-ext` and `data-secret` on a `div` and strips every other
//     attribute, so the marking never reaches the response.
//
// Both say the same thing about what this file proves: **the byte-level walk is the
// assertion that carries the claim**, and the technique-shaped ones beside it are
// defence in depth against an allowlist that might grow. The mutations that *are*
// caught are in the commit message.

// # The nested callout, and why it gets its own instance
//
// Phase 10's defect on this surface was a **`> > [!secret]-` nested inside a revealed
// `>`, swallowed by it, reaching a player** as a collapsed callout with its body in
// it. `internal/content/secret.go` now recurses and forces an outer callout that
// contains one to `SecretCollapsed` whatever its own marker byte says.
//
// **The shipped demo vault contains no nested callout.** `make demo-check` demands
// that both states be demonstrated; it does not demand that shape, so the
// regression this file is named for is not reachable from the artefact. Rather than
// assert nothing about it, `TestANestedCalloutInsideARevealedOneReachesNoReader`
// plants the shape into its **own copy** of the vault, before the seed, and drives
// it through the same server and the same assertions. Its own header says which half
// of this file is the artefact and which half is a fixture, because a reader is
// entitled to know.

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
)

// The two states, in the two spellings each layer uses.
//
// **Four constants and not two, because the marker byte and the rendered value are
// different vocabularies and conflating them makes an assertion vacuous.** That is
// not hypothetical: an earlier version of this file compared `data-secret` against
// `content.SecretState.String()` -- `-` and `+` -- while `ext/secret.go` writes
// `collapsed` and `revealed`, so every "the player has no collapsed element" check
// compared against a value nothing emits and passed for the wrong reason. Worth
// naming, because a `data-` attribute is a product-owned spelling and a reader has
// no way to guess it from the domain's.
//
// The marker bytes come from `content.SecretState`, so a rename there breaks this
// file's arithmetic. **The rendered values are literals**, because `ext/secret.go`'s
// `secretCollapsed` and `secretRevealed` are unexported; each comment names the
// declaration it mirrors, so a rename there turns the assertions red rather than
// silently vacuous -- which is the property the split is for.
var (
	// secretMarkerCollapsed is `[!secret]-` as the author wrote it: present, withheld from
	// every non-GM response. The *rendered* spelling is `secretStateCollapsed`.
	secretMarkerCollapsed = content.SecretCollapsed.String()

	// secretMarkerRevealed is `[!secret]+` as the author wrote it: public to everybody who
	// may read the page, because a reveal publishes in place and a private
	// campaign's access gate is what makes that safe. The *rendered* spelling is
	// `secretStateRevealed`.
	secretMarkerRevealed = content.SecretRevealed.String()
)

// The rendered spellings, mirroring `ext/secret.go`'s unexported `secretCollapsed`
// and `secretRevealed`.
const (
	// secretStateCollapsed is the `data-secret` value of a callout authored collapsed.
	secretStateCollapsed = "collapsed"

	// secretStateRevealed is the `data-secret` value of a callout authored revealed.
	secretStateRevealed = "revealed"
)

// The markers the secret extension writes into the rendered document.
//
// **`data-` and not the class alone.** `ext/secret.go` writes both, and a class is a
// styling decision `shell.css` owns — a rename there would un-hold a class
// assertion silently. `play_test.go` established the rule that tests read
// `data-testid`, and this is the same shape of product-owned name.
const (
	secretStateAttribute = "data-secret"
	secretHiddenClass    = "secret--collapsed"
	secretExtensionAttr  = "data-ext"
	secretExtensionValue = "secret"
)

// minSecretNeedle is the shortest line this suite will treat as identifying a
// secret.
//
// Twenty-four characters: long enough that the odds of it appearing in a page's
// prose by coincidence are negligible, and short enough that an author can write a
// first body line within it.
const minSecretNeedle = 24

// demoSecret is one callout found in a page of the artefact.
type demoSecret struct {
	// state is the marker byte, verbatim from the file.
	state string

	// needle is the line a non-GM response must not carry.
	//
	// **One line, and it has to be findable.** The whole body would be a better
	// needle and a worse one: a body the renderer wraps differently from the source
	// would report a leak that is not there. The first line is short enough to
	// survive verbatim and long enough that finding it elsewhere by accident is
	// implausible.
	needle string
}

// collapsed reports whether this callout is Game-Master-only.
func (s demoSecret) collapsed() bool { return s.state == secretMarkerCollapsed }

// demoPageSecrets is one page of the artefact and the callouts it carries.
type demoPageSecrets struct {
	page    demoPage
	secrets []demoSecret
}

// states is a human-readable summary for a failure message.
func (p demoPageSecrets) states() string {
	states := make([]string, 0, len(p.secrets))

	for _, secret := range p.secrets {
		states = append(states, secret.state)
	}

	return "The page holds " + strings.Join(states, ", ")
}

// collapsed counts the page's Game-Master-only callouts.
func (p demoPageSecrets) collapsed() int {
	count := 0

	for _, secret := range p.secrets {
		if secret.collapsed() {
			count++
		}
	}

	return count
}

// demoSecretsOf returns a page's callouts, in document order.
func demoSecretsOf(source string) []demoSecret {
	scanned := content.ScanSecrets(source)

	found := make([]demoSecret, 0, len(scanned))

	for _, secret := range scanned {
		found = append(found, demoSecret{
			state:  secret.State.String(),
			needle: secretNeedle(secret.Body),
		})
	}

	return found
}

// secretNeedle is the line a non-GM response must not carry.
//
// **Markdown emphasis stripped, and nothing else.** `*`, `**` and “ ` “ become
// elements or are dropped by the renderer, so a needle carrying them would not be
// found in the rendered text even though it is present — a false negative in the
// safe direction, and still a failure this suite must not report as a leak. `_` is
// deliberately **not** stripped: an underscore inside a word is not emphasis and is
// carried verbatim, so stripping it would break a needle rather than fix one. A
// wikilink is not stripped either — `[[a|b]]` renders as `b`, so a needle carrying
// one will not match — and a callout whose first body line carries a wikilink
// therefore fails the Game Master control with a message that says so.
func secretNeedle(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		stripped := strings.NewReplacer("**", "", "*", "", "`", "").Replace(trimmed)

		return strings.TrimSpace(stripped)
	}

	return ""
}

// demoMarkerBytes reads the marker byte off every `[!secret]` marker in a source,
// in document order.
//
// **A second, trivial grammar, and here is why it is not a second answer.** The
// product's own `content.ScanSecrets` deliberately does **not** report an outer
// callout's authored byte: `scanRange` overwrites `State` with `SecretCollapsed` when
// the callout nests another, so the file's `+` is unreachable through the scanner by
// design — the rewrite happens to `find` before the append and the author
// deliberately kept the *reported* state forced.
//
// So for a fixture whose whole point is "the file says `+` and the pipeline treats
// it as collapsed", the byte has to be read from the file. That is the only place
// in this suite where a `[!secret]` marker is read without the product's grammar,
// and it reads only the byte — no spans, no bodies, no state — so there is nothing
// for it to disagree with `ScanSecrets` about.
func demoMarkerBytes(source string) []string {
	var written []string

	for offset := 0; ; {
		at := strings.Index(source[offset:], "[!secret]")
		if at < 0 {
			return written
		}

		marker := offset + at + len("[!secret]")
		if marker < len(source) {
			written = append(written, string(source[marker]))
		}

		offset = marker
	}
}

// demoSecretsInVault returns every page of the artefact carrying a callout.
//
// **Sorted by campaign and path**, because `walkDemoVault` sorts and "the first
// page with a secret" must not depend on the filesystem.
func demoSecretsInVault(t *testing.T, vault demoVaultDir) []demoPageSecrets {
	t.Helper()

	kinds := demoKindsForBuild(t)

	var carried []demoPageSecrets

	for _, page := range walkDemoVault(t, vault, kinds) {
		secrets := demoSecretsOf(vault.page(t, page.campaign+"/"+page.rel))
		if len(secrets) == 0 {
			continue
		}

		carried = append(carried, demoPageSecrets{page: page, secrets: secrets})
	}

	return carried
}

// TestTheGameMasterSeesBothSecretStatesAndAPlayerSeesOnlyTheRevealedOne is claim
// two, over every callout the artefact carries.
//
// **Three assertions per callout, and they are three different questions:**
//
//  1. the Game Master's response **carries** the needle — the control, and what
//     makes "absent from the player's" mean anything;
//  2. the other reader's response carries it in **no text node, no attribute value
//     and no comment**, which is the byte-level "omission, not hiding"; and
//  3. the other reader's response has **no element at all** claiming to be a
//     collapsed secret, no `secret--collapsed` class token, and no `data-ext="secret"`
//     element that is `hidden` or carries an inline `style` — the technique-shaped
//     readings of the same claim, which give a legible failure rather than a list of
//     node types.
//
// Plus one per page: the number of `data-ext="secret"` elements equals the number of
// callouts **that reader is entitled to**, which is what catches a shell.
//
// **A revealed callout is owed to both readers**, and that is asserted rather than
// assumed — a redactor that cut both states would pass every withholding assertion
// above and fail here. §5.6 puts `+` in public and `-` in GM-only, and cutting both
// makes the reveal endpoint a way to lose information rather than to publish it.
//
// **The other reader is `demo-player` inside the showcase campaign and an anonymous
// client elsewhere**, and that is not a convenience. `demo-player` is a member of
// `greyhaven` and of nothing else, so the showcase is where a *player's* view of a
// collapsed secret is a real state. The other campaign carrying a secret is the
// public one, where the reader is a stranger — and that is the sharper case, because
// there the access gate is already open and the callout is the only control there is.
func TestTheGameMasterSeesBothSecretStatesAndAPlayerSeesOnlyTheRevealedOne(t *testing.T) {
	boot := sharedDemo(t)

	carried := demoSecretsInVault(t, boot.vault)
	if len(carried) == 0 {
		t.Fatal("no page of the demo vault carries a [!secret] callout, so the " +
			"redaction claim would pass on a vault that demonstrates nothing. " +
			"`make demo-check` demands both states be demonstrated; this suite " +
			"demands that what it demonstrates is actually withheld")
	}

	for _, entry := range carried {
		t.Run(entry.page.url(), func(t *testing.T) {
			other, otherName := demoOtherReader(boot, entry.page.campaign)

			url := boot.base + entry.page.url()

			if status, _ := demoGet(t, other, url); status != http.StatusOK {
				t.Fatalf("GET %s as %s = %d, want 200. A reader who cannot open the "+
					"page is not a reader whose view of it this claim is about, and "+
					"skipping the comparison would leave a redaction regression "+
					"unnoticed behind a 404", entry.page.url(), otherName, status)
			}

			gmDocument := boot.requireDocument(t, boot.gm, url)
			otherDocument := boot.requireDocument(t, other, url)

			for _, secret := range entry.secrets {
				demoRequireNeedle(t, gmDocument, entry.page, secret, demoGM)

				if secret.collapsed() {
					demoRequireWithheld(t, otherDocument, entry.page, secret, otherName)

					continue
				}

				if carried := demoCarry(otherDocument.root, secret.needle); len(carried) == 0 {
					t.Errorf("the revealed callout in %s is missing from the response to "+
						"%s. §5.6 puts `+` in public and `-` in GM-only, and cutting both "+
						"states makes the reveal endpoint a way to lose information rather "+
						"than to publish it.\n\nIts first line is %q.",
						entry.page.rel, otherName, secret.needle)
				}
			}

			demoRequireCalloutCount(t, otherDocument, entry,
				len(entry.secrets)-entry.collapsed(), otherName)
			demoRequireCalloutCount(t, gmDocument, entry, len(entry.secrets), demoGM)
		})
	}
}

// demoOtherReader names the reader a campaign's secrets are withheld from.
//
// A member of the showcase campaign and a stranger elsewhere. The choice is stated
// in the caller's comment because it is the substance of the claim rather than a
// detail: the two cases fail for different reasons, and only one of them is the
// access gate.
func demoOtherReader(boot *demoBoot, campaign string) (*http.Client, string) {
	if campaign == showcaseSlug {
		return boot.player, demoPlayer
	}

	return boot.anon, "a reader with no account"
}

// demoRequireNeedle fails unless the Game Master's response carries this callout's
// line.
//
// **The control for every "absent" assertion in this file.** A needle not findable
// in the Game Master's bytes is not findable in anybody's, so the withholding
// assertions below it would pass for the wrong reason — and a suite that passes for
// the wrong reason is the failure mode this repository documents at length.
func demoRequireNeedle(
	t *testing.T,
	document demoDocument,
	page demoPage,
	secret demoSecret,
	reader string,
) {
	t.Helper()

	if secret.needle == "" {
		t.Fatalf("the callout in %s has an empty first body line, so this suite has no "+
			"needle to withhold or to find", page.rel)
	}

	if len(secret.needle) < minSecretNeedle {
		t.Fatalf("the callout in %s has a first body line of %d characters (%q). %d is "+
			"the floor: a shorter line is short enough that the renderer, the "+
			"sanitiser or the page's own prose could reproduce it by accident, and a "+
			"needle that cannot identify anything identifies nothing",
			page.rel, len(secret.needle), secret.needle, minSecretNeedle)
	}

	if carried := demoCarry(document.root, secret.needle); len(carried) == 0 {
		t.Errorf("the callout in %s is missing from %s's response too. Its first body "+
			"line is %q. So every withholding assertion for this page would pass on a "+
			"needle nothing renders, which is how a redaction suite goes green while "+
			"proving nothing — check whether the line survives the render: emphasis "+
			"and links become elements, so a line carrying one will not match",
			page.rel, reader, secret.needle)
	}
}

// demoRequireWithheld is the whole of §5.6.1 against one reader's response.
func demoRequireWithheld(
	t *testing.T,
	document demoDocument,
	page demoPage,
	secret demoSecret,
	reader string,
) {
	t.Helper()

	if carried := demoCarry(document.root, secret.needle); len(carried) > 0 {
		t.Errorf("%s receives the body of a collapsed [!secret] callout in %s. It is "+
			"in %v.\n\n§5.6.1 rules out every form of hiding that still ships the text: "+
			"`display:none`, `hidden`, a comment, a class, a placeholder, a live "+
			"region. Each of those puts the secret in the response, and the response is "+
			"what `curl` prints.\n\nThe secret's first line is %q.",
			reader, page.rel, carried, secret.needle)
	}

	if nodes := demoElementsWithAttr(
		document.root,
		secretStateAttribute,
		secretStateCollapsed,
	); //nolint:lll // one condition, one sentence
	len(
		nodes,
	) > 0 {
		t.Errorf("%s's response for %s carries %d element(s) with %s=%q. Redaction is "+
			"omission: the callout is cut out of the **source** before anything renders "+
			"it, so there is no element left to hide. Found: %s",
			reader, page.rel, len(nodes), secretStateAttribute, secretStateCollapsed,
			describeElements(nodes))
	}

	if nodes := demoCarryingClassToken(document.root, secretHiddenClass); len(nodes) > 0 {
		t.Errorf("%s's response for %s carries %d element(s) with the class token %q. "+
			"The markup for a secret the reader may not see is never produced, so a "+
			"class meaning `collapsed` has nothing to mean here. Found: %s",
			reader, page.rel, len(nodes), secretHiddenClass, describeElements(nodes))
	}

	// No secret element that is hidden rather than absent. `aria-hidden` is in the
	// list because a screen reader that honours it and a reader who does not are
	// exactly the split §5.6.1 refuses to rely on.
	eachElement(document.root, func(node *html.Node) {
		if attrOf(node, secretExtensionAttr) != secretExtensionValue {
			return
		}

		for _, attribute := range []string{"hidden", "aria-hidden", "style"} {
			if hasAttr(node, attribute) {
				t.Errorf("%s's response for %s carries a `secret` element with %q. A "+
					"secret element that is hidden is a secret in the response: a reader "+
					"with the stylesheet disabled, a `curl`, or an assistive technology "+
					"that ignores the attribute all see it. Redaction is omission.",
					reader, page.rel, attribute)
			}
		}
	})
}

// demoRequireCalloutCount requires a response to carry exactly this many secret
// elements.
//
// **The count is what catches a shell.** A redactor that emitted
// `<div class="secret secret--collapsed" hidden></div>` per collapsed callout — an
// empty placeholder, which §5.6.1 rules out for a second and separate reason: it
// discloses the *existence and position* of a secret, which in a game about who is
// hiding what is information — would carry the right classes and no text and pass
// every assertion above. It would not pass this one. So would a redactor that cut a
// revealed callout: the count would be too low.
func demoRequireCalloutCount(
	t *testing.T,
	document demoDocument,
	entry demoPageSecrets,
	want int,
	reader string,
) {
	t.Helper()

	got := demoElementsWithAttr(document.root, secretExtensionAttr, secretExtensionValue)
	if len(got) == want {
		return
	}

	t.Errorf("%s's response for %s carries %d `secret` element(s), want %d. %s A count "+
		"that is too high means a shell was emitted for a callout this reader may not "+
		"see — a placeholder, which §5.6.1 rules out as well as hiding, because it "+
		"discloses that a secret is here and where. A count that is too low means a "+
		"revealed secret was cut.\n\nFound: %s",
		reader, entry.page.url(), len(got), want, entry.states(), describeElements(got))
}

// describeElements names a few offending elements for a failure message.
func describeElements(nodes []*html.Node) string {
	const shown = 3

	described := make([]string, 0, shown+1)

	for index, node := range nodes {
		if index == shown {
			described = append(described, "… and "+strconv.Itoa(len(nodes)-shown)+" more")

			break
		}

		described = append(described,
			"<"+node.Data+" class=\""+attrOf(node, "class")+"\" "+secretStateAttribute+
				"=\""+attrOf(node, secretStateAttribute)+"\">")
	}

	if len(described) == 0 {
		return "(none)"
	}

	return strings.Join(described, ", ")
}

// TestTheGameMasterAndAPlayerHoldDifferentValidatorsForAPageWithASecret is ADR 0016,
// against the artefact rather than against a fixture.
//
// **The `ETag` is salted with `include_secrets`,** so a GM's validator and a
// player's are two cache entries rather than one. Without the salt the first reader
// to warm a shared cache decides what every later reader is served — and on a
// public campaign the first reader is a stranger.
//
// One assertion and it is a strong one: **the two validators must differ**. Equal
// validators mean one representation, and one representation of this page cannot
// satisfy both a Game Master and a player.
// nestedFixturePage is where the planted nested callout goes, and the page is
// **written by this test**, not read from the artefact.
//
// Anything under the vault copy's `notes/` that the seed has not heard of is
// indexed by the startup index along with everything else, so it is served by the
// same route, through the same gates, with the same renderer as the artefact's own
// pages. Nothing about the request is special.
const nestedFixturePage = "greyhaven/notes/e2e-nested.md"

// nestedOuterNeedle and nestedInnerNeedle are the two bodies, written here rather
// than read because the page is here.
//
// **Distinct, and each long enough to identify itself** (`minSecretNeedle`), so a
// failure can say *which* of the two leaked.
const (
	nestedOuterNeedle = "The party has been told the signal fire is out."
	nestedInnerNeedle = "The traitor at the gate is Captain Aldric of the ninth watch."
)

// nestedFixtureSource is the shape phase 10 shipped.
//
// **The `>` quoting is the whole point and it is easy to get wrong.** A nested
// callout's header line carries *two* levels of quoting (`> > [!secret]-`), because
// one turns the block quote into a callout and the second puts the callout inside
// it. `parseCalloutHeader` is given a depth and `scanRange` recurses one deeper, so
// the scanner finds the inner one at depth two — which is what it did not do before
// the fix, when the outer body's loop swallowed every line that was still quoted.
//
// The outer is **`+`, revealed**: that is the state which used to fail. A collapsed
// outer would have been cut whole and the nesting would never have been a question.
const nestedFixtureSource = `---
title: A revealed callout that nests a collapsed one
---

## The shape phase 10 shipped

The callout below is revealed, and the callout inside it is not. A reader who may
not see a secret must not be able to reach the inner one by way of the outer.

> [!secret]+ Shown to the party  ^outer-revealed
> The party has been told the signal fire is out.
>
> > [!secret]- Never told  ^inner-collapsed
> > The traitor at the gate is Captain Aldric of the ninth watch.

Back to [[index]].
`

// TestANestedCalloutInsideARevealedOneReachesNoReader is phase 10's defect, and it
// is a **fixture** rather than a reading of the artefact.
//
// **The shipped demo vault contains no nested callout.** `make demo-check` requires
// both secret states to be demonstrated and it does not require that shape, so
// nothing in the release artefact exercises the recursion in
// `internal/content/secret.go` — which is the code the fix lives in. Asserting
// nothing about it would leave the defect this file is named for untested by the
// suite that drives the demo vault, so the shape is planted into **this test's own
// copy** of the vault, before the seed, and served by its own instance.
//
// # The three assertions, and the rule behind the third
//
//  1. **The Game Master's response carries both bodies.** The control: a needle
//     nothing renders makes every withholding assertion pass for the wrong reason.
//  2. **The player's response carries neither, in no text node, no attribute and no
//     comment**, and carries **no `secret` element at all** — so the outer revealed
//     callout is not emitted with the inner one cut out of its middle, which is the
//     second way this could fail and the one a byte search alone would miss.
//  3. **The outer callout renders as `collapsed` even though its marker byte is
//     `+`.** That is `scanRange`'s one override: a callout that contains another is
//     forced to `SecretCollapsed` whatever its own marker says, because there is no
//     rendering of a public outer callout that withholds a nested body, and every
//     other failure path in this subsystem resolves toward hiding (§5.6.2). A GM
//     who wrote `+` over a `-` made a mistake, and the resolution of that mistake is
//     not to publish it.
func TestANestedCalloutInsideARevealedOneReachesNoReader(t *testing.T) {
	planted := fixtureDemo(t, func(vault demoVaultDir) error {
		vault.write(t, nestedFixturePage, nestedFixtureSource)

		return nil
	})

	source := planted.vault.page(t, nestedFixturePage)

	secrets := demoSecretsOf(source)
	if len(secrets) != 2 {
		t.Fatalf("the planted page holds %d callout(s), want 2: a revealed outer and a "+
			"collapsed inner. If the scanner does not find the inner one the fixture is "+
			"not the shape it claims to be, and every assertion below would pass over a "+
			"page with nothing nested in it", len(secrets))
	}

	// **The author's bytes and the scanner's answer are asserted separately, and
	// they disagree.** That disagreement *is* the rule: `scanRange` forces an outer
	// callout that contains another to `SecretCollapsed` whatever its marker byte
	// says, so `ScanSecrets` cannot report `+` for this outer — which means the
	// scanner is the wrong place to read the author's byte from, and `demoMarkerBytes`
	// exists to read it from the file instead.
	written := demoMarkerBytes(source)

	if len(written) != 2 {
		t.Fatalf("the planted page carries %d marker byte(s) in its source, want 2. "+
			"The two halves of this fixture are a byte in the file and an answer from "+
			"the scanner, and they are read from two places on purpose", len(written))
	}

	if written[0] != secretMarkerRevealed {
		t.Errorf("the planted page's outer callout is written %q. It has to be %q: "+
			"a collapsed outer would be cut whole by the redactor and the nesting "+
			"would never have been a question", written[0], secretMarkerRevealed)
	}

	if written[1] != secretMarkerCollapsed {
		t.Errorf("the planted page's inner callout is written %q, want %q", written[1],
			secretMarkerCollapsed)
	}

	for index, secret := range secrets {
		if secret.state != secretMarkerCollapsed {
			t.Errorf("the scanner reports the planted page's callout %d as %q, want %q. "+
				"An outer callout that nests another is forced collapsed whatever its "+
				"marker byte says, so %q here means the override did not run",
				index, secret.state, secretMarkerCollapsed, secretMarkerRevealed)
		}
	}

	url := planted.base + "/c/" + showcaseSlug + "/wiki/notes/e2e-nested"

	gmDocument := planted.requireDocument(t, planted.gm, url)

	for _, needle := range []string{nestedOuterNeedle, nestedInnerNeedle} {
		if carried := demoCarry(gmDocument.root, needle); len(carried) == 0 {
			t.Fatalf("the Game Master's response for the planted page does not carry "+
				"%q. So every withholding assertion below would pass on a needle "+
				"nothing renders", needle)
		}
	}

	// **The renderer and the redactor read two different states, and this is where
	// the suite pins that down.**
	//
	// `ext/secret.go` puts the **authored** marker byte into the rendered element:
	// the file says `+`, so the Game Master's document says `data-secret="revealed"`.
	// `content.ScanSecrets` reports the **forced** state, so the redactor cuts the
	// whole outer callout for a player. Two states, two readers, and no reader is
	// shown anything they may not see — the Game Master's page calling a callout
	// "revealed" is not a disclosure, because the only reader who receives that page
	// is entitled to its contents anyway.
	//
	// Asserted rather than left as a comment, because this is the split a later
	// change is most likely to close in the dangerous direction: a renderer reading
	// the forced state would render the outer collapsed (harmless), and a **redactor
	// reading the authored byte would publish the inner callout** — phase 10's
	// defect, one layer along.
	forced := demoElementsWithAttr(gmDocument.root, secretStateAttribute, secretStateRevealed)
	if len(forced) == 0 {
		t.Errorf("the Game Master's response for the planted page carries no "+
			"`%s=%q` element. The file's outer callout is authored revealed and "+
			"`ext/secret.go` writes the authored byte into the element, so its absence "+
			"means the callout did not render as a callout at all",
			secretStateAttribute, secretStateRevealed)
	}

	playerDocument := planted.requireDocument(t, planted.player, url)

	for _, needle := range []string{nestedOuterNeedle, nestedInnerNeedle} {
		if carried := demoCarry(playerDocument.root, needle); len(carried) > 0 {
			t.Errorf("a player receives %q from the planted nested page, in %v. The "+
				"inner callout is %q and the outer one is the one the author revealed; "+
				"neither may reach a reader who may not see a secret",
				needle, carried, nestedInnerNeedle)
		}
	}

	// **No secret element at all**, which is stronger than "no text": a renderer that
	// emitted the outer revealed callout with the inner one cut out of its middle
	// would carry no inner text and still be a page whose structure says there is a
	// secret here.
	if elements := demoElementsWithAttr(
		playerDocument.root, secretExtensionAttr, secretExtensionValue,
	); len(elements) != 0 {
		t.Errorf("a player's response for the planted page carries %d `secret` "+
			"element(s): %s. The outer callout is forced collapsed, so the redactor "+
			"cuts it whole and the page should carry none",
			len(elements), describeElements(elements))
	}
}

// TestTheGameMasterAndAPlayerHoldDifferentValidatorsForAPageWithASecret is ADR 0016,
// against the artefact rather than against a fixture.
//
// **The `ETag` is salted with `include_secrets`,** so a Game Master's validator and a
// player's are two cache entries rather than one. Without the salt the first reader
// to warm a shared cache decides what every later reader is served — and on a
// public campaign the first reader is a stranger.
//
// One assertion and it is a strong one: **the two validators must differ**. Equal
// validators mean one representation, and one representation of this page cannot
// satisfy both a Game Master and a player.
func TestTheGameMasterAndAPlayerHoldDifferentValidatorsForAPageWithASecret(t *testing.T) {
	boot := sharedDemo(t)

	page, found := firstPageWithBothSecretStates(t, boot)
	if !found {
		t.Fatal("no page of the showcase campaign carries both a collapsed and a " +
			"revealed callout, so there is no page whose two readers would be served " +
			"two different representations of the same source")
	}

	url := boot.base + page.url()

	gmValidator := demoValidator(t, boot.gm, url)
	playerValidator := demoValidator(t, boot.player, url)

	if gmValidator == playerValidator {
		t.Fatalf("%s answers %s and %s with the same validator %s. The cache key is "+
			"salted with `include_secrets`, so the two are two entries; one validator "+
			"means a shared cache may hand one reader's response to the other, which "+
			"is the disclosure ADR 0016 exists to prevent",
			page.url(), demoGM, demoPlayer, gmValidator)
	}
}

// firstPageWithBothSecretStates is the first showcase page carrying one callout of
// each state.
//
// Deterministic because `demoSecretsInVault` walks in sorted order, and the choice
// is stated rather than left to whichever page the filesystem offered first.
func firstPageWithBothSecretStates(t *testing.T, boot *demoBoot) (demoPage, bool) {
	t.Helper()

	for _, entry := range demoSecretsInVault(t, boot.vault) {
		if entry.page.campaign != showcaseSlug {
			continue
		}

		collapsed, revealed := 0, 0

		for _, secret := range entry.secrets {
			if secret.collapsed() {
				collapsed++
			} else {
				revealed++
			}
		}

		if collapsed > 0 && revealed > 0 {
			return entry.page, true
		}
	}

	return demoPage{}, false
}
