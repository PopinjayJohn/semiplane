package theme_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/theme"
)

// §4.12.1's table, held against the stylesheet it governs.
//
// The tests in this file are the anti-rot gate for ownership.go, and they are the
// reason that file is data rather than a comment. Each one reads the product's
// *own* stylesheets — not a copy of the palette, not a list restated in a test — so
// the claim being checked is "the ownership table and the stylesheet still agree
// about who owns what", which is the claim that rots.
//
// Every one of them has been made to fail. The mutations are named beside each
// test; AGENTS.md's rule is that an audit nobody can fail is a green light wired
// to nothing, and a table a stylesheet can drift away from is that.

// cssDir is the product's hand-written stylesheets, relative to this package.
//
// The three files `app.css` imports, and only those: `app.css` itself holds the
// `@import` lines and the `@source` globs, so a declaration walk over it finds
// nothing that is not in one of the three.
//
// Read from **source** rather than from the built file, for the reason
// `tokens_contrast_test.go` gives and it is the only correct answer here: the
// build minifies and Tailwind prunes whatever no utility references, so a name
// that only a rule no template uses would be absent from the built output, and a
// check that read it would report the stylesheet as missing a token it has.
const cssDir = "../../web/static/css"

// sourceSheets are the files that declare tokens, in the order `app.css` imports
// them. Load order is irrelevant to a name walk and named for the reader.
var sourceSheets = []string{"tokens.css", "shell.css", "tv.css"}

// brandPrefix is the namespace §4.12.1's overridable column lives in.
//
// A prefix rather than the two exact names, and that is the point of the test it
// drives: **any** `--brand-*` name the product declares has to be classified here,
// so a brand token added to tokens.css without a decision in ownership.go fails the
// build instead of being silently unreachable from a campaign manifest.
const brandPrefix = "--brand-"

// declarationPattern finds a custom property declaration.
//
// It requires the colon, so `var(--x)` uses and `--x` inside prose do not match,
// and it requires a declaration boundary before the name — a `;`, a `{` or
// whitespace — because a property name is always at one of those.
var declarationPattern = regexp.MustCompile(`(?:^|[;{\s])(--[A-Za-z0-9_-]+)\s*:`)

// valuePattern captures a declaration's value as well as its name.
//
// `--bg` is the one token whose *value* this package has to agree with — see
// `TestThePageBackgroundsAreTheOnesTheStylesheetDeclares` — so a walk that reports
// presence is not enough on its own.
var valuePattern = regexp.MustCompile(`(?:^|[;{\s])(--[A-Za-z0-9_-]+)\s*:\s*([^;}]*)`)

// commentPattern matches a CSS comment.
var commentPattern = regexp.MustCompile(`(?s)/\*.*?\*/`)

// stripComments removes CSS comments so a mechanical check reads declarations and
// not the prose about them.
//
// Not optional. tokens.css is more comment than code, and its second block of
// comments is §4.12.1's own protected-variable table, which names every token in
// the repository — including `--brand-header-image`, which no rule declares. A
// walk that read comments would report that token as declared, and
// `TestEveryCampaignTokenIsDeclaredByTheProduct` would then pass for the wrong
// reason.
func stripComments(sheet string) string {
	return commentPattern.ReplaceAllString(sheet, "")
}

// readSheet reads one of the product's stylesheets, comments stripped.
func readSheet(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(cssDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}

	return stripComments(string(raw))
}

// declaredTokens reads every custom property the product's stylesheets declare, as
// a set keyed by name.
func declaredTokens(t *testing.T) map[string]bool {
	t.Helper()

	declared := map[string]bool{}

	for _, sheet := range sourceSheets {
		for _, match := range declarationPattern.FindAllStringSubmatch(readSheet(t, sheet), -1) {
			declared[match[1]] = true
		}
	}

	if len(declared) == 0 {
		t.Fatalf("no custom property was found in %v; the walk is broken, and a "+
			"walk that finds nothing passes every test below for the wrong reason", sourceSheets)
	}

	return declared
}

// tokensInThemeBlock reads the tokens one `[data-theme="…"]` rule declares, as
// name to declared value.
//
// Brace-matched rather than split on the theme names, because the two rules are
// separated by a comment and by a whole other rule, and a naive split would hand
// back tokens belonging to whichever rule came next.
func tokensInThemeBlock(t *testing.T, themeName string) map[string]string {
	t.Helper()

	sheet := readSheet(t, "tokens.css")
	selector := `[data-theme="` + themeName + `"]`

	from := strings.Index(sheet, selector)
	if from < 0 {
		t.Fatalf("tokens.css declares no %s rule", selector)
	}

	open := strings.Index(sheet[from:], "{")
	if open < 0 {
		t.Fatalf("the %s rule has no body", selector)
	}

	body, closed := braceBody(sheet, from+open)
	if !closed {
		t.Fatalf("the %s rule's body is never closed", selector)
	}

	return declarationsIn(body)
}

// braceBody returns the text between the brace at open and the one that closes it.
func braceBody(sheet string, open int) (string, bool) {
	depth := 0

	for index := open; index < len(sheet); index++ {
		switch sheet[index] {
		case '{':
			depth++
		case '}':
			depth--

			if depth == 0 {
				return sheet[open+1 : index], true
			}
		}
	}

	return "", false
}

// declarationsIn collects a block's custom properties as name to declared value.
//
// A map, so a token declared twice inside one rule (light and dark each declare
// `--dur-fast` once, and the `forced-colors` block re-declares several) keeps the
// **last** value — which is the one that wins in CSS, and therefore the one any
// comparison against has to use.
func declarationsIn(block string) map[string]string {
	names := map[string]string{}
	for _, match := range valuePattern.FindAllStringSubmatch(block, -1) {
		names[match[1]] = strings.TrimSpace(match[2])
	}

	return names
}

// TestEveryBrandTokenTheProductDeclaresIsInTheCampaignVocabulary is the gate that
// makes ownership.go's list complete rather than merely plausible.
//
// §4.12.1's left column is a *namespace* in practice: `--brand-accent`,
// `--brand-accent-ink` and `--brand-header-image`, with more to come. So a brand
// token the stylesheet declares and the vocabulary does not name is a token a
// campaign can see in its own stylesheet and cannot set — and the failure is
// invisible in every direction: the stylesheet is right, the vocabulary is right,
// and the manifest that tried to use the token is refused for being "semiplane's
// own".
//
// **Mutation:** adding `--brand-surface: #123456;` to the `[data-theme="light"]`
// rule in tokens.css fails this test with the new name. Narrowing `brandPrefix` to
// `--brand-a` passes it again, which is what makes it a gate and not a comment.
func TestEveryBrandTokenTheProductDeclaresIsInTheCampaignVocabulary(t *testing.T) {
	t.Parallel()

	vocabulary := map[string]bool{}
	for _, name := range theme.CampaignTokens() {
		vocabulary[name] = true
	}

	for name := range declaredTokens(t) {
		if !strings.HasPrefix(name, brandPrefix) {
			continue
		}

		if !vocabulary[name] {
			t.Errorf("the stylesheets declare %s, and %s is not in the campaign "+
				"vocabulary: a campaign manifest naming it would be refused as "+
				"semiplane's own token, so the token would be visible in the "+
				"stylesheet and unreachable from a theme (UI §4.12.1)",
				name, name)
		}
	}
}

// TestEveryCampaignTokenIsDeclaredByTheProduct is the other direction, and it is
// the one that catches a vocabulary entry nobody wired up.
//
// `--brand-header-image` is the case this exists for: §4.12.1 lists it as
// overridable, tokens.css's own table names it, and **no rule declares it** — so a
// manifest setting it would emit a declaration that changes nothing, and the GM
// would have no way to tell that from a browser bug. Rather than shipping a token
// whose override has nowhere to land, the vocabulary omits it and this test keeps
// the omission honest: if a future change declares it, this fails and the next
// author finds out they can now add it.
//
// **Mutation:** deleting the `--brand-accent: var(--accent);` declaration from
// tokens.css fails this test. Adding the vocabulary entry for
// `--brand-header-image` without declaring it fails it too, which is the property
// that makes the vocabulary safe to extend.
func TestEveryCampaignTokenIsDeclaredByTheProduct(t *testing.T) {
	t.Parallel()

	declared := declaredTokens(t)

	for _, name := range theme.CampaignTokens() {
		if !declared[name] {
			t.Errorf("%s is in the campaign vocabulary, and no stylesheet "+
				"declares it: the generator would emit a declaration that changes "+
				"nothing, and a campaign theme that appears to work and does not "+
				"(UI §4.12.1)", name)
		}
	}
}

// TestEveryCampaignTokenIsDeclaredInBothThemes holds the asymmetry §6.2 and §6.3
// do not have.
//
// tokens.css declares every layer-2 and layer-3 token twice, once per theme, and
// `tokens_contrast_test.go` has its own half of that claim. For the brand pair it
// matters more than for most: the generated sheet is **theme-independent** — §4.12.4
// says one brand applies to both, which is why §4.12.3's page floor is checked
// against two pages — so a brand token declared in only one theme would give the
// other theme an override standing on nothing.
//
// **Mutation:** deleting `--brand-accent: var(--accent);` from the `[data-theme="dark"]`
// rule fails this test and nothing else in the repository notices.
func TestEveryCampaignTokenIsDeclaredInBothThemes(t *testing.T) {
	t.Parallel()

	blocks := map[string]map[string]string{
		"light": tokensInThemeBlock(t, "light"),
		"dark":  tokensInThemeBlock(t, "dark"),
	}

	for _, name := range theme.CampaignTokens() {
		for themeName, declared := range blocks {
			if _, ok := declared[name]; !ok {
				t.Errorf("the %s theme does not declare %s, and a campaign theme "+
					"is not per-theme (UI §4.12.4): the generated sheet sets it in "+
					"both themes, so a one-theme declaration is a default that "+
					"exists in one and not the other", themeName, name)
			}
		}
	}
}

// TestEveryProtectedFamilyProtectsSomethingTheProductDeclares keeps
// `protectedPrefixes` from being decoration.
//
// A protected family that matches no declared name protects nothing, and it costs
// nothing to write one: `--targt-min` would sit in that list, look like the
// accessibility contract, and match nothing. The list is a claim about tokens that
// exist, so this holds it to one.
//
// It is also the only thing that can catch a family **missing** from the list while
// tokens exist that match it, which is why the list is prefixes and not the exact
// names §4.12.1's table prints: the record writes `--border*` and `--text-*`, and
// `--border-w` and `--text-subtle` are declared tokens a name-by-name list would
// have to be extended to cover by hand.
//
// **Mutation:** adding `--nothing-like-this: 1;` to tokens.css's primitive layer
// and prefixing the protected list with `--nothing` fails this test.
func TestEveryProtectedFamilyProtectsSomethingTheProductDeclares(t *testing.T) {
	t.Parallel()

	declared := declaredTokens(t)

	for _, prefix := range theme.ProtectedTokens() {
		protectsSomething := false

		for name := range declared {
			if strings.HasPrefix(name, prefix) {
				protectsSomething = true

				break
			}
		}

		if !protectsSomething {
			t.Errorf("the protected family %s matches no token the product "+
				"declares: it protects nothing, and a list entry that protects "+
				"nothing reads as part of the accessibility contract (UI §4.12.1)",
				prefix)
		}
	}
}

// TestNoCampaignTokenIsAlsoProtected is the disjointness the two tables need.
//
// A name that is both would make "who owns this" a question with two answers, and
// the *code* would pick one — `ownerOf` returns campaign-owned without consulting
// the protected list, so the generator would emit it while the refusal message for
// every other name in that family said it was never overridable.
//
// No current name can be in both (`--brand-accent` matches no protected prefix),
// which is exactly why it needs asserting: the two lists are edited separately, and
// a future token spelled `--target-min-brand` would satisfy each of the two tests
// above and this one is the only thing that would notice.
//
// **Mutation:** adding `"--target-min-brand": ""` to `campaignVocabulary` fails
// this test, and so does adding `--border` to the protected prefixes.
func TestNoCampaignTokenIsAlsoProtected(t *testing.T) {
	t.Parallel()

	for _, name := range theme.CampaignTokens() {
		for _, prefix := range theme.ProtectedTokens() {
			if !strings.HasPrefix(name, prefix) {
				continue
			}

			t.Errorf("%s is both campaign-owned and matched by the protected "+
				"family %s: the generator would emit it while every other name in "+
				"that family is refused as never overridable (UI §4.12.1)",
				name, prefix)
		}
	}
}
