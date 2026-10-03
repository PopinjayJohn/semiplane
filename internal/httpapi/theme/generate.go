package theme

// The generated stylesheet.
//
// # This function is the enforcement
//
// §4.12.2's claim is that "the protected variables above are never emitted into
// that stylesheet", and tokens.css hands the proof to this file: "the generator
// never emits a protected name — so a campaign cannot set one even by writing a
// raw .css into its content root". The property is structural rather than
// advisory, and it is structural because of what this file *cannot* do:
//
//   - it emits colour names by iterating `campaignVocabulary`, so the set of names
//     it can possibly write is the closed allowlist and nothing else;
//   - the two font names it emits are the two constants `fontSlots` maps to, and
//     neither is reachable from the manifest — the manifest names a *slot*, and the
//     slot's token spelling is this file's;
//   - it emits values that have been through `parseColour`, `quoted` or
//     `assetURL`, so every value is either seven characters of `#` and lowercase
//     hex, a quoted ASCII family, or a percent-encoded `/c/{slug}/assets/` URL;
//   - it writes no selector from input, no `@import`, and no media query.
//
// Which means the sheet is not a template with holes in it. There is nowhere in it
// for a campaign's bytes to go, so there is no sanitising step that could be
// forgotten and no escaping question to get wrong. That is the whole design, and
// it is why `TestTheGeneratedStylesheetDeclaresNothingButTheBrandPair` can assert
// a shape rather than the absence of a pattern.
//
// # The two names that are not in the allowlist, and why that is not a hole
//
// `--font-prose` and `--font-ui` are §4.12.1's left column — overridable — and the
// generated sheet writes them. They are the only two names this file writes that
// `campaignVocabulary` does not contain, and the property that makes that safe is
// **the fallback is not the manifest's to remove**: the emitted value is always
// `"<family>", <the product's system stack>`, where the second half is a constant
// in font.go rather than anything the manifest supplied. A GM who names a font that
// fails to load gets the product's design, which is §5.2's stated degradation and
// §4.12.1's parenthetical.
//
// **Every other protected name is unreachable here**, because the only loops are
// over `campaignVocabulary` and over `fontSlots`. There is no path by which a
// manifest reaches a declaration for `--focus-ring`, `--target-min` or
// `--font-mono`, and `TestTheGeneratedStylesheetCarriesNoProtectedVariable` reads
// the forbidden set out of the product's own stylesheets rather than out of this
// package, so it would catch a mistake here rather than agreeing with it.
//
// # Why no `@media`, ever
//
// §10.7's rule is that no rule selecting TV mode may sit inside a `@media`
// naming `min-width`, `orientation`, `pointer` or `hover`.
//
//nolint:misspell // `forced-colors` is the CSS media feature's own spelling, and
// the same is true of `prefers-color-scheme`; a media query written the UK way
// matches nothing.
// §6.7's rule is that `prefers-*` and `forced-colors` are required in *every*
// mode. A generated sheet with no media query at all cannot violate either: it has
// no mode to select and no preference to honour. The brand pair is theme-independent by §4.12.4's own
// argument — one brand applies to both themes — so there is nothing here that
// *would* want a query.
//
// # Specificity, and why the selector is `:root[data-theme]`
//
// tokens.css declares `--brand-accent: var(--accent)` inside `[data-theme="light"]`
// and `[data-theme="dark"]`, both of which are specificity (0,1,0). A generated
// sheet declaring `:root` is *also* (0,1,0), so which wins comes down to which
// `<link>` comes last in the document — a coupling between this file and
// shell.templ that no test can see and that a reordering of the head would break
// silently.
//
// `:root[data-theme]` is (0,2,0) and outranks both without help. It also has a
// second property worth having: it matches **only** where the head resolver has
// written `data-theme`, so a document served to a visitor whose script did not run
// — or a reader whose browser dropped it — keeps the product's own brand, which is
// the fail-toward-default direction §4.12.3 asks for everywhere else.
// `TestTheGeneratedSelectorOutranksEveryThemeBlockInTheBuiltStylesheet` measures
// the claim against the built file rather than asserting the string.

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// sheetSelector is what the generated declarations are scoped to.
//
// One selector, and the specificity argument above is why. Not `:root`, and not
// `html[data-theme]` — which would be equivalent in effect and one class *lower*,
// so a later revision choosing it would lose the property this selector exists for.
const sheetSelector = ":root[data-theme]"

// sheetHeader is the comment every generated sheet opens with.
//
// Fixed text. It names the file the sheet came from so that a reader inspecting
// `/c/{slug}/theme.css` in a network panel is looking at something they can
// change, and it carries no campaign value: a comment is a place to put a
// campaign's slug or its hex digits where a log line or a byte count could then
// expose them, and nothing here needs one.
const sheetHeader = `/* Generated by semiplane from the campaign's theme.yaml.
 * Do not edit this file: it is rewritten on every request. Edit theme.yaml
 * instead — semiplane validates it, and a token it will not accept is a token
 * this sheet will not carry. */`

// Manifest is a campaign's theme, validated and ready to serve.
//
// Opaque by construction: the only way to hold one is to have called `Parse` with
// bytes that passed, so a `Manifest` cannot be built around a colour that failed a
// floor or a font that is not in the content root. The generator takes a `theming`
// rather than a `Manifest` so that this type holds exactly the sheet and the one
// fact about it that the route needs.
type Manifest struct {
	// themed reports that this manifest described a theme, as against being empty.
	// False for the empty manifest and for a file that contained only comments.
	themed bool

	// sheet is the generated stylesheet, and it is never empty: a campaign with
	// no theme gets the header alone, which is a valid stylesheet and keeps the
	// route's response total.
	sheet string
}

// Sheet returns the generated stylesheet.
func (m Manifest) Sheet() string {
	return m.sheet
}

// Branded reports whether this manifest described a theme, as against being empty.
//
// The route uses it to decide between remembering a sheet and forgetting one, and
// the distinction is the difference between "this campaign has no theme" and "this
// campaign's theme was withdrawn" — the second of which must clear the first's
// remembered sheet, or a GM who deletes their manifest keeps their brand forever.
//
// The name says "branded" rather than "themed" because §4.12.3 uses the word
// throughout and a route method with two spellings of one concept is a route
// method somebody has to translate.
func (m Manifest) Branded() bool {
	return m.themed
}

// generate renders the stylesheet for a validated theme.
//
// Deterministic by construction: the colour declarations are emitted in the
// vocabulary's **sorted order**, the `@font-face` blocks in `fontSlots`'s sorted
// order, and the two font declarations in the same order, so the same manifest
// always produces the same bytes and therefore the same `ETag`. A sheet whose bytes
// changed on a restart with no input change would revalidate as new on every
// deploy, which is the kind of defect that is invisible until somebody looks at a
// cache hit rate.
//
// Iterating the vocabulary rather than the validated map is also what makes the
// first claim in this file's header true without reading the rest of it: the set of
// colour names this function can write is the set of names a campaign may set,
// because it is the same set.
//
// The lookup is checked rather than assumed. A vocabulary that grows to a fourth
// name would otherwise make every campaign emit a declaration for the name its
// manifest did not set, and the zero colour — `#000000` — is a colour nobody
// chose: the pair rule catches this for the pair, the emptiness branch below
// catches it for the image, and nothing else would catch it for whatever comes
// next.
func generate(themed theming) string {
	var sheet strings.Builder

	sheet.WriteString(sheetHeader)
	sheet.WriteString("\n")

	if !themed.themed {
		return sheet.String()
	}

	for _, face := range themed.fonts {
		writeFontFace(&sheet, face)
	}

	sheet.WriteString(sheetSelector)
	sheet.WriteString(" {\n")

	for _, name := range CampaignTokens() {
		switch name {
		case brandImage:
			// The image is a URL and not a colour, so it has its own writer and
			// its own emptiness check. Writing it through `writeDeclaration` would
			// need `colour` to be able to hold a string, which is how a colour
			// parser grows a second grammar.
			if themed.image != "" {
				writeDeclaration(&sheet, name, urlValue(themed.image))
			}
		default:
			value, named := themed.colours[name]
			if !named {
				continue
			}

			writeDeclaration(&sheet, name, value.String())
		}
	}

	for _, slot := range fontSlots {
		face, named := faceFor(themed.fonts, slot)
		if !named {
			continue
		}

		writeDeclaration(&sheet, face.token(), face.stackValue())
	}

	sheet.WriteString("}\n")

	return sheet.String()
}

// writeFontFace writes one `@font-face` block, and the three properties §4.12.2
// makes non-negotiable.
//
// **`font-display: swap` is written here and is not in the schema**, which is the
// whole implementation of "forced": there is no key a manifest could set it with,
// because `KnownFields(true)` refuses a document carrying one. A face that blocks
// for three seconds on a slow connection and then paints is the failure the
// property prevents, and §5.2's system stacks are the reason the product has
// nothing to lose by swapping.
//
// The `format()` descriptor is on the strength of the **extension the server
// resolved**, so a face is described by what was actually served rather than by
// what the manifest spelled; see `fontURL` for why the two can differ.
//
// `font-weight` and `font-style` are deliberately absent. A face declared with
// neither applies at every weight and every style, which is what a variable font
// wants and what a static family needs — and a face declared `font-weight: 400`
// beside a `--font-prose` used at 700 would fall back to the system stack for
// exactly the headings that carry the most text.
func writeFontFace(sheet *strings.Builder, face fontFace) {
	sheet.WriteString("@font-face {\n")
	sheet.WriteString("  font-family: ")
	sheet.WriteString(face.family)
	sheet.WriteString(";\n")
	sheet.WriteString("  src: url(\"")
	sheet.WriteString(face.url)
	sheet.WriteString("\") format(\"")
	sheet.WriteString(face.format)
	sheet.WriteString("\");\n")
	sheet.WriteString("  font-display: swap;\n")
	sheet.WriteString("}\n\n")
}

// writeDeclaration writes one custom property declaration, in the shape the
// browser reads and the shape the tests assert.
func writeDeclaration(sheet *strings.Builder, name, value string) {
	sheet.WriteString("  ")
	sheet.WriteString(name)
	sheet.WriteString(": ")
	sheet.WriteString(value)
	sheet.WriteString(";\n")
}

// faceFor is the face for one slot, if the manifest named it.
//
// **Keyed on `fontFace.slot`, not on the face's position.** The first version of
// this walked `fontSlots` and `faces` in step on the assumption that both were in
// the same order, which holds only while every manifest names *every* slot — and a
// manifest that themes only `ui` is not a mistake, it is a GM who wants the chrome
// in their typeface and the prose in the product's. That manifest would have had
// its one face written into `--font-prose`. Carrying the slot on the face removes
// the coupling entirely, and it is the third bug this file's structure invited and
// caught: `TestAFaceLandsInTheTokenItsOwnSlotNames` is the test.
func faceFor(faces []fontFace, slot string) (fontFace, bool) {
	for _, face := range faces {
		if face.slot == slot {
			return face, true
		}
	}

	return fontFace{}, false
}

// urlValue is a CSS `url()` token, for the one declaration whose value is a URL.
//
// **Double-quoted, not bare.** A bare `url(…)` ends at the first `)`, and a
// confined path can contain one — `art/a)b.png` is a legal filename and `os.Root`
// resolves it happily. Quoting moves the terminator to the first `"`, which
// `encodePath` has already percent-encoded, so the value is exactly the URL and
// the declaration ends where it should.
//
// The replace below is the belt to that pair of braces, and it is here because
// the quoting's guarantee is about *this* string rather than about every string
// that could ever arrive: `encodePath` percent-encodes `"` and `\` before a URL
// reaches this function, so neither can occur — but a function whose output is
// well-formed only when its caller remembered a step is a function with a second
// grammar hiding in it. A stray `"` would end the string and turn everything
// after it into declarations, which is the exact failure the quoting exists to
// prevent, and it costs two replacements to make that unreachable rather than
// merely untrue today.
// `TestTheGeneratedSheetQuotesEveryURLItEmits` feeds this both halves.
func urlValue(url string) string {
	return `url("` + strings.NewReplacer(`"`, "%22", `\`, "%5C").Replace(url) + `")`
}

// sheetValidator is the `ETag` for one campaign's generated sheet.
//
// **Strong**, and unlike `assets.assetValidator`, which is weak because it hashes
// a hint rather than the bytes: this package has the whole representation in hand
// — it wrote it, in this function — so a strong validator costs nothing and is
// exactly true. Two consequences worth having: RFC 9110 §13.1.5 lets a client put
// this validator in an `If-Range`, and two responses claiming the same validator
// really are byte-identical.
//
// **Salted with the campaign id**, and that is S-8.3 made structural for the same
// reason `assets` salts: two campaigns whose manifests happen to agree describe the
// same brand for readers of different campaigns, and a validator that crossed
// between them would let one campaign's cache answer for the other's. The URL
// already differs, so this is belt to braces — but the brace is one line and the
// belt is a URL a proxy is entitled to rewrite.
//
// A separator joins the pre-image rather than concatenation, because `1` + `23`
// and `12` + `3` are the same string. That is the ambiguity `content.CacheKey`
// names and this is the same answer.
func sheetValidator(campaignID int64, sheet string) string {
	preimage := strconv.FormatInt(campaignID, 10) + validatorPreimageSep + sheet

	digest := sha256.Sum256([]byte(preimage))

	return `"` + hex.EncodeToString(digest[:]) + `"`
}

// validatorPreimageSep joins the validator's inputs. See sheetValidator.
const validatorPreimageSep = "\x00"
