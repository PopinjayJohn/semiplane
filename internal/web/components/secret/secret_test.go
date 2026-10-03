package secret_test

// The audits: §4.10's disclosure surface, and the decision this work item exists
// to make testable.
//
// # The decision, in one line
//
// **A redacted secret is silent.** Nothing in the player-facing document's
// accessibility tree says a secret was there — no element, no class, no `data-`
// attribute, no announcement, no live region, no `aria-hidden` tombstone.
// `TestThePlayerDocumentCarriesNoTraceOfAHiddenSecret` holds it over a document the
// **real** pipeline built, and the six ways of leaving a trace are in
// `controls_test.go`.
//
// # Why the document under test is built, not written
//
// Every fixture here that is about a *secret* renders through
// `internal/content`'s redactor, `internal/content`'s renderer, and
// `components.Shell`. A hand-written "player document" would pass whether or not
// the redactor cut the callout, whether or not the sanitiser kept the element, and
// whether or not the shell put something in the tree beside it — so it would be a
// claim about a string.
//
// The corpus has callouts in every position that matters: one **at a block
// boundary**, which §4.10.2 says leaves a clean gap; one **mid-paragraph**, which
// is the case the record calls out — "omission leaves a sentence that reads oddly"
// — and one already revealed, so the fixture is not a page where every secret is
// hidden and the assertions are trivially true.
//
// # A11Y_TESTS
//
// `Structural`, `Target`, `Vocabulary`, `Contrast`, `BuiltStylesheet`,
// `EveryRoute`, `SheetIsInsideTheBuild`, `SheetDeclaresNo`, `RuleInTheSheet`,
// `SelectorInThisSheet` and `SheetIsResponsibleFor` are the gate's pattern;
// `css_test.go` covers the sheet half. The **Makefile change that names this
// package in `A11Y_COMPONENT_PKGS` is the integrator's**, and
// `TestTheAuditNamesMatchTheGatePattern` reads the Makefile's own pattern to check
// the names would be found — so the claim does not depend on that line having
// landed yet, and `TestTheGateNamesThisPackageIsTheIntegratorsToMake` says so
// loudly rather than silently.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"golang.org/x/net/html"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/web/components"
	"github.com/semiplane/semiplane/internal/web/components/chrome"
	"github.com/semiplane/semiplane/internal/web/components/edit"
	"github.com/semiplane/semiplane/internal/web/components/live"
	"github.com/semiplane/semiplane/internal/web/components/secret"
)

// retiredEntities is UI §1.2's two words, read from one place.
//
// A package-level list rather than a literal inside the audit, because the rule is
// UI §1.2's and a second copy of the list is a second answer to it.
var retiredEntities = []string{"world", "session"}

// authorTextClasses are the two elements this package renders an author's own words
// into.
//
// **A closed list of classes, not a rule about words.** The vocabulary audit needs
// a carve-out — a GM whose campaign is about a fantasy world will write "world" in
// a callout's title — and a carve-out that decided by *reading* the text would be
// one whose width depends on what the author happened to write. Naming the two
// elements puts the width in the markup, where a reviewer can see it.
var authorTextClasses = []string{"secret-disclosure-name", "secret-chrome-title"}

// The corpus's three callouts, and the words that identify them.
//
// **Each of these appears in exactly one fixture.** A finding about one of them is
// therefore unambiguously a finding about a secret and not a coincidence.
const (
	secretTitleBoundary = "Aldric replaced it"
	secretBodyBoundary  = "the eastern signal fire"
	secretTitleMid      = "the harbour key"
	secretBodyMid       = "mira's passphrase"
)

// stubLabel is §4.10.2's stub label, verbatim.
//
// A document carrying it to a player has disclosed that a secret was here, whether
// it came from a stub or from something else.
const stubLabel = "A GM-only note"

// corpus is a page carrying the two hidden callouts and one revealed one.
//
// `[!secret]+` is here so the fixture is not a page where every secret is hidden:
// with nothing revealed, an assertion that "the revealed marker never reaches a
// player" is satisfied by a document that renders nothing at all, which is a much
// weaker claim than it looks.
const corpus = `# The signal fire

The eastern watch reported the fire at dusk, and

> [!secret]-
> ` + secretTitleBoundary + `. He also took ` + secretBodyBoundary + `.

> [!secret]-
> ` + secretTitleMid + ` is ` + secretBodyMid + `, which no member of the party
> has been told.

> [!secret]+
> The harbourmaster saw the boat leave before dawn.

The watch changed shift at the second bell.`

// render parses one component's output into a document node.
//
// Parsing rather than asserting on the string, for the file header's reason: the
// audits are about what a screen reader reaches, and a substring search over
// markup cannot see a word in a comment or in an attribute name.
func render(t *testing.T, component templ.Component) *html.Node {
	t.Helper()

	var out strings.Builder

	if err := component.Render(context.Background(), &out); err != nil {
		t.Fatalf("render: %v", err)
	}

	document, err := html.Parse(strings.NewReader(out.String()))
	if err != nil {
		t.Fatalf("parse the rendered document: %v", err)
	}

	return document
}

// markupOf renders one component to a string, for `Decide`'s input.
func markupOf(t *testing.T, component templ.Component) string {
	t.Helper()

	var out strings.Builder

	if err := component.Render(context.Background(), &out); err != nil {
		t.Fatalf("render: %v", err)
	}

	return out.String()
}

// fragment renders components in order, as one.
//
// `templ.ComponentFunc` rather than a hand-written `Render` so the composition is
// the components and nothing else — a wrapper that re-ordered them would be a
// third component, and this is a fixture rather than a product.
func fragment(parts ...templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, out io.Writer) error {
		for _, component := range parts {
			if err := component.Render(ctx, out); err != nil {
				return fmt.Errorf("rendering a fixture fragment: %w", err)
			}
		}

		return nil
	})
}

// shellView is the chrome every fixture in this package is rendered through.
//
// One fixture for all of them, and the reason is that the shell is where the skip
// links and the `role="banner"` come from: rendering two shells would test the
// shells and not this surface.
func shellView() components.ShellView {
	return components.ShellView{
		Title:    "Secrets",
		Instance: components.InstanceView{Name: "Greyhaven", Version: "0.10.0"},
		Account:  components.AccountView{Username: "mira"},
		Campaign: chrome.CampaignRef{Slug: "greyhaven", Name: "Greyhaven"},
	}
}

// pageBody renders the corpus through the **real** pipeline.
//
// `content.OmitSecrets` is the redactor P10 installs and `content.NewRenderer` is
// the renderer the wiki route uses, so `includeSecrets=false` here is exactly the
// flag the route derives from its access gate — not a stand-in for it.
func pageBody(t *testing.T, includeSecrets bool) string {
	t.Helper()

	redacted, err := content.OmitSecrets().Redact(corpus, includeSecrets)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}

	rendered, err := content.NewRenderer("greyhaven", nil).
		Render(content.Document{Body: redacted})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	return rendered.HTML
}

// playerWikiDocument is what a player receives for the corpus: the shell, the page,
// and the redacted body.
//
// **A whole document, not a fragment**, because the claim is about the document a
// reader's assistive technology loads. A test that only walked the page body would
// pass while the shell said something.
func playerWikiDocument(t *testing.T) *html.Node {
	t.Helper()

	return render(t, components.WikiPage(components.WikiPageView{
		Shell:   shellView(),
		Heading: "The signal fire",
		Body:    pageBody(t, false),
	}))
}

// gmWikiDocument is the same page as the GM receives it, and it is the **control**
// for every "no trace" assertion below.
//
// A negative control that is not itself a control is a red light wired to nothing.
func gmWikiDocument(t *testing.T) *html.Node {
	t.Helper()

	return render(t, components.WikiPage(components.WikiPageView{
		Shell:   shellView(),
		Heading: "The signal fire",
		Body:    pageBody(t, true),
	}))
}

// disclosuresView is the GM's disclosure panel over the corpus's three callouts.
//
// Two hidden and one revealed, so the fixture exercises both branches of the
// per-callout control — a panel where every callout is hidden leaves the revealed
// branch unreached.
func disclosuresView() secret.DisclosuresView {
	return secret.DisclosuresView{
		Endpoint:  secret.Endpoint("greyhaven", "Lore/Signal_Fire"),
		Page:      "Signal Fire",
		Validator: `"sp1:0f3a"`,
		Marker:    secret.MarkerShown,
		Callouts: []secret.CalloutView{
			{
				Anchor:  "signal-fire",
				Ordinal: 0,
				Title:   secretTitleBoundary,
				State:   secret.StateHidden,
			},
			{
				Anchor:  "d41d8cd98f00",
				Ordinal: 1,
				Title:   secretTitleMid,
				State:   secret.StateHidden,
			},
			{
				Anchor:  "harbourmaster",
				Ordinal: 2,
				Title:   "The harbourmaster",
				State:   secret.StateRevealed,
			},
		},
	}
}

// editorDocument is the editor with the disclosure panel mounted.
//
// **Composed rather than by editing `edit.EditorSurface`**, and the reason is the
// ownership this work item was given: `internal/web/components/edit` is the
// integrator's. Composing it in the test is what makes the claim honest anyway —
// the panel is asserted *inside a real editor document*, with the real save control
// beside it, which is what lets "a reveal is not a save" compare two markup shapes
// rather than assert them in isolation.
//
// The panel is placed in the centre slot after the editor section rather than
// inside `previewPane`, which is where the integrator will mount it. None of this
// file's audits depend on the difference — §10.2's rules are about the document's
// landmarks, its heading sequence and its references, and §10.6's is about every
// focus stop wherever it is — and composing it outside `edit` is what lets the test
// exist before the mount does.
func editorDocument(t *testing.T) *html.Node {
	t.Helper()

	view := edit.EditorView{
		Shell:       shellView(),
		Page:        "Signal Fire",
		Buffer:      corpus,
		Validator:   `"sp1:0f3a"`,
		ContentHash: "9f2bc41d8cd98f00b204e9800998ecf8427e",
		Preview:     pageBody(t, true),
		State:       edit.SaveIdle,
	}

	centre := fragment(edit.EditorSurface(view), secret.Disclosures(disclosuresView()))

	return render(t, fragment(
		components.Shell(view.Shell, centre, edit.EditorRail(view)),
	))
}

// --- THE DECISION --------------------------------------------------------------

// TestThePlayerDocumentCarriesNoTraceOfAHiddenSecret is UI §4.10.1, §5.6.1 and the
// hard decision, as one test with a subtest per rule so a failure names the rule.
//
// Four rules, and each is a different disclosure:
//
//  1. **No text.** Nothing a screen reader reads names a secret: not the body, not
//     the title, and not the stub's label — which is what a stub would put there
//     and what an announcement would put there.
//  2. **No attribute names a *hidden* callout.** A revealed callout is public —
//     §5.6.1 cuts `-` and keeps `+`, so a player document legitimately carries
//     `data-secret="revealed"` and `data-ext="secret"` on a callout whose body
//     every reader may see. What must not appear is any attribute or class that
//     identifies a callout whose body was removed: `data-secret="collapsed"`, a
//     `secret--collapsed` class, or any `data-secret-*` attribute naming an anchor
//     or a control. A `data-` attribute is not announced, so a test reading only
//     text would pass on a document whose every removed callout was still
//     identified by its state.
//  3. **No `aria-hidden`.** The tempting way to make an element invisible to
//     assistive technology while leaving it for everyone else is still a node whose
//     existence says something was there, and §4.10.1 names it.
//  4. **No live region.** §7.5's announced-content table is a closed list and a
//     redacted secret is not on it.
func TestTheRedactedPageSatisfiesTheStructuralContractOfOmission(t *testing.T) {
	t.Parallel()

	document := playerWikiDocument(t)

	t.Run("no text names a removed secret", func(t *testing.T) {
		t.Parallel()

		assertNoTextNamesASecret(t, document)
	})

	t.Run("no attribute names a hidden callout", func(t *testing.T) {
		t.Parallel()

		assertNothingIdentifiesAHiddenCallout(t, document)
	})

	t.Run("nothing is aria-hidden", func(t *testing.T) {
		t.Parallel()

		assertNoAriaHidden(t, document)
	})

	t.Run("nothing announces", func(t *testing.T) {
		t.Parallel()

		assertNoLiveRegionInThePlayerDocument(t, document)
	})
}

// assertNoTextNamesASecret is rule 1.
//
// Scans four places, because a secret phrase left in any one of them is the same
// disclosure and reading three of the four is how the fourth slips through: a text
// node, a comment, an attribute's **value** and an attribute's **name**.
//
// `nodeOwnBytes` is what makes this correct, and it is a **set of (value, place)
// pairs** rather than one string per node. The first version passed the element and
// read its *descendants'* text while `where` strings named attributes, so every
// attribute on a leaking element produced a finding blaming an attribute that did
// not contain the phrase, and a phrase that lived in an attribute value produced
// none at all. Both halves were visible the moment the mutation that emits a stub
// was run: fourteen findings, all of them naming `lang="en"`.
func assertNoTextNamesASecret(failer auditFailer, document *html.Node) {
	failer.Helper()

	forbidden := []string{
		secretBodyBoundary, secretTitleBoundary,
		secretBodyMid, secretTitleMid,
		stubLabel,
	}

	walkAll(failer, document, func(node *html.Node) {
		for _, said := range nodeOwnBytes(node) {
			word, found := matchingWord(said.value, forbidden)
			if !found {
				continue
			}

			failNode(failer, node, secretLeakMessage, said.where, word)
		}
	})
}

// spokenBy is one place a node says something, and the phrase naming that place.
//
// A pair rather than a bare string because a finding has to name *which* of the
// four it is about; a rule that reports "this document contains a secret" leaves
// the reader to go and find it.
type spokenBy struct {
	where string
	value string
}

// nodeOwnBytes is everything a node itself says, each with where to find it.
//
// **Four places, and an element contributes only two of them.** An element's
// descendants' text is not one of them, because `walkAll` reaches every text node
// already: including it reported the same phrase once per ancestor, which is what
// made the first version of this audit's findings unreadable — eight findings for
// one leaked phrase, on `<html>`, `<body>`, `<div>`, `<main>`, `<div>` and `<p>`.
//
// So an element contributes its **attribute names** and its **attribute values**,
// and a text or comment node contributes itself. The attribute name is the fourth
// place and it is not hypothetical: `data-secret-body="the eastern signal fire"`
// puts the phrase where `html.Parse` can see it and no renderer ever prints it,
// which is exactly the byte a player with `curl` reads.
func nodeOwnBytes(node *html.Node) []spokenBy {
	switch node.Type {
	case html.TextNode:
		return []spokenBy{{where: "its text", value: node.Data}}
	case html.CommentNode:
		return []spokenBy{{where: "its comment", value: node.Data}}
	case html.ElementNode:
		said := make([]spokenBy, 0, 2*len(node.Attr))

		for _, attr := range node.Attr {
			said = append(said,
				spokenBy{where: "the attribute name " + attr.Key, value: attr.Key},
				spokenBy{
					where: "the attribute " + attr.Key + "=" + strconv.Quote(attr.Val),
					value: attr.Val,
				},
			)
		}

		return said
	default:
		return nil
	}
}

// secretLeakMessage is why a leaked phrase fails, in one place, because three
// callers format it and three hand-formatted copies drift.
const secretLeakMessage = "%s contains %q. §5.6.1 is omission: the secret body, " +
	"the title that names it, and the existence of both must be absent from every " +
	"byte a non-GM receives. §4.10.1 rules out the announcement and the stub for " +
	"exactly this reason — an oracle for where secrets are is the disclosure"

// matchingWord is the forbidden phrase a value contains, and whether one did.
//
// **Case-folded on both sides.** A vault's prose is the author's, and an author who
// capitalises a callout's title has not written a different secret; a scan that
// matched exactly would be a scan that passes on a rephrasing.
func matchingWord(value string, forbidden []string) (string, bool) {
	lowered := strings.ToLower(value)

	for _, word := range forbidden {
		if strings.Contains(lowered, strings.ToLower(word)) {
			return word, true
		}
	}

	return "", false
}

// assertNothingIdentifiesAHiddenCallout is rule 2.
//
// **Scoped to hidden callouts, and the scoping is the substance.** §5.6.1's
// redactor cuts `-` and keeps `+`: a revealed callout is public and its element is
// in the player's document by design, `data-secret="revealed"` and all. A rule that
// refused the whole `data-secret*` family would therefore be a rule that failed on
// the product working, and a rule like that gets deleted rather than fixed.
//
// What must not survive is anything identifying a callout whose **body was
// removed**:
//
//   - `data-secret` with any value but `revealed`;
//   - a `secret--collapsed` class token;
//   - any `data-secret-*` attribute that is not `data-secret` itself — the control
//     and anchor attributes belong to the GM's chrome and a player's document
//     carrying one names a callout that is not there.
func assertNothingIdentifiesAHiddenCallout(failer auditFailer, document *html.Node) {
	failer.Helper()

	walk(failer, document, func(node *html.Node) {
		if state, ok := attribute(node, "data-secret"); ok && state != "revealed" {
			failNode(failer, node,
				"carries data-secret=%q. §5.6.1 is omission: a callout whose "+
					"body was removed must leave nothing in the document that "+
					"says it was there, and a state attribute is exactly that. "+
					"(data-secret=\"revealed\" is correct -- a revealed callout "+
					"is public and its element belongs in this document)",
				state)
		}

		if hasClass(node, "secret--collapsed") {
			failNode(failer, node,
				"carries the secret--collapsed class. §5.6.1 rules the class out "+
					"as a hiding mechanism because the response body reaches any "+
					"player with curl — and a modifier that names the *hidden* "+
					"state has no business in a player document at all")
		}

		if hasClass(node, "secret") && !hasClass(node, "secret--revealed") {
			failNode(failer, node,
				"carries the bare secret class with no revealed modifier; a "+
					"rendered callout in a player document is a *revealed* one, "+
					"so the class list says this element is something the reader "+
					"was not shown")
		}

		for _, name := range anyAttributeNamed(node, "data-secret") {
			if name == "data-secret" {
				continue
			}

			failNode(failer, node,
				"carries %s. That attribute family belongs to the GM's controls "+
					"and belongs to no player document at all", name)
		}

		if value, ok := attribute(node, "data-ext"); ok && value == "secret" &&
			attributeOr(node, "data-secret") != "revealed" {
			failNode(failer, node,
				"carries data-ext=\"secret\" on an element that is not a revealed "+
					"callout; this attribute is how a rendered callout identifies "+
					"itself, and a player reading the markup has read it")
		}
	})
}

// assertNoAriaHidden is rule 3, and it is about the attribute's **presence**.
//
// The failure it exists for is a template reaching for `aria-hidden` to make a
// collapsed callout "not announced". That is the second-worst answer: it leaves the
// element, its class and its text in the response, and adds a node whose existence
// says something was there.
func assertNoAriaHidden(failer auditFailer, document *html.Node) {
	failer.Helper()

	walk(failer, document, func(node *html.Node) {
		if hasAttribute(node, "aria-hidden") {
			failNode(failer, node,
				"carries aria-hidden. §4.10.1: a redacted secret is not in the DOM "+
					"at all — no element, no aria-hidden, no placeholder, no "+
					"role=\"presentation\". An aria-hidden element is still an "+
					"element, and its presence is a disclosure")
		}
	})
}

// assertNoLiveRegionInThePlayerDocument is rule 4: nothing announces.
func assertNoLiveRegionInThePlayerDocument(failer auditFailer, document *html.Node) {
	failer.Helper()

	walk(failer, document, func(node *html.Node) {
		role := attributeOr(node, "role")
		if role == "alert" || role == "status" || role == "log" {
			failNode(failer, node,
				"carries role=%q. §7.5's announced-content table is a closed list "+
					"and a redacted secret is not on it; a live region on this "+
					"document would announce the existence of a disclosure the "+
					"page is required not to carry",
				role)
		}

		if politeness, ok := attribute(node, "aria-live"); ok && politeness != "off" {
			failNode(failer, node,
				"carries aria-live=%q. Stated explicitly because an element with "+
					"no aria-live inherits its nearest live-region ancestor's "+
					"politeness, so \"not stated\" is not \"not announced\"",
				politeness)
		}
	})
}

// The four rules, again, as named functions so a control can run them by name.
//
// **Not copies.** The same bodies the player-facing subtests call, reached through
// the `auditFailer` interface; a copy is a second answer to the same question.
func auditTextFindsASecret(failer auditFailer, document *html.Node) {
	failer.Helper()
	assertNoTextNamesASecret(failer, document)
}

func auditAttributeFindsAHiddenCallout(failer auditFailer, document *html.Node) {
	failer.Helper()
	assertNothingIdentifiesAHiddenCallout(failer, document)
}

func auditAriaHiddenFindsIt(failer auditFailer, document *html.Node) {
	failer.Helper()
	assertNoAriaHidden(failer, document)
}

func auditLiveRegionFindsIt(failer auditFailer, document *html.Node) {
	failer.Helper()
	assertNoLiveRegionInThePlayerDocument(failer, document)
}

// EveryRouteAuditOnTheRedactedPageObjectsToEveryLeakItClaimsTo is the control for
// **all four** rules, and it is deliberately *not* the GM's copy of the page.
//
// # Why two of the four need a fixture and two do not
//
// The text rule and the attribute rule have a real positive instance: the GM's copy
// of the corpus carries both removed callouts with their bodies, and the two
// revealed ones with neither. A positive control that is a *real document* is worth
// a great deal — it proves the redactor, the renderer, the sanitiser and the shell
// all still put the thing there, so the negative assertion is not passing because
// the fixture was empty.
//
// `aria-hidden` and the live-region rule have **no positive instance anywhere in
// the product**, and that is the finding rather than a gap: nothing in semiplane
// renders an `aria-hidden` element, and the only live regions are the ones §7.5
// names. So their controls are fixtures, which is exactly what
// `controls_test.go` is for. Saying so here is the point: a rule whose control had
// to be invented is a rule whose absence in the product is unverified, and the
// reader is entitled to know which two those are.
//
// Each fixture below is built to violate **exactly one** rule, so a finding that
// mentions a different one is a failure rather than a coincidence.
func TestEveryRouteAuditOnTheRedactedPageObjectsToEveryLeakItClaimsTo(t *testing.T) {
	t.Parallel()

	// The GM's own document: the one place these two leaks are *correct*, so a
	// control run over it says the audit is targeting rather than merely absent.
	// `real` is a predeclared identifier, hence the field's name.
	gmDocument := gmWikiDocument(t)

	for _, row := range []struct {
		name     string
		document *html.Node
		subject  string
		audit    func(auditFailer, *html.Node)
		inGM     bool
	}{
		{
			name:     "text names a removed secret",
			document: gmDocument,
			subject:  secretBodyBoundary,
			audit:    auditTextFindsASecret,
			inGM:     true,
		},
		{
			name:     "an attribute names a hidden callout",
			document: gmDocument,
			subject:  `data-secret="collapsed"`,
			audit:    auditAttributeFindsAHiddenCallout,
			inGM:     true,
		},
		{
			name:     "a stub survives in the tree",
			document: render(t, secret.Stub(secret.StubView{Enabled: true})),
			subject:  stubLabel,
			audit:    auditTextFindsASecret,
		},
		{
			name:     "an aria-hidden tombstone",
			document: parse(t, `<main><p aria-hidden="true">the eastern signal fire</p></main>`),
			subject:  "aria-hidden",
			audit:    auditAriaHiddenFindsIt,
		},
		{
			name:     "an announcement of the omission",
			document: parse(t, `<main><p role="alert" aria-live="polite">A GM-only note was removed here.</p></main>`),
			subject:  `role="alert"`,
			audit:    auditLiveRegionFindsIt,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			recorder := &recordingFailer{}

			row.audit(recorder, row.document)

			if recorder.failures == 0 {
				t.Fatalf("the audit reported nothing over a document carrying "+
					"%s, so it would report nothing over a document that "+
					"really did leak one: it is a rule that cannot fail",
					row.subject)
			}

			if !recorder.mentions(row.subject) {
				t.Errorf("the audit reported %d finding(s) but none mentioned "+
					"%q; a rule that fires for an unrelated reason looks "+
					"exactly like one that works",
					recorder.failures, row.subject)
			}

			if !row.inGM {
				return
			}

			// The control has to be silent on the player document too, or
			// "the audit fired" and "the audit is right" are two claims
			// that have not been told apart.
			clean := &recordingFailer{}
			row.audit(clean, playerWikiDocument(t))

			if clean.failures != 0 {
				t.Errorf("the same audit reported %d finding(s) on the "+
					"player-facing document: %s", clean.failures,
					strings.Join(clean.messages, " | "))
			}
		})
	}
}

// parse reads a markup literal into a document, for the fixtures above.
//
// `html.Parse` because a fixture that is not well-formed would silently become a
// different document than the one the assertion is about.
func parse(t *testing.T, markup string) *html.Node {
	t.Helper()

	document, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatalf("parse the fixture: %v", err)
	}

	return document
}

// recordingFailer collects findings instead of failing a test.
//
// `Helper` is a no-op, which is the documented behaviour rather than an omission:
// there is no `*testing.T` for a finding that is the point.
type recordingFailer struct {
	failures int
	messages []string
}

// Helper is a no-op.
func (r *recordingFailer) Helper() {}

// Errorf records one finding.
func (r *recordingFailer) Errorf(format string, args ...any) {
	r.failures++
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

// mentions reports whether any finding's text contains a substring.
func (r *recordingFailer) mentions(needle string) bool {
	for _, message := range r.messages {
		if strings.Contains(message, needle) {
			return true
		}
	}

	return false
}

// --- THE SURFACE'S STRUCTURE ---------------------------------------------------

// TestTheDisclosureSurfaceSatisfiesTheStructuralContract is UI §10.2, as one test
// with a subtest per rule so a failure names the rule rather than "the a11y test
// failed".
func TestTheDisclosureSurfaceSatisfiesTheStructuralContract(t *testing.T) {
	t.Parallel()

	document := editorDocument(t)

	t.Run("landmarks are present and distinguishing", func(t *testing.T) {
		t.Parallel()

		assertLandmarksArePresentAndDistinguishing(t, document)
	})

	t.Run("heading levels never skip", func(t *testing.T) {
		t.Parallel()

		assertHeadingLevelsNeverSkip(t, document)
	})

	t.Run("every reference resolves", func(t *testing.T) {
		t.Parallel()

		assertEveryReferenceResolves(t, document)
	})

	t.Run("no inline outline suppression", func(t *testing.T) {
		t.Parallel()

		assertNoInlineOutlineSuppression(t, document)
	})

	t.Run("no aria-hidden on a focus stop", func(t *testing.T) {
		t.Parallel()

		assertNoAriaHiddenOnAFocusStop(t, document)
	})

	t.Run("skip links come first and resolve", func(t *testing.T) {
		t.Parallel()

		assertSkipLinksComeFirstAndResolve(t, document)
	})
}

// assertLandmarksArePresentAndDistinguishing is §10.2's landmark rule.
//
// A document with two `navigation` landmarks and no name on either is one where a
// reader navigating by landmark hears "navigation" twice; §7.2 forbids the
// unlabelled duplicate.
func assertLandmarksArePresentAndDistinguishing(failer auditFailer, document *html.Node) {
	failer.Helper()

	seen := map[string]int{}

	walk(failer, document, func(node *html.Node) {
		role := attributeOr(node, "role")

		switch role {
		case "banner", "main", "contentinfo":
			if seen[role] != 0 {
				failNode(failer, node, "a second role=%q; §10.2 requires exactly one of each", role)
			}

			seen[role]++

		case "navigation":
			seen[role]++

			if strings.TrimSpace(accessibleName(node)) == "" {
				failNode(failer, node,
					"role=\"navigation\" with no accessible name; §7.2 forbids an "+
						"unlabelled duplicate and a reader navigating by landmark "+
						"cannot find this one")
			}
		default:
		}
	})

	for _, required := range []string{"banner", "main", "contentinfo"} {
		if seen[required] == 0 {
			failer.Helper()
			failer.Errorf("the document has no role=%q landmark; §10.2 requires "+
				"the shell's four", required)
		}
	}
}

// assertHeadingLevelsNeverSkip is §7.2's rule.
//
// A document whose headings start at `<h3>` has exactly one `<h1>`-shaped hole and
// no outline at all, so the sequence is compared rather than the set.
func assertHeadingLevelsNeverSkip(failer auditFailer, document *html.Node) {
	failer.Helper()

	previous := 0

	walk(failer, document, func(node *html.Node) {
		level, isHeading := headingLevel(node)
		if !isHeading {
			return
		}

		if previous != 0 && level > previous+1 {
			failNode(failer, node,
				"an <h%d> after an <h%d>; §7.2 requires heading levels never to "+
					"skip, and a skipped level is a hole in the outline rather "+
					"than a level a reader chose",
				level, previous)
		}

		previous = level
	})
}

// assertEveryReferenceResolves is §10.2's reference-integrity rule.
//
// Every `aria-controls`, `aria-labelledby`, `aria-describedby`, `aria-owns` and a
// skip link's `href="#…"` must name an id in the document. A reference to nothing is
// invisible in a rendering and is what a region with no accessible name looks like
// from a reader's side.
func assertEveryReferenceResolves(failer auditFailer, document *html.Node) {
	failer.Helper()

	ids := map[string]bool{}

	walk(failer, document, func(node *html.Node) {
		id := attributeOr(node, "id")
		if id == "" {
			return
		}

		if ids[id] {
			failNode(failer, node, "duplicates the id %q; §10.2 requires an id to be unique", id)
		}

		ids[id] = true
	})

	references := []string{
		"aria-controls", "aria-labelledby", "aria-describedby", "aria-owns",
	}

	walk(failer, document, func(node *html.Node) {
		for _, name := range references {
			for id := range strings.FieldsSeq(attributeOr(node, name)) {
				if !ids[id] {
					failNode(failer, node,
						"%s names %q and the document has no such id; §10.2's "+
							"reference-integrity rule exists because this is "+
							"invisible in a rendering", name, id)
				}
			}
		}

		if node.Data != "a" {
			return
		}

		href := attributeOr(node, "href")
		if !strings.HasPrefix(href, "#") || href == "#" {
			return
		}

		if !ids[strings.TrimPrefix(href, "#")] {
			failNode(failer, node,
				"a skip link to %q and the document has no such id; §7.2's rule "+
					"is that a skip link points at a landmark this route "+
					"actually renders, because a link to a landmark that is not "+
					"here moves focus nowhere", href)
		}
	})
}

// assertNoInlineOutlineSuppression is §10.2's rule that an outline level is never
// set in markup.
//
// It exists because `outline` in an author's CSS is the one thing a stylesheet can
// use to bury a heading below the others, and the stylesheet is not markup an audit
// can see.
func assertNoInlineOutlineSuppression(failer auditFailer, document *html.Node) {
	failer.Helper()

	walk(failer, document, func(node *html.Node) {
		if style := attributeOr(node, "style"); strings.Contains(style, "outline") {
			failNode(failer, node,
				"carries an inline outline; §10.2 forbids suppressing an outline "+
					"in markup, which is invisible to a stylesheet audit")
		}
	})
}

// assertNoAriaHiddenOnAFocusStop is §10.2's rule and §7.10's prohibition.
func assertNoAriaHiddenOnAFocusStop(failer auditFailer, document *html.Node) {
	failer.Helper()

	walk(failer, document, func(node *html.Node) {
		if !hasAttribute(node, "aria-hidden") {
			return
		}

		if isFocusStop(node) {
			failNode(failer, node,
				"carries aria-hidden and is focusable; §7.10 prohibits aria-hidden "+
					"on a focus stop outright")
		}
	})
}

// assertSkipLinksComeFirstAndResolve is §7.2's rule.
//
// Both halves are one condition: "a link with no landmark" and "a landmark with no
// link" are both failures and only one is visible in a rendering.
func assertSkipLinksComeFirstAndResolve(failer auditFailer, document *html.Node) {
	failer.Helper()

	var order []*html.Node

	walk(failer, document, func(node *html.Node) {
		if isFocusStop(node) {
			order = append(order, node)
		}
	})

	if len(order) == 0 {
		failer.Helper()
		failer.Errorf("the document has no focus stops at all, so the skip-link " +
			"audit found nothing to check: a rule over an empty document is a " +
			"rule that cannot fail")

		return
	}

	seenNonSkip := false

	for _, node := range order {
		if hasClass(node, "skip-link") {
			if seenNonSkip {
				failNode(failer, node,
					"a skip link after another focus stop; §7.2 requires the skip "+
						"links to be the first focusable elements in the document, "+
						"because they are the only way past the navigation on a "+
						"keyboard")
			}

			continue
		}

		seenNonSkip = true
	}
}

// TestEveryFocusStopOnTheSecretSurfaceCarriesTheTargetClass is §10.6.
//
// §10.6 makes the target size "enforced by construction", and §7.3's construction
// is a shared utility plus a walk over every focus stop in the **served** document.
// **These controls are editor chrome, not a page body**, so
// `internal/content/target.go` never sees them — that pass adds the class to a page
// body's focusable elements. A class applied from the script that arrives with the
// behaviour would be invisible to this walk.
func TestEveryFocusStopOnTheSecretSurfaceCarriesTheTargetClass(t *testing.T) {
	t.Parallel()

	document := editorDocument(t)

	stops := 0

	walk(t, document, func(node *html.Node) {
		if !isFocusStop(node) {
			return
		}

		stops++

		if !hasClass(node, "target") {
			failNode(t, node,
				"is focusable and carries no .target class; §10.6's audit walks "+
					"the served document, so a class applied from the script "+
					"that arrives with the behaviour is invisible to it")
		}
	})

	if stops == 0 {
		t.Fatal("the editor document rendered no focus stops, so this audit " +
			"found nothing to check")
	}
}

// TestTheSecretSurfaceVocabularyFindsTheWordWhereverItIs is UI §1.2 and §10.2's
// last clause, named for the claim it makes rather than for the surface it runs
// over.
//
// The scan covers **every text node, every comment, every attribute value and every
// attribute name**, and each inclusion has a reason:
//
//   - **Comments**: a word in a comment is invisible to a reader, which is exactly
//     why an audit reading text nodes would pass on a document whose markup still
//     names a retired entity.
//   - **Attribute values**: an `aria-label` or a `title` is announced, so a retired
//     word named there is named to the person the rule protects.
//   - **Attribute names**: a `data-world` is invisible to a reader and obvious to a
//     developer grepping for the feature it implies, which makes it exactly as
//     retired as a label.
//
// **The one carve-out, and why it is this narrow**: the two elements this package
// fills with an author's own words — the disclosure row's name and the callout
// chrome's title. A GM whose campaign is about a fantasy world will write "world"
// in a callout title, and a rule that failed that document would be failing a
// reader for their own words. `controls_test.go` holds the carve-out to that width:
// the word is fine inside those two and a finding one element outside them.
func TestTheSecretSurfaceVocabularyFindsTheWordWhereverItIs(t *testing.T) {
	t.Parallel()

	document := render(t, components.Shell(
		shellView(),
		fragment(
			secret.Disclosures(disclosuresView()),
			secret.CalloutChrome(secret.CalloutChromeView{
				Title:   "The world of Greyhaven",
				Ordinal: 0,
				State:   secret.StateRevealed,
				Marker:  secret.MarkerShown,
			}),
			secret.Stub(secret.StubView{Enabled: true}),
		),
		components.InstanceRail(shellView().Instance),
	))

	walkAll(t, document, func(node *html.Node) {
		if inAuthorText(node) {
			return
		}

		value := readableBytesOf(node)
		if value == "" {
			return
		}

		lowered := strings.ToLower(value)

		for _, word := range retiredEntities {
			if !strings.Contains(lowered, word) {
				continue
			}

			failNode(t, node,
				"contains %q, which UI §1.2 retires. A GM whose campaign is "+
					"called \"The World Map\" will see their own title on their "+
					"own screen and no audit changes that — which is why the "+
					"only carve-out is the two elements this package fills with "+
					"an author's words, and this node is not one of them",
				word)
		}
	})
}

// inAuthorText reports whether a node sits inside one of the two elements this
// package fills with author text.
//
// **A class check on the ancestor chain, not a check of the words.** §4.10's
// chrome and the disclosure row both render author text, and a rule that decided by
// *reading* the text would be one whose width depends on what the author wrote.
func inAuthorText(node *html.Node) bool {
	for current := node; current != nil; current = current.Parent {
		if current.Type != html.ElementNode {
			continue
		}

		for _, class := range authorTextClasses {
			if hasClass(current, class) {
				return true
			}
		}
	}

	return false
}

// readableBytesOf is whatever a reader or a developer could read off a node: its
// own text, and every attribute's name and value.
func readableBytesOf(node *html.Node) string {
	switch node.Type {
	case html.TextNode, html.CommentNode:
		return node.Data
	case html.ElementNode:
		var out strings.Builder

		for _, attr := range node.Attr {
			out.WriteString(attr.Key)
			out.WriteString("=")
			out.WriteString(attr.Val)
			out.WriteString(" ")
		}

		return out.String()
	default:
		return ""
	}
}

// TestTheDisclosurePanelIsAbsentForAPageWithNoCallouts is §4.6's "absent rather
// than empty", applied to the disclosure panel.
//
// A GM opening the editor of a page with no secrets must not be told there are any,
// and must not be handed a heading with nothing under it.
func TestTheDisclosurePanelIsAbsentForAPageWithNoCallouts(t *testing.T) {
	t.Parallel()

	document := render(t, secret.Disclosures(secret.DisclosuresView{
		Endpoint:  secret.Endpoint("greyhaven", "Lore/Signal_Fire"),
		Page:      "Signal Fire",
		Validator: `"sp1:0f3a"`,
	}))

	if text := strings.TrimSpace(nodeText(document)); text != "" {
		t.Errorf("a disclosure panel with no callouts rendered the text %q; the "+
			"panel's own copy would tell a GM their page has secrets", text)
	}

	walk(t, document, func(node *html.Node) {
		if attributeOr(node, "data-testid") == secret.DisclosuresTestID {
			t.Errorf("the panel rendered at all with no callouts; §4.6 requires a " +
				"section with nothing in it to be absent rather than empty")
		}
	})
}

// --- A DISCLOSURE IS NOT A SAVE -------------------------------------------------

// TestTheRevealControlIsNotTheSaveControl is §4.10.3's "Reveal is not a save",
// asserted on the markup rather than in a comment.
//
// Four directions, and each is a different way a disclosure becomes a save:
//
//  1. **No element carries both hooks.** The editor's save is `data-editor-save`
//     and a reveal is `data-secret-reveal`; one element answering to both is a
//     control whose behaviour is whichever script bound last.
//  2. **The save control carries no `data-secret-*` at all**, so a script cannot
//     read a page's endpoint off the save button and fire a reveal from it.
//  3. **Every reveal control is `type="button"` and none is inside a form.** The
//     request is a `PUT` carrying `If-Match`, and a form can express neither.
//  4. **Every reveal control carries its own endpoint and its own validator**, so
//     two controls on one page cannot share one — which is how a second reveal
//     becomes a silent double disclosure after the first moved the bytes.
func TestTheRevealControlIsNotTheSaveControl(t *testing.T) {
	t.Parallel()

	document := editorDocument(t)

	controls := 0

	walk(t, document, func(node *html.Node) {
		isSave := hasAttribute(node, "data-editor-save")
		isReveal := hasAttribute(node, secret.RevealHook) ||
			hasAttribute(node, secret.UnrevealHook) ||
			hasAttribute(node, secret.RevealAllHook)

		if isSave && isReveal {
			failNode(t, node,
				"carries both the save hook and a reveal hook; §4.10.3: reveal is "+
					"not a save, and one element answering to both is a control "+
					"whose behaviour is whichever script bound last")
		}

		if isSave && len(anyAttributeNamed(node, "data-secret")) != 0 {
			failNode(t, node,
				"the save control carries a data-secret-* attribute; the save path "+
					"has nothing to do with a disclosure, and a script reading "+
					"one off the other is how a save reveals")
		}

		if !isReveal {
			return
		}

		controls++

		if attributeOr(node, "type") != "button" {
			failNode(t, node,
				"a reveal control that is not type=\"button\"; the request is a PUT "+
					"carrying If-Match, and only a button cannot submit")
		}

		if attributeOr(node, "data-secret-href") == "" ||
			attributeOr(node, "data-secret-if-match") == "" {
			failNode(t, node,
				"a reveal control missing an endpoint or a validator; S-6.3 makes "+
					"the precondition load-bearing and a reveal without one is "+
					"a 428 a client would mistake for a success")
		}

		for ancestor := node.Parent; ancestor != nil; ancestor = ancestor.Parent {
			if ancestor.Data == "form" {
				failNode(t, node,
					"a reveal control inside a <form>; a form can set neither the "+
						"PUT nor the If-Match a reveal needs, so a disclosure "+
						"submitted by one is an unconditional write")
			}
		}
	})

	if controls != 4 {
		t.Errorf("the editor document rendered %d reveal controls, want 4 (two "+
			"hidden callouts, one unreveal for the revealed one, and one "+
			"reveal-all); the fixture exercises both branches of the per-callout "+
			"control and a count that changed means one of them stopped "+
			"rendering", controls)
	}

	walk(t, document, func(node *html.Node) {
		if node.Data == "form" {
			failNode(t, node,
				"the editor renders a <form>; §6.2's write path is a PUT with "+
					"If-Match and an HTML form can express neither")
		}
	})
}

// TestTheRevealRequestCarriesOneMarkerAndNoPageContent is S-6.3, on the request
// rather than on the route.
//
// **The body is asserted literally**, not for "does not contain the buffer": a
// reveal request is `{anchor, revealed}` and nothing else, so a body that grew a
// `content` field would be a save wearing a reveal's name and the audit names the
// field.
func TestTheRevealRequestCarriesOneMarkerAndNoPageContent(t *testing.T) {
	t.Parallel()

	request := secret.RevealRequest{
		Method:   "PUT",
		URL:      secret.Endpoint("greyhaven", "Lore/Signal_Fire"),
		IfMatch:  `"sp1:0f3a"`,
		Anchor:   "signal-fire",
		Ordinal:  0,
		Revealed: true,
	}

	body := request.Body()

	want := `{"anchor":"signal-fire","revealed":true}`
	if body != want {
		t.Errorf("the reveal body is %s, want %s.\n"+
			"      A reveal is one byte of one marker. A body that grew a "+
			"content, buffer or validator field would be a save wearing a "+
			"reveal's name, and S-6.2's precondition is the header's job",
			body, want)
	}

	for _, forbidden := range []string{"content", "buffer", "hash", "ifMatch"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the reveal body carries %q; see above", forbidden)
		}
	}

	if request.Method != http.MethodPut {
		t.Errorf("the reveal request's method is %q; the endpoint mounts one "+
			"pattern, PUT /c/{slug}/secrets/{path...}", request.Method)
	}

	if !strings.Contains(request.URL, "/secrets/") || strings.Contains(request.URL, "/edit/") {
		t.Errorf("the reveal request's URL is %q; it must name the secrets "+
			"endpoint. A reveal routed through the editor's save path is a "+
			"disclosure going through the ordinary write, which §4.10.3 forbids",
			request.URL)
	}
}

// TestTheZeroValueOfEveryEnumIsTheSafeOne is a property of this package's types
// rather than of a rendering.
//
// §5.6.2 requires every failure path in this subsystem to resolve toward hiding. A
// view model a route builds is one of those paths, and the way a forgotten field
// resolves is the zero value — so the zero value has to be *hidden*, not
// *revealed*, not "the published page's default".
func TestTheZeroValueOfEveryEnumIsTheSafeOne(t *testing.T) {
	t.Parallel()

	if got := secret.State(0); got != secret.StateHidden {
		t.Errorf("the zero State is %q; §5.6.2 requires every failure path to "+
			"resolve toward hiding, and a view model whose zero value publishes "+
			"is a disclosure with no bug in it", got)
	}

	if got := secret.State(0).String(); got != "hidden" {
		t.Errorf("the zero State spells itself %q, want \"hidden\"", got)
	}

	if got := secret.State(7).String(); got != "unknown" {
		t.Errorf("State(7) spells itself %q, want \"unknown\"; a value outside the "+
			"closed set is a bug, and the honest thing to say about one is that "+
			"it is not a state this package has", got)
	}

	if got := secret.State(7).String(); got == "7" {
		t.Error("State(7) renders as a number, which a reader would see as a " +
			"state the product has")
	}

	if got := secret.Marker(0); got != secret.MarkerOff {
		t.Errorf("the zero Marker is %v; D15's published-page default is off and "+
			"a zero value that showed the marker would put a GM's bookkeeping "+
			"into a player's fiction", got)
	}

	if (secret.StubView{}).Enabled {
		t.Error("the zero StubView enables the stub; §5.6.1's default is " +
			"omission and a stub discloses the existence of a secret")
	}
}

// TestEveryCalloutNamesItselfForTheAccessibleName is the control-naming rule.
//
// Three "Reveal" buttons on one page are three buttons a screen-reader user cannot
// act on, and §4.8's `acceptLabel` sets out the same reasoning for the diff's
// eight. A callout with no title falls back to its ordinal, which is safe to speak
// for the one reader entitled to this surface.
func TestEveryCalloutNamesItselfForTheAccessibleName(t *testing.T) {
	t.Parallel()

	if name := (secret.CalloutView{Anchor: "a", Ordinal: 0, Title: secretTitleBoundary}).Name(); name != secretTitleBoundary {
		t.Errorf("a titled callout names itself %q", name)
	}

	if name := (secret.CalloutView{Anchor: "b", Ordinal: 1}).Name(); name != "secret 2" {
		t.Errorf("an untitled callout at ordinal 1 names itself %q, want "+
			"\"secret 2\"; a position is the only name it has", name)
	}

	if name := (secret.CalloutView{Anchor: "c", Title: "   "}).Name(); name != "secret 1" {
		t.Errorf("a whitespace-only title names itself %q; whitespace is not a name", name)
	}
}

// TestTheControlNameCarriesThePage is the third clause of the same rule.
//
// A GM with three editor tabs open is looking at three "Reveal" buttons, and
// "Reveal: Aldric replaced it" three times over says nothing about which page any of
// them acts on.
func TestTheControlNameCarriesThePage(t *testing.T) {
	t.Parallel()

	document := editorDocument(t)

	labels := map[string]bool{}

	walk(t, document, func(node *html.Node) {
		if !hasAttribute(node, secret.RevealHook) {
			return
		}

		label := attributeOr(node, "aria-label")

		if !strings.Contains(label, "Signal Fire") {
			failNode(t, node,
				"its accessible name is %q and does not name the page; §4.8's "+
					"acceptLabel reasoning applies — a GM with several editor "+
					"tabs open cannot tell which page a control acts on", label)
		}

		labels[label] = true
	})

	if len(labels) == 0 {
		t.Fatal("no reveal control carried an accessible name at all")
	}

	if len(labels) < 2 {
		t.Errorf("every reveal control has the same accessible name (%v); a list "+
			"of controls a reader cannot tell apart is a list they cannot act "+
			"on", labels)
	}
}

// --- THE ALERTS ----------------------------------------------------------------

// TestTheVisibleLabelIsInsideTheAccessibleName is §10.2's label-in-name rule, and it
// exists because the two were once different strings.
//
// The unreveal button's `aria-label` is built from `hideLabel` ("Hide again"), and its
// visible text was `hideSentence` ("Hidden again. Nobody new can read it.") — the
// sentence belongs to the *outcome* alert, one click later. Nothing in this package
// failed, because the button had a name, it was distinct from its neighbours, and
// every other assertion held. A sighted GM read "Hidden again. Nobody new can read
// it." on a control and a screen-reader user heard "Hide again: The harbourmaster on
// Signal Fire", which is §10.2's exact failure: the accessible name does not contain
// the visible label.
//
// **Every control on the surface, both branches and the page-level one.** The rule is
// about the whole set and not about the button that happened to be wrong.
func TestTheVisibleLabelIsInsideTheAccessibleName(t *testing.T) {
	t.Parallel()

	document := editorDocument(t)

	controls := 0

	walk(t, document, func(node *html.Node) {
		if !hasAttribute(node, secret.RevealHook) &&
			!hasAttribute(node, secret.UnrevealHook) &&
			!hasAttribute(node, secret.RevealAllHook) {
			return
		}

		controls++

		name := attributeOr(node, "aria-label")
		visible := strings.TrimSpace(nodeText(node))

		if visible == "" {
			failNode(t, node,
				"has no visible text; §10.2's label-in-name rule needs a label to "+
					"be inside the name, and a control whose only text is in an "+
					"aria-label is invisible to a sighted GM")

			return
		}

		if !strings.Contains(name, visible) {
			failNode(t, node,
				"reads %q and is named %q. §10.2's label-in-name rule: the "+
					"accessible name must contain the visible label, because a "+
					"voice-control user saying what they can see must reach this "+
					"control. Two different strings means a sighted GM and a "+
					"screen-reader user are looking at and hearing two "+
					"different controls", visible, name)
		}
	})

	if controls == 0 {
		t.Fatal("no reveal, unreveal or reveal-all control was found on the " +
			"editor surface, so this rule had nothing to read and nothing to fail")
	}
}

// TestTheAlertsAreAnnouncedAndAssertive is §4.10.3 and §7.5's channel table.
//
// **Two alerts, both the GM's, both assertive**, and each renders with
// `role="alert"` *and* `aria-live="assertive"` — the second because the first is
// implicit and §10.8's automated check is the attribute. A region that is
// `role="alert"` and silent by omission is a region a reader of the markup cannot
// tell apart from one that was never announced.
func TestTheAlertsAreAnnouncedAndAssertive(t *testing.T) {
	t.Parallel()

	for _, row := range []struct {
		name      string
		component templ.Component
	}{
		{
			name: "reveal outcome",
			component: secret.RevealOutcome(
				secret.RevealOutcomeView{Anchor: "signal-fire", Revealed: true},
			),
		},
		{
			name: "reconcile capped",
			component: secret.CappedAlert(
				secret.CappedAlertView{Detail: "A sync client changed this page back."},
			),
		},
		{
			// The region itself, and this row was missing: the two fragments
			// above are what a patch inserts, and each carries its own
			// `role="alert"`, so they announce whatever the region around them
			// says. A patch that inserts anything else — and §7.5's rules
			// describe exactly such patches — is governed by the region, so the
			// region's own politeness is load-bearing and was unasserted.
			name:      "the outcome region",
			component: secret.OutcomeRegion(),
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			document := render(t, row.component)

			alerts := 0

			walk(t, document, func(node *html.Node) {
				if attributeOr(node, "role") != "alert" {
					return
				}

				alerts++

				politeness, _ := attribute(node, "aria-live")
				if politeness != "assertive" {
					failNode(t, node,
						"carries role=alert and aria-live=%q; §4.10.3 makes the "+
							"reveal outcome and the reconcile-capped notice "+
							"assertive, and a GM who does not hear either is "+
							"wrong about what the table is showing", politeness)
				}
			})

			if alerts == 0 {
				t.Errorf("the %s rendered no role=alert element at all", row.name)
			}
		})
	}
}

// TestTheCappedHeadingIsTheOneLiveAlreadyUses holds one sentence to two packages.
//
// `live.NoticeReconcileCapped` shipped in phase 9 and carries this heading; §4.10.3
// puts the same condition on the status page. Two spellings of one condition is a
// second answer to the same question.
func TestTheCappedHeadingIsTheOneLiveAlreadyUses(t *testing.T) {
	t.Parallel()

	fromLive := live.NoticeReconcileCapped.Heading()
	if strings.TrimSpace(fromLive) == "" {
		t.Fatal("live.NoticeReconcileCapped has no heading; the agreement this " +
			"test holds cannot be checked against nothing")
	}

	document := render(t, secret.CappedAlert(secret.CappedAlertView{}))

	if !strings.Contains(nodeText(document), fromLive) {
		t.Errorf("the reconcile-capped alert says %q and live's notice says %q; "+
			"one condition with two sentences is a second answer to the same "+
			"question", nodeText(document), fromLive)
	}
}

// TestTheOutcomeRegionIsEmptyAtLoad is §7.5's replay rule, and it is the reason the
// region's own audit cannot be "it contains an alert".
//
// **Present and empty**, and both directions: a region absent cannot be patched
// into, and a region carrying an outcome at load announces a fact the reader did not
// just cause.
func TestTheOutcomeRegionIsEmptyAtLoad(t *testing.T) {
	t.Parallel()

	document := render(t, secret.OutcomeRegion())

	region := findByTestID(t, document, secret.OutcomeRegionTestID)

	if region == nil {
		t.Fatal("the outcome region is absent; a live region that is absent " +
			"cannot be patched into, which is a silent-pass failure mode")
	}

	if text := strings.TrimSpace(nodeText(region)); text != "" {
		t.Errorf("the outcome region carries %q at load; §7.5's replay rule is "+
			"that a live region announces on a transition, and a GM who opens the "+
			"editor has not transitioned", text)
	}

	if containsFocusStop(region) {
		t.Error("the outcome region holds a focus stop; §7.5 prohibits a patch " +
			"touching the focused element, and the way to guarantee that is for " +
			"the target to be incapable of holding focus")
	}

	// The heading is a *sibling*, so replacing the region's contents does not
	// replace the region's name.
	if attributeOr(region, "aria-labelledby") == "" {
		t.Error("the outcome region carries no aria-labelledby; an assertive region " +
			"with no name announces an announcement")
	}
}

// findByTestID returns the element carrying this hook, or nil.
func findByTestID(t *testing.T, document *html.Node, hook string) *html.Node {
	t.Helper()

	var found *html.Node

	walk(t, document, func(node *html.Node) {
		if found == nil && attributeOr(node, "data-testid") == hook {
			found = node
		}
	})

	return found
}

// TestNoPatchTargetCanHoldFocus is `live`'s property on this surface's outlets.
//
// The hooks are declared in `secret.go` rather than read out of a document, because
// a route patches into them by name and a name nothing renders is a name whose
// focusability nothing has checked.
func TestNoPatchTargetCanHoldFocus(t *testing.T) {
	t.Parallel()

	outlets := secret.Outlets()

	if len(outlets) == 0 {
		t.Fatal("this package declares no patch targets, so Decide has nothing " +
			"to reason about and the assertion below is vacuous")
	}

	// The declared targets, each mounted as the bare element a route would patch
	// into — **plus** this package's own outcome region, which is the one target
	// a real document renders and the one a focus stop could creep into.
	mounted := make([]templ.Component, 0, len(outlets)+1)
	mounted = append(mounted, secret.OutcomeRegion())

	for _, outlet := range outlets {
		mounted = append(mounted, element(outlet.Hook))
	}

	document := render(t, fragment(mounted...))

	for _, outlet := range outlets {
		found := false

		walk(t, document, func(node *html.Node) {
			if attributeOr(node, secret.ChromeAttribute) != outlet.Hook {
				return
			}

			found = true

			if containsFocusStop(node) {
				failNode(t, node,
					"is the patch target %q and holds a focus stop; §7.5's rule "+
						"is that a patch must never touch the focused element, "+
						"and a target that can hold focus is one a reader can be "+
						"inside", outlet.Hook)
			}
		})

		if !found {
			t.Errorf("the outlet %q is declared but no rendered element carries "+
				"it as a data-chrome value; a patch into nothing is silent",
				outlet.Hook)
		}
	}
}

// element is a bare element carrying one attribute, for the fixture above.
//
// `templ.ComponentFunc` writing literal markup rather than a component, because
// the point is to render **an element carrying this hook and nothing else** — a
// component that put something inside it would be testing the fixture.
func element(hook string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, out io.Writer) error {
		if _, err := io.WriteString(out, `<div data-chrome="`+hook+`"></div>`); err != nil {
			return fmt.Errorf("writing a bare hook element: %w", err)
		}

		return nil
	})
}

// TestTheMarkerIsATextLabelAndNotOnlyAColour is §4.10.4's own requirement.
//
// "The marker is a text label, not an icon, because it is the one place the UI
// must be understood without sight *or* without colour perception." The word is
// asserted **in the rendered text**, not in a class, and the test reads the parsed
// DOM — a marker drawn with a `::before` would satisfy every other test here.
func TestTheMarkerIsATextLabelAndNotOnlyAColour(t *testing.T) {
	t.Parallel()

	document := render(t, secret.CalloutChrome(secret.CalloutChromeView{
		Title:   "Aldric replaced it",
		Ordinal: 0,
		State:   secret.StateRevealed,
		Marker:  secret.MarkerShown,
	}))

	if !strings.Contains(nodeText(document), "Revealed") {
		t.Errorf("the rendered chrome reads %q and carries no text marker; "+
			"§4.10.4 requires a text label because a colour is unreadable "+
			"without colour perception and an icon is unreadable without sight",
			nodeText(document))
	}
}

// TestTheMarkerIsOffWhenTheCallerSaysOff is D15's published-page default, exercised
// from the component rather than from a route.
func TestTheMarkerIsOffWhenTheCallerSaysOff(t *testing.T) {
	t.Parallel()

	document := render(t, secret.CalloutChrome(secret.CalloutChromeView{
		Title:   "Aldric replaced it",
		Ordinal: 0,
		State:   secret.StateRevealed,
		Marker:  secret.MarkerOff,
	}))

	if strings.Contains(nodeText(document), "Revealed") {
		t.Errorf("the chrome reads %q with MarkerOff; §4.10.4 and D15 leave the "+
			"marker off by default and the zero value is that", nodeText(document))
	}
}

// TestAMarkerOnAHiddenCalloutIsNotRendered is a state/marker contradiction the
// caller could express and the component must refuse.
//
// A "Revealed" marker on a *hidden* callout is a lie about who may see it, and it is
// the failure `ext/secret.go` calls a "styling lie about who may see it" when a
// class and a state disagree.
func TestAMarkerOnAHiddenCalloutIsNotRendered(t *testing.T) {
	t.Parallel()

	document := render(t, secret.CalloutChrome(secret.CalloutChromeView{
		Title:   "Aldric replaced it",
		Ordinal: 0,
		State:   secret.StateHidden,
		Marker:  secret.MarkerShown,
	}))

	if strings.Contains(nodeText(document), "Revealed") {
		t.Errorf("a hidden callout rendered the text %q; §4.10.4's marker is "+
			"about who may see the body, and a marker on a hidden callout says "+
			"the opposite", nodeText(document))
	}
}

// TestTheStubRendersNothingUntilTheCampaignOptsIn is §5.6.1's default.
//
// Both branches in one test, because a stub that rendered nothing at all would pass
// the default assertion while being a feature that does not work.
func TestTheStubRendersNothingUntilTheCampaignOptsIn(t *testing.T) {
	t.Parallel()

	off := render(t, secret.Stub(secret.StubView{}))

	if text := strings.TrimSpace(nodeText(off)); text != "" {
		t.Errorf("the stub rendered the text %q with the setting off; §5.6.1's "+
			"default is omission and a stub discloses the existence and position "+
			"of a secret", text)
	}

	on := render(t, secret.Stub(secret.StubView{Enabled: true}))

	if !strings.Contains(nodeText(on), stubLabel) {
		t.Errorf("the enabled stub reads %q; §4.10.2 names the label verbatim",
			nodeText(on))
	}

	tag := findByTestID(t, on, secret.StubTestID)
	if tag == nil || tag.Data != "blockquote" {
		t.Errorf("the stub is %v; §4.10.2 specifies an empty <blockquote>", tag)
	}
}

// TestTheStubIsNotALiveRegion is the stub half of the decision.
//
// **The stub is content, not an event.** §4.10.2 requires "screen-reader users get
// the same thing sighted users get", and an announced stub would give them
// something sighted users do not get: an interruption, on every load, for a
// condition that has not changed since the page was written.
func TestTheStubIsNotALiveRegion(t *testing.T) {
	t.Parallel()

	document := render(t, secret.Stub(secret.StubView{Enabled: true}))

	walk(t, document, func(node *html.Node) {
		role := attributeOr(node, "role")
		if role == "alert" || role == "status" {
			failNode(t, node,
				"the stub carries role=%q; §4.10.2 asks for the same thing sighted "+
					"and screen-reader users get, and an announced stub is a "+
					"thing only one of them gets", role)
		}

		if politeness, ok := attribute(node, "aria-live"); ok && politeness != "off" {
			failNode(t, node, "the stub carries aria-live=%q", politeness)
		}
	})
}

// TestTheStubCarriesNoAnchorAndNoOrdinal is the stub's *content* rule, and it is
// the one thing a stub could leak that a plain label cannot.
//
// A stub naming its callout — by block id, ordinal or title — would disclose *which*
// secret it stands for, which is §5.6.1's existence **and position**.
func TestTheStubCarriesNoAnchorAndNoOrdinal(t *testing.T) {
	t.Parallel()

	document := render(t, secret.Stub(secret.StubView{Enabled: true}))

	walk(t, document, func(node *html.Node) {
		for _, name := range anyAttributeNamed(node, "data-secret") {
			failNode(t, node,
				"the stub carries %s; §5.6.1 withholds the existence *and the "+
					"position* of a secret, and an attribute naming which callout "+
					"this is would disclose the second half", name)
		}
	})
}

// --- THE PATCH GATE ------------------------------------------------------------

// TestDecideRefusesTheFourWaysAndAllowsTheOne is the server-side half of the
// decision.
//
// `Decide` is what keeps this surface from drifting into a channel that announces
// secrets: the marker outlet may not take an announcement, and no outlet may take a
// focus stop.
func TestDecideRefusesTheFourWaysAndAllowsTheOne(t *testing.T) {
	t.Parallel()

	outcome, found := secret.OutletFor(secret.OutcomeHook)
	if !found {
		t.Fatal("the outcome outlet is not declared; Decide has nothing to " +
			"reason about and this test is vacuous")
	}

	marker, found := secret.OutletFor(secret.MarkerHook)
	if !found {
		t.Fatal("the marker outlet is not declared; same reason")
	}

	for _, row := range []struct {
		name    string
		frag    secret.Fragment
		want    bool
		subject secret.Refusal
	}{
		{
			name: "an outcome fragment at the outcome outlet",
			frag: secret.Fragment{
				Outlet: outcome,
				Markup: `<p role="alert" aria-live="assertive">Revealed.</p>`,
			},
			want: true,
		},
		{
			name: "an announcement aimed at the marker outlet",
			frag: secret.Fragment{
				Outlet: marker,
				Markup: `<p role="alert" aria-live="assertive">Revealed.</p>`,
			},
			want:    false,
			subject: secret.RefusedAnnouncedIntoSilent,
		},
		{
			name: "a fragment holding a button",
			frag: secret.Fragment{
				Outlet: outcome,
				Markup: `<p role="alert">Revealed.<button type="button">Undo</button></p>`,
			},
			want:    false,
			subject: secret.RefusedHoldsFocusStop,
		},
		{
			name: "a fragment aimed at an outlet nobody declared",
			frag: secret.Fragment{
				Outlet: secret.Outlet{Hook: "nowhere", Selector: `[data-chrome="nowhere"]`},
				Markup: `<p>Revealed.</p>`,
			},
			want:    false,
			subject: secret.RefusedUnknownOutlet,
		},
		{
			name: "a fragment whose selector and hook disagree",
			frag: secret.Fragment{
				Outlet: secret.Outlet{Hook: outcome.Hook, Selector: `[data-chrome="elsewhere"]`},
				Markup: `<p>Revealed.</p>`,
			},
			want:    false,
			subject: secret.RefusedUnknownOutlet,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			decision := secret.Decide(row.frag)

			if decision.Write != row.want {
				t.Fatalf("Decide wrote = %v, want %v (reason %q)",
					decision.Write, row.want, decision.Reason)
			}

			if row.want {
				return
			}

			if decision.Reason != row.subject {
				t.Errorf("Decide refused with %q, want %q; a refusal for an "+
					"unrelated reason looks exactly like a working one",
					decision.Reason, row.subject)
			}
		})
	}
}

// TestTheRenderedAlertsAreFragmentsThisServerWouldWrite closes the loop between
// `Decide` and the markup.
//
// **A refusal set never exercised over the real markup is a rule about nothing.**
func TestTheRenderedAlertsAreFragmentsThisServerWouldWrite(t *testing.T) {
	t.Parallel()

	outcome, _ := secret.OutletFor(secret.OutcomeHook)
	marker, _ := secret.OutletFor(secret.MarkerHook)

	for _, row := range []struct {
		name   string
		outlet secret.Outlet
		make   func() templ.Component
	}{
		{
			name:   "reveal outcome",
			outlet: outcome,
			make: func() templ.Component {
				return secret.RevealOutcome(secret.RevealOutcomeView{
					Anchor: "signal-fire", Revealed: true,
				})
			},
		},
		{
			name:   "reconcile capped",
			outlet: outcome,
			make: func() templ.Component {
				return secret.CappedAlert(secret.CappedAlertView{Detail: "capped"})
			},
		},
		{
			// The chrome is the marker outlet's fragment, and it is **not** an
			// announcement — which is what `RefusedAnnouncedIntoSilent` is for.
			name:   "callout chrome",
			outlet: marker,
			make: func() templ.Component {
				return secret.CalloutChrome(secret.CalloutChromeView{
					Title: "Aldric replaced it", Ordinal: 0,
					State: secret.StateRevealed, Marker: secret.MarkerShown,
				})
			},
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()

			decision := secret.Decide(secret.Fragment{
				Outlet: row.outlet,
				Markup: markupOf(t, row.make()),
			})

			if !decision.Write {
				t.Errorf("Decide refused this package's own fragment (%q); a "+
					"disclosure surface that cannot ship its own announcement "+
					"is a surface with a region and nothing to put in it",
					decision.Reason)
			}
		})
	}
}

// TestTheEndpointIsTheRouteTheRevealHandlerServes holds the URL against the
// handler's own mounted pattern.
//
// `internal/httpapi/secrets` spells its pattern in `Mount` and there is nothing to
// import from it. So the assertion is that the two agree in shape:
// `/c/{slug}/secrets/{path...}`.
func TestTheEndpointIsTheRouteTheRevealHandlerServes(t *testing.T) {
	t.Parallel()

	got := secret.Endpoint("greyhaven", "Lore/Signal Fire")

	want := "/c/greyhaven/secrets/Lore/Signal Fire"
	if got != want {
		t.Errorf("Endpoint produced %q, want %q.\n"+
			"      The handler mounts PUT /c/{slug}/secrets/{path...}; a URL "+
			"naming a different path is a disclosure aimed at the wrong route, "+
			"and it would 404 rather than fail loudly",
			got, want)
	}

	if strings.ContainsAny(got, "?#") {
		t.Errorf("Endpoint produced %q; §5.6.3's argument is that the URL is the "+
			"precondition's transport and carries no per-secret selector", got)
	}
}

// TestTheChromeAttributeIsTheOneLiveAlreadyUses holds one attribute name to two
// packages.
//
// Both are `data-chrome`, spelled twice so neither package imports the other. A
// rename reaching one of them would make a patch target one hook in a document
// carrying the other, which fails silently: the client selects nothing and reports
// nothing.
func TestTheChromeAttributeIsTheOneLiveAlreadyUses(t *testing.T) {
	t.Parallel()

	if secret.ChromeAttribute != live.ChromeAttribute {
		t.Errorf("the patch attribute is %q here and %q in components/live; one "+
			"attribute, one vocabulary", secret.ChromeAttribute, live.ChromeAttribute)
	}

	if secret.SelectorFor(secret.MarkerHook) != live.SelectorFor(secret.MarkerHook) {
		t.Error("the selector derivation disagrees with live's for the same hook; " +
			"a hook and the selector naming it must be one fact")
	}
}

// TestTheMarkerOutletIsSilent is `Decide`'s rule stated as a property of the
// package's own declarations rather than of a fixture.
//
// §4.10.4: the marker is a label a GM reads. An outlet that could be announced into
// is an outlet whose channel was never decided.
func TestTheMarkerOutletIsSilent(t *testing.T) {
	t.Parallel()

	decision := secret.Decide(secret.Fragment{
		Outlet: secret.Outlet{
			Hook:     secret.MarkerHook,
			Selector: secret.SelectorFor(secret.MarkerHook),
		},
		Markup: `<span role="alert" aria-live="assertive">Revealed</span>`,
	})

	if decision.Write || decision.Reason != secret.RefusedAnnouncedIntoSilent {
		t.Errorf("an announcement into the marker outlet was %v (%q); §4.10.4's "+
			"marker is a label, and a region that speaks it on a re-render is "+
			"§7.5's replay failure",
			decision.Write, decision.Reason)
	}
}

// --- THE GATE'S OWN NAMES ------------------------------------------------------

// TestTheAuditNamesMatchTheGatePattern is the claim that this package's audits are
// part of `make a11y`.
//
// **It reads the Makefile's own `A11Y_TESTS`** rather than carrying a copy, so a
// rename in the pattern fails here rather than leaving a set of audits that silently
// stopped running — the exact failure AGENTS.md records for three phase-9 tests
// named outside the pattern and therefore never executed under the gate.
//
// It checks the *names*, not the package list: naming this package in
// `A11Y_COMPONENT_PKGS` is the integrator's change, and this test is green before
// and after it.
func TestTheAuditNamesMatchTheGatePattern(t *testing.T) {
	t.Parallel()

	pattern := gatePattern(t)
	names := auditsInThisPackage(t)

	matched := 0

	for _, name := range names {
		if matchedByAny(name, pattern) {
			matched++

			continue
		}

		t.Logf("%s matches no alternative in A11Y_TESTS, so `make a11y` will not "+
			"run it; it still runs under `make check`", name)
	}

	if matched == 0 {
		t.Fatalf("no test in this package matches any alternative in the "+
			"Makefile's A11Y_TESTS (%s), so `make a11y` runs none of this "+
			"package's audits while reporting green", pattern)
	}

	if matched < 4 {
		t.Errorf("only %d of %d tests in this package match A11Y_TESTS; this "+
			"package claims the §10.2, §10.6, §10.1 and vocabulary audits and "+
			"each needs a name the gate finds", matched, len(names))
	}
}

// gatePattern reads `A11Y_TESTS` out of the Makefile.
//
// Three levels up: `secret` → `components` → `web` → the repository root. A copy of
// the pattern here would be a second answer to "what does the gate run", which is
// the thing this test exists to prevent.
func gatePattern(t *testing.T) string {
	t.Helper()

	path := filepath.Join("..", "..", "..", "..", "Makefile")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	for line := range strings.SplitSeq(string(raw), "\n") {
		name, value, found := strings.Cut(line, "A11Y_TESTS")
		if !found || strings.HasPrefix(name, "#") {
			continue
		}

		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}

		if strings.Contains(value, "$(") {
			t.Fatalf("A11Y_TESTS is built from a variable (%s), so this test "+
				"cannot read the gate's real pattern and its claim is about "+
				"nothing", value)
		}

		return value
	}

	t.Fatal("the Makefile has no A11Y_TESTS line; the pattern this test reads " +
		"has moved and the gate's coverage claim cannot be checked")

	return ""
}

// auditsInThisPackage lists this package's test names, read off the source.
//
// Reading rather than hard-coding, so a new audit is counted and a renamed one is
// noticed.
func auditsInThisPackage(t *testing.T) []string {
	t.Helper()

	matches, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob the package's test files: %v", err)
	}

	var names []string

	for _, path := range matches {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		for line := range strings.SplitSeq(string(raw), "\n") {
			rest, found := strings.CutPrefix(strings.TrimSpace(line), "func Test")
			if !found {
				continue
			}

			if name, _, hasArgs := strings.Cut(rest, "("); hasArgs && name != "" {
				names = append(names, name)
			}
		}
	}

	return names
}

// matchedByAny reports whether a test name matches any alternative in the pattern.
func matchedByAny(name, pattern string) bool {
	for alternative := range strings.SplitSeq(pattern, "|") {
		if alternative == "" {
			continue
		}

		if strings.Contains(name, alternative) {
			return true
		}
	}

	return false
}

// TestTheGateNamesThisPackageIsTheIntegratorsToMake is the loud log, as a test.
//
// **It cannot fail**, and the reason is worth stating rather than leaving as an
// exercise in restraint: the Makefile is the integrator's file (AGENTS.md rule 3),
// so a test here that failed on it would be red on this branch over another
// package's pending change. What it can do is say, once, in `go test -v` output,
// that the coverage is one line away — which is the difference between a silent
// pass and a known one.
func TestTheGateNamesThisPackageIsTheIntegratorsToMake(t *testing.T) {
	t.Parallel()

	if makefileMentionsThisPackage() {
		return
	}

	t.Log("A11Y_COMPONENT_PKGS does not name ./internal/web/components/secret.\n" +
		"      The audits in this package run under `make check`, and they will " +
		"run under `make a11y` the moment the line lands:\n" +
		"          A11Y_COMPONENT_PKGS := $(wildcard ./internal/web/components/play \\\n" +
		"              ./internal/web/components/chat ./internal/web/components/live \\\n" +
		"              ./internal/web/components/secret)\n" +
		"      Until then `go test -run` matches nothing here and exits 0.")
}

// makefileMentionsThisPackage reports whether the gate names this package.
func makefileMentionsThisPackage() bool {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "Makefile"))
	if err != nil {
		return false
	}

	return strings.Contains(string(raw), "./internal/web/components/secret")
}
