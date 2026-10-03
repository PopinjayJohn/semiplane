package theme

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/semiplane/semiplane/internal/content"
)

// The manifest: what a campaign writes to say how it looks.
//
// # The file
//
// One file, `theme.yaml`, at the campaign's content root, written by the same
// person who writes the vault in Obsidian. It is not a page, it is not front
// matter, and it is not served: it is read by the server on every request for
// `/c/{slug}/theme.css` and turned into a stylesheet the server generates. §4.12.2
// is explicit that "a campaign cannot ship arbitrary CSS" and this is the shape of
// that claim — the campaign ships *data*, and the only CSS that reaches a browser
// is CSS this package wrote.
//
// It is read through `content.Root` like every other path in a campaign (S-3.5),
// and the read is bounded before the bytes are in memory. That ordering is the
// same one `content.interpretFrontMatter` uses and for the same reason: a limit
// applied after the read bounds the parser but not the buffer, and a file a sync
// client can grow without limit is a lever on both at once.
//
// # The shape
//
//	tokens:
//	  --brand-accent: "#0e7c7b"
//	  --brand-accent-ink: "#ffffff"
//	  --brand-header-image: "art/banner.png"
//	fonts:
//	  prose:
//	    family: "Campaign Serif"
//	    src: fonts/serif.woff2
//	  ui:
//	    family: "Campaign Sans"
//	    src: fonts/sans.woff2
//
// Two keys and no others. Four decisions in that, each load-bearing:
//
//   - **Colours keyed by name, not by role.** `accent: "#0e7c7b"` would be shorter
//     and would read better, and it would also be a *second* vocabulary: the names
//     in this file would be the generator's and the names in the stylesheet would
//     be CSS's, and a rename of one would silently orphan the other. Writing the
//     token name in the file means the file, the vocabulary in ownership.go and
//     the declaration in the stylesheet are one list of spellings, and the
//     vocabulary is what decides which of them a campaign may use.
//   - **Under `tokens:`, with nothing else.** `KnownFields(true)` on the decoder
//     turns an unrecognised top-level key into a refusal rather than a shrug, and
//     that is worth a nesting level: a GM who writes `brand:` instead of `tokens:`
//     gets told so, rather than a manifest that validates as empty and a campaign
//     whose brand silently never applies. "Validates as empty" is the failure this
//     is the answer to. The same strictness is what makes `font-display` and
//     `font-weight` unreachable in the `fonts:` section: §4.12.2 says `swap` is
//     forced, and the way to force a value is to refuse the key that would set it.
//   - **Fonts keyed by role, not by name, and this is the asymmetry.** A font is
//     not a custom property — it is a `@font-face` and a family name, and the
//     generator owns the declaration because it owns the fallback. So `fonts:` is
//     keyed `prose`/`ui`, the two slots §4.12.1 names, and the CSS spelling of a
//     slot (`--font-prose`) is not something the author writes. Keying it by name
//     would put a name this package cannot validate in the manifest.
//   - **No version field.** A manifest without one cannot be migrated, and a
//     migration for a file with two keys is a file a person edits — which they can
//     do in Obsidian, where nothing is atomic and nothing is reviewed. If the
//     shape ever changes, the honest version is a new name (`theme-v2.yaml`) and a
//     generator that reads it, because a GM with an old vault must keep a working
//     theme. See ADR 0054.
//
// # What a manifest may not do
//
// Whole-manifest refusal, never per-key. §4.12.3's failure direction is the core
// theme standing, and applying the two names of a pair while refusing one of them
// produces a *third* thing: an accent with the default ink, or an ink with the
// default accent, neither of which was validated as a pair and both of which the
// GM did not write. A partial application is a theme nobody asked for and no
// contrast floor covers, so the refusal is total and names the offending token.
//
// **One exception, and it is not a partial application.** A manifest that names
// only `fonts:` and no `tokens:` is a campaign that has chosen to change its
// typeface and not its colours. Those are two independent halves of §4.12.1's
// left column and neither is half of a pair — the brand *pair* is the thing that
// cannot be halved, and the rule above is about the pair. A font on its own is
// applied, with the core brand untouched.

// ManifestFileName is where a campaign's theme manifest lives: one name, at the
// root of the content tree.
//
// A leading dot was rejected and the reasoning is worth keeping: the indexer skips
// dot-files, so a hidden manifest would be invisible to the index, to a directory
// listing in the wiki, and to the GM trying to find the file they are told the
// error came from. `theme.yaml` is a file an author can see and edit where they
// already work.
const ManifestFileName = "theme.yaml"

// MaxManifestBytes bounds the manifest at 16 KiB, and the number is small on
// purpose.
//
// `content.MaxDocumentBytes` is 4 MiB and `frontMatterMaxBytes` is 256 KiB, both
// sized for prose. A theme manifest is two colour literals, so 16 KiB is four
// orders of magnitude past what the schema can express — and the file's *grammar*
// is what bounds its content, not its size. The cap is not there to catch a large
// manifest; it is there so that "read the manifest" is a bounded operation even
// when the file is not a manifest at all, which is the case a hostile vault
// presents: a sync client writes four gigabytes into `theme.yaml`, and without
// this the request reads all four before discovering there is nothing to parse.
const MaxManifestBytes = 16 << 10

// The refusals. All of them are *answers*, not faults: a campaign's theme is
// attacker-reachable input (Obsidian sync writes it, §4.12.2), and §4.12.3's
// requirement is that a rejected theme degrades to the default rather than
// shipping an unreadable UI. Nothing here is a 5xx.
var (
	// ErrNoManifest is a campaign with no manifest, which is not a failure and
	// not an absence either: it is the ordinary state of every campaign that has
	// not been branded, and it produces the core theme.
	ErrNoManifest = errors.New("theme: the campaign has no theme manifest")

	// ErrManifestTooLarge is a manifest over MaxManifestBytes.
	ErrManifestTooLarge = fmt.Errorf("%w over the %d-byte limit",
		content.ErrDocumentTooLarge, MaxManifestBytes)

	// ErrMalformedManifest is a manifest the YAML parser would not read, or one
	// carrying a key this build has no name for.
	ErrMalformedManifest = errors.New("theme: the theme manifest is not valid YAML")

	// ErrUnreadableManifest is a manifest that exists and could not be read —
	// a permissions change, an I/O error. Distinct from the refusals above
	// because it is the one that answers 500: the campaign may well have a
	// perfectly good theme on disk, and telling every reader of it that there is
	// none would be a lie produced by our own fault.
	ErrUnreadableManifest = errors.New("theme: the theme manifest could not be read")
)

// document is the manifest as parsed, before any decision has been made about it.
//
// A two-field struct rather than `map[string]any` because `KnownFields(true)` is
// the mechanism that refuses an unrecognised key, and it only works against a
// struct: the moment the target is a bare map, every key is by definition
// recognised and the strictness is gone. The inner map is `map[string]any` because
// a token's value has two grammars — a colour literal for the brand pair and a
// path for the header image — and a typed inner map would be a coercion in the one
// place a coercion is exactly the failure: `123456` reaching `--brand-accent` as
// an integer rather than as the six characters the author read.
type document struct {
	// `tokens` is the colour and image half. It is a tag and not a constant
	// because a struct tag cannot name one, and this sentence is the answer to
	// "what may a manifest contain" for a reader who would rather not read a struct
	// literal: exactly this, and `KnownFields(true)` refuses anything else
	// (`TestAnUnknownKeyIsRefused` is the evidence).
	Tokens map[string]any `yaml:"tokens"`

	// `fonts` is the `@font-face` half, keyed by slot. Nil rather than an empty map
	// when absent, because "no fonts" and "fonts: {}" are the same answer and only
	// one of them should be a distinguishable state.
	Fonts map[string]fontEntry `yaml:"fonts"`
}

// fontEntry is one slot's `@font-face`: a family name and a path.
//
// Two keys and no others, and that is §4.12.2's "font-display: swap forced"
// implemented as a **closed schema** rather than as a check. A manifest that wants
// `font-display: block` writes it, `KnownFields(true)` refuses the document, and
// there is no spelling of a font declaration this package will honour without the
// swap.
//
// `weight` and `style` are absent for a reason worth stating: a face declared with
// neither applies at every weight and style, which is what a variable font wants
// and what a static family needs anyway. Adding them later is additive and no
// existing manifest changes meaning.
type fontEntry struct {
	// Family is the CSS font family name, unquoted as written. `quoted` decides
	// whether it may be emitted, and a value that fails is a refusal rather than an
	// escaped literal — see font.go's `quoted` for why escaping is not the answer.
	Family string `yaml:"family"`

	// Src is a path **relative to the campaign's content root**, resolved through
	// `os.Root` and rewritten to `/c/{slug}/assets/…`. Never a URL: the grammar
	// refuses a scheme, a leading slash and a `//`, so there is no spelling that
	// names a host.
	Src string `yaml:"src"`
}

// Parse reads a campaign's manifest and returns the theme it describes.
//
// Total, in the sense that matters here: a manifest it will not apply returns an
// error and **no** stylesheet, and the caller is expected to keep whatever theme
// the campaign had rather than to serve half of this one. `ErrNoManifest` is not
// returned here — an absent file is not a manifest that failed, and the caller
// reads the file, so absence is its own case before `Parse` is reached.
//
// root and slug are the other two inputs and neither is optional, for the same
// reason a colour's hex digits are not: the paths in this manifest become
// `url()`s, and a URL is only knowable against a campaign. `root` may be nil for a
// manifest that names no path — every refusal test in the package relies on that —
// and a manifest that *does* name one with a nil root is a refusal, not a panic.
//
// The error is a `*RefusalError` for every decision this package makes about the
// manifest's *content*, including a colour that fails a contrast floor and a font
// that is not in the content root: a caller that wants to tell a GM which token it
// was reads it with `errors.As`. The parser's own message is discarded rather than
// wrapped, following `content.interpretFrontMatter`: a YAML error quotes the line
// it choked on, and that line is a campaign file's contents.
func Parse(src []byte, root *content.Root, slug string) (Manifest, error) {
	if len(src) > MaxManifestBytes {
		return Manifest{}, ErrManifestTooLarge
	}

	var parsed document

	decoder := yaml.NewDecoder(bytes.NewReader(src))
	decoder.KnownFields(true)

	if err := decoder.Decode(&parsed); err != nil {
		// An empty document is not an error. A manifest that is nothing but
		// comments decodes to a zero value, and "a GM wrote a file with the
		// explanation in it and no theme in it yet" is a state to serve, not a
		// fault to report. The core sheet comes back rather than an empty string,
		// because the route serves `Sheet()` unconditionally and an empty body is
		// not a stylesheet.
		if errors.Is(err, io.EOF) {
			return Manifest{sheet: generate(theming{})}, nil
		}

		return Manifest{}, ErrMalformedManifest
	}

	themed, err := parsed.theme(root, slug)
	if err != nil {
		return Manifest{}, err
	}

	return Manifest{themed: themed.themed, sheet: generate(themed)}, nil
}

// theme validates the manifest and returns the theme it describes, or the zero
// value for "no theme".
//
// Five steps, and the order is the argument:
//
//  1. **Every token name, in sorted order.** Sorted rather than map order, for a
//     reason that is about the log and not about the sheet: a refusal names the
//     *first* offending token, and which one that is must not depend on Go's map
//     iteration order — a manifest with two bad names would report a different one
//     per request, and an operator comparing two log lines would conclude they were
//     two different manifests.
//  2. **The pairs, both halves or neither.** Driven by the vocabulary's own partner
//     column rather than by a hand-written "is the accent there too", because the
//     completeness rule cannot drift from the vocabulary: there is only one list
//     and it carries both facts.
//  3. **The floors**, if the brand pair is present at all.
//  4. **The header image**, resolved through the root.
//  5. **The fonts**, resolved through the root, in slot order.
//
// The colour half and the path half are separate steps because their grammars are
// and their failure modes are: a colour fails a *measurement*, a path fails a
// *boundary*, and a manifest that gets both wrong should be told about the colour
// first because that is the one a reader of the campaign would notice.
func (d document) theme(root *content.Root, slug string) (theming, error) {
	if len(d.Tokens) == 0 && len(d.Fonts) == 0 {
		return theming{}, nil
	}

	parsed := map[string]colour{}

	for _, name := range slices.Sorted(maps.Keys(d.Tokens)) {
		// `--brand-header-image` is a path and never reaches the colour grammar.
		// `headerImage` below reads it straight out of the document and resolves it
		// through the root, so the loop that validates *colours* must step over it:
		// handing it to `claimToken` is asking a colour parser about a path and
		// being told "a colour is written #rrggbb" — a true sentence that refuses
		// every manifest naming an image, which is what the first version of this
		// loop did and what made the whole image path unreachable.
		if name == brandImage {
			continue
		}

		value, err := claimToken(name, d.Tokens[name])
		if err != nil {
			return theming{}, err
		}

		parsed[name] = value
	}

	for _, name := range slices.Sorted(maps.Keys(parsed)) {
		partner := campaignVocabulary[name]
		if partner == "" {
			continue
		}

		if _, half := parsed[partner]; !half {
			return theming{}, refused(name, reasonHalfPair)
		}
	}

	colours, err := measure(parsed)
	if err != nil {
		return theming{}, err
	}

	image, err := headerImage(root, slug, d.Tokens)
	if err != nil {
		return theming{}, err
	}

	faces, err := fontFaces(root, slug, d.Fonts)
	if err != nil {
		return theming{}, err
	}

	return theming{themed: true, colours: colours, image: image, fonts: faces}, nil
}

// headerImage resolves `--brand-header-image`, and reports "no image" as the
// empty string.
//
// **The two states are different and the difference is a whole key.** A manifest
// that does not name the token leaves the product's `none`; a manifest that names
// it with an empty value is refused, because `--brand-header-image: ""` is
// something a GM writes meaning "none" and would silently produce a URL for a
// file that is not there. The null branch is the same reasoning as
// `claimToken`'s unquoted-colour branch, for the same file and the same person.
//
// The **extension** is checked inside `imageURL`, from `imageMedia`, and there is
// deliberately no contrast floor here: §4.12.3 states two and both are about
// colour. The reason a brand image cannot make the campaign unreadable is in
// `theme.css`, which paints the product's own scrim over the banner above the
// image — so whatever the image is, the text on top of it is the text colour on
// the scrim, and the pair is one the contrast gate already holds.
func headerImage(root *content.Root, slug string, tokens map[string]any) (string, error) {
	raw, named := tokens[brandImage]
	if !named {
		return "", nil
	}

	rel, isText := raw.(string)
	if !isText {
		return "", refused(brandImage, reasonNotAPath)
	}

	if rel == "" {
		return "", refused(brandImage, reasonEmptyPath)
	}

	url, ok := imageURL(root, slug, rel)
	if !ok {
		return "", refused(brandImage, reasonImagePath)
	}

	return url, nil
}

// fontFaces resolves the `fonts:` section, in slot order.
//
// **Both halves or neither, per slot.** A `@font-face` whose family is named and
// whose `src` is missing is a face the browser never loads, and a
// `--font-prose` naming it is a stack whose only usable entry is a family no
// machine has — which §4.12.1's "system stack always retained as fallback" is
// precisely there to prevent, and which a partial application would defeat by
// being applied at all. So the completeness rule here is the same shape as the
// colour pair's, and for the same reason: a half-written theme is a theme nobody
// asked for.
//
// **An unknown slot is a refusal**, not a skipped entry. `KnownFields(true)`
// does not reach the *values* of a map, so `fonts: {titel: …}` decodes without
// complaint; without this check it would be ignored, and a GM who misspelled the
// slot would get a campaign that quietly kept its system stack with nothing in
// any log to say so. The name is validated by `slotKnown` before it is used, so
// nothing from the manifest reaches the generated sheet.
func fontFaces(root *content.Root, slug string, declared map[string]fontEntry) ([]fontFace, error) {
	var faces []fontFace

	for _, slot := range fontSlots {
		entry, named := declared[slot]
		if !named {
			continue
		}

		face, err := resolveFace(root, slug, slot, entry)
		if err != nil {
			return nil, err
		}

		faces = append(faces, face)
	}

	// The unknown slots, sorted, so the refusal is the same one every request.
	for _, slot := range slices.Sorted(maps.Keys(declared)) {
		if !slices.Contains(fontSlots, slot) {
			return nil, refusedFont(reasonUnknownSlot)
		}
	}

	return faces, nil
}

// brandFontPrefix is what a refusal about a font is named, when the manifest's own
// slot name may not be echoed.
//
// A slot is a YAML map key like a token name: an arbitrary byte string that would
// otherwise reach a log line. `resolveFace` validates the slot it is given against
// `fontSlots`, so the name that reaches the refusal is one of two fixed words.
const brandFontPrefix = "fonts:"

// resolveFace is one slot's decisions, in the order that makes each refusal the
// most useful one:
//
//  1. **The family name**, because it is what a GM sees in the browser's font
//     picker and what the refusal can name.
//  2. **The path**, because a missing `src` next to a good family name is the
//     failure that would render as "my font did not apply" with nothing in the
//     log.
func resolveFace(
	root *content.Root,
	slug, slot string,
	entry fontEntry,
) (fontFace, error) {
	if entry.Family == "" {
		return fontFace{}, refusedFont(reasonEmptyFamily)
	}

	family, ok := quoted(entry.Family)
	if !ok {
		return fontFace{}, refusedFont(reasonFamilyName)
	}

	if entry.Src == "" {
		return fontFace{}, refusedFont(reasonEmptySrc)
	}

	src, format, ok := fontURL(root, slug, entry.Src)
	if !ok {
		return fontFace{}, refusedFont(reasonFontPath)
	}

	return fontFace{slot: slot, family: family, url: src, format: format}, nil
}

// claimToken is one token's decision: is the name one a campaign may set, and is
// the value a colour.
//
// Four refusals and one acceptance, in this order, and the order is the
// argument:
//
//  1. **The name's shape.** Before ownership, because an unrecognisable name has
//     no owner and no rule — and because the name is what reaches a log line.
//  2. **Ownership.** §4.12.1's table, consulted as data. A product token is
//     refused with the reason that matches it, and the three product refusals are
//     three different sentences about three different things: the accessibility
//     contract, the font channel, and "that is ours".
//  3. **The value's grammar.** Last, so that a manifest naming a protected token
//     is refused for being protected rather than for a colour it never got as far
//     as parsing.
func claimToken(name string, raw any) (colour, error) {
	if _, wellFormed := tokenName(name); !wellFormed {
		return colour{}, &RefusalError{Reason: reasonMalformedKey}
	}

	switch ownerOf(name) {
	case ownerCampaign:
	case ownerProduct:
		if protected(name) {
			return colour{}, refused(name, reasonProtected)
		}

		if fontOverride(name) {
			return colour{}, refused(name, reasonFontSection)
		}

		return colour{}, refused(name, reasonProductToken)
	}

	// `--brand-header-image` is a path and does not belong to this function at
	// all — `document.theme` steps over it before calling. The branch is here for
	// the caller that has not: a future call site that validates "the tokens" in
	// one loop would otherwise reach `parseColour` and be told a path is not a
	// colour, which is true and sends a GM to the wrong half of their file. The
	// path is the answer either way, and the reason says so.
	if name == brandImage {
		return colour{}, refused(name, reasonNotAPath)
	}

	// A null value is its own answer rather than a colour failure, and it is the
	// single most likely mistake in this file: YAML reads `#` as the start of a
	// comment, so an unquoted `--brand-accent: #0e7c7b` hands the parser an
	// *empty* value and not a colour. Without this branch the author is told their
	// colour is not a colour, which is true and useless; with it, they are told
	// the two characters they have to change.
	if raw == nil {
		return colour{}, refused(name, reasonUnquoted)
	}

	value, isText := raw.(string)
	if !isText {
		return colour{}, refused(name, reasonNotAColour)
	}

	parsed, err := parseColour(value)
	if err != nil {
		return colour{}, refused(name, reasonNotAColour)
	}

	return parsed, nil
}

// theming is a validated theme, or the absence of one.
//
// A map of colours rather than two fields, and the reason is that
// `campaignVocabulary` is the list of names a campaign may set — so the generator
// must ask *that* list which declarations to write. A `theming` with an `accent` and
// an `ink` field would put a second list of names in this package, and the two
// would agree until somebody added a fourth overridable token to ownership.go and
// the generator went on emitting three.
//
// A struct rather than a bare map for the same reason `document` is a struct: the
// "no theme" state must be representable, and a zero map with a nil entry is a
// subtler way of saying that than a flag next to the values. The flag is
// `themed`, and the reason it cannot be derived from the other three is the case
// that makes it necessary: a manifest naming **only** `fonts:` is a theme, and a
// manifest naming nothing at all is not, and both have an empty colour map.
type theming struct {
	// themed reports that the manifest named a token or a font and they all
	// passed. False for a manifest that named nothing, which is a campaign with no
	// theme rather than a campaign with a black one.
	themed bool

	// colours is the validated value for each brand name the manifest set. Every
	// key is in `campaignVocabulary`, every value came out of `parseColour`, and it
	// is the only thing `generate` reads for a colour.
	colours map[string]colour

	// image is `--brand-header-image`'s resolved URL, or "" for the product's own
	// `none`. Always `/c/{slug}/assets/…` when non-empty — see font.go's
	// `assetURL` for why the server builds it and not the author.
	image string

	// fonts is one `@font-face` per named slot, in `fontSlots` order. Each is a
	// resolved, percent-encoded URL and a `format()` descriptor, so the generator
	// interpolates no path of its own.
	fonts []fontFace
}

// measure applies §4.12.3's two floors to a validated set of tokens, and returns
// the colours to generate when they pass.
//
// **Both floors are conditional on the pair being present at all**, and that is
// what lets a manifest that themes only a font through: the zero value of
// `map[string]colour` has no accent in it, and measuring the pair with the zero
// colour would refuse every font-only theme for a contrast failure its author
// never caused. An absent pair is not a failed pair; `document.theme` has already
// established that a *named* pair is complete, so the only case reaching here
// without one is a manifest that named no colour at all.
//
// The pair floor first, then the page floors, and the order is the order a GM
// should fix things in: the ink is the one they can change freely, and a brand
// that is unreadable *on itself* is a worse problem than one that is hard to see
// against the page.
//
// The floors are named to the pair rather than to the set, and that is a real
// limit: `brandAccent` and `brandInk` are the only two tokens with a contrast
// requirement, because they are the only two whose relationship to something else
// is specified. `--brand-header-image` has no floor because it is not a colour —
// the reason a brand image cannot make a campaign unreadable is in `theme.css`,
// which paints the product's own scrim over it.
func measure(parsed map[string]colour) (map[string]colour, error) {
	accent, hasAccent := parsed[brandAccent]
	if !hasAccent {
		return parsed, nil
	}

	ink := parsed[brandInk]

	if contrastRatio(ink, accent) < brandPairFloor {
		return nil, refused(brandInk, reasonPairFloor)
	}

	for index, page := range pageBackgrounds {
		if contrastRatio(accent, page) < brandPageFloor {
			return nil, refused(brandAccent, reasonPageFloorOn(themeNames[index]))
		}
	}

	return parsed, nil
}

// reasonPageFloorOn is the page-floor reason for one theme, so the two refusals
// differ in exactly one word and a GM can tell which page failed.
func reasonPageFloorOn(theme string) string {
	return reasonPageFloor + " in the " + theme + " theme"
}

// RefusalError is a manifest decision semiplane will not apply.
//
// A named exported type rather than a formatted error because §4.12.3 requires
// the campaign overview to show a GM "a notice naming the rejected pair", and a
// caller cannot do that with a string: it needs the token, and it needs to know
// the difference between "you named the product's token" and "your colour is too
// close to the page to read".
//
// `Token` is empty when the manifest's own *name* was the problem, and `Reason`
// is always a fixed sentence plus, at most, one validated token name. Neither
// field ever carries a byte from the manifest's values.
type RefusalError struct {
	// Token is the custom property name the refusal is about, when the name was
	// a well-formed one. Empty otherwise, and `Reason` says so.
	Token string

	// Reason is the sentence a GM reads. Fixed text, plus the token name or the
	// theme's name — never a value from the file.
	Reason string
}

// Error satisfies the error interface, and is the log line's text.
func (e *RefusalError) Error() string {
	if e.Token == "" {
		return e.Reason
	}

	return e.Token + ": " + e.Reason
}

// refused builds a refusal about a named token.
func refused(token, reason string) *RefusalError {
	return &RefusalError{Token: token, Reason: reason}
}

// refusedName builds a refusal whose `Token` is a key **this package owns**.
//
// The distinction from `refused` is the same boundary S-12.3 draws, one level down:
// a *token name* is either one of the vocabulary's own spellings or the empty
// string, so it is safe to print; a **`fonts:` slot** is an arbitrary byte string
// a sync client wrote, so it is not. Every refusal about the font section goes
// through here with a fixed name, which is why a GM who misspells a slot is told
// which slots exist rather than being quoted back into a log line.
func refusedFont(reason string) *RefusalError {
	return &RefusalError{Token: brandFontPrefix, Reason: reason}
}

// The reasons. Named constants rather than literals at the call sites because
// they are part of this package's contract — a caller may match on them — and
// because a message that varies with its call site is a message nobody can grep.
//
// None of them contains a byte from the manifest. That is the constraint all of
// them obey, and it is why the offending value never appears in one — see
// `errNotAColour` — and why the font refusals name `fonts:` rather than the slot
// the author wrote.
const (
	reasonProductToken = "this is semiplane's own token and no campaign theme may set it"
	reasonProtected    = "this token is part of the accessibility contract and is never overridable"
	reasonMalformedKey = "a token name must be -- followed by lowercase letters, digits or dashes"
	reasonUnquoted     = "a colour must be quoted: an unquoted # begins a YAML comment"
	reasonNotAColour   = "a colour is written #rrggbb, with no alpha and no other notation"
	reasonHalfPair     = "the brand is two tokens; set both or set neither"
	reasonPairFloor    = "the brand ink is below 4.5:1 on the brand accent"
	reasonPageFloor    = "the brand accent is below 3:1 on the page"
	reasonConfinement  = "the manifest is a link, or lies outside the campaign's own root"

	reasonFontSection = "set this in the manifest's fonts: section, which keeps the " +
		"system stack as a fallback"
	reasonNotAPath  = "a path is written relative to the campaign's own content root"
	reasonEmptyPath = "a path cannot be empty; delete the token to use semiplane's own"
	reasonImagePath = "the header image must be a file in this campaign, named with a " +
		".png, .jpg, .jpeg, .webp or .avif extension"
	reasonUnknownSlot = "a font slot is prose or ui, and no other"
	reasonEmptyFamily = "a font needs a family name"
	reasonFamilyName  = "a font family is printable ASCII, up to 64 characters, " +
		"with no quote or backslash in it"
	reasonEmptySrc = "a font needs a src naming a file in this campaign"
	reasonFontPath = "a font src must be a file in this campaign, named with a " +
		".woff2, .woff, .ttf or .otf extension"
)

// maxTokenNameLen bounds a token name at 64 characters.
//
// A bound because a YAML key is an arbitrary-length byte string and this value
// reaches a log line and a refusal message. CSS custom property names have no
// length limit in the specification, so this is a choice rather than a standard:
// the longest name in the product's stylesheets is `--surface-sunken-border` at 22,
// and a campaign cannot invent a meaningful name an order of magnitude longer
// than that. Everything longer is refused with `reasonMalformedKey`, unquoted.
const maxTokenNameLen = 64

// tokenName validates a manifest key as a CSS custom property name.
//
// The grammar is `--` followed by at least one character from `a-z`, `0-9` and
// `-`. That is a subset of what CSS permits, deliberately: a name that reaches a
// log line, a refusal message and a generated declaration is a name worth
// restricting to characters that cannot terminate a line, quote a string, or open
// a comment. Uppercase and non-ASCII are excluded for the same reason the colour
// grammar is `#rrggbb` and nothing else — the product's own names are all
// lowercase, so the subset loses nothing a campaign could have used.
func tokenName(name string) (string, bool) {
	if !strings.HasPrefix(name, "--") || len(name) > maxTokenNameLen {
		return "", false
	}

	rest := name[2:]
	if rest == "" {
		return "", false
	}

	for _, letter := range rest {
		switch {
		case letter >= 'a' && letter <= 'z',
			letter >= '0' && letter <= '9',
			letter == '-':
		default:
			return "", false
		}
	}

	return name, true
}

// readManifest reads a campaign's manifest through its confined root.
//
// Three answers, and the middle one is why this function exists rather than a
// `defer` at each call site:
//
//   - `ErrNoManifest` — there is no `theme.yaml`. Not a failure: the campaign is
//     unbranded, and the caller serves the core theme and forgets any theme it
//     had. Withdrawal is not an error, and treating a deleted manifest as one
//     would keep serving a brand a GM had deliberately removed.
//   - a `*RefusalError` — the file is there and will not be applied. The caller's
//     problem, not this one's: keep the last good theme and log it.
//   - anything else — a read that failed. The caller answers 500.
//
// The size cap is applied to the *read*, not after it: `Target.ReadFile` would
// have the whole file in memory before anything could look at it, and the file is
// attacker-reachable.
func readManifest(root *content.Root) ([]byte, error) {
	target, err := root.At(ManifestFileName)
	if err != nil {
		return nil, classifyManifestRead(root, err)
	}

	file, err := target.Open()
	if err != nil {
		return nil, classifyManifestRead(root, err)
	}

	defer func() {
		// The close error is deliberately dropped. The bytes are read by this
		// point, a close on a read-only handle fails only if the descriptor is
		// already gone, and the alternative — threading a close error into a
		// return that has three answers of its own — buys a log line nobody
		// acts on. `assets` logs its close failure because it hands the handle
		// to `ServeContent`, which reads after the defer.
		_ = file.Close()
	}()

	// One `stat` before the read, because a *directory* called `theme.yaml` opens
	// successfully on Unix and then fails the read with a platform-specific error —
	// which would make this campaign's theme a 500 for as long as the directory
	// existed. `content.Target.ReadFile` performs the same check for the same
	// reason; it is not available here because the bounded read needs the handle.
	//
	// The file was already resolved through `Root.At`, so this stat cannot escape
	// the root and answers only "is this a regular file".
	if info, statErr := file.Stat(); statErr == nil && !info.Mode().IsRegular() {
		return nil, ErrNoManifest
	}

	// `Parse` is what refuses an over-long document, and there is deliberately no
	// second check here. Two checks on one number is one check and a place for
	// them to disagree — and the mutation that removes this one is provably
	// unobservable, because the other one still holds. `io.LimitReader` is the
	// part that is this function's own: it bounds how many bytes are *read*, which
	// no later check can do, since by then the bytes are already in memory.
	//
	// One byte past the cap, so the read is bounded even for a file that is
	// enormous.
	src, err := io.ReadAll(io.LimitReader(file, MaxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreadableManifest, err)
	}

	return src, nil
}

// classifyManifestRead maps a root's refusal to one of this package's answers.
//
// Three, and the middle one is the one worth reading twice:
//
//   - `ErrNoManifest` for a missing file and for a path that is not a file. From
//     this package's side those are the same fact: there is no manifest to apply.
//   - a `*RefusalError` for a link. `content.RefuseSymlinks` is S-4.4, and
//     `content.Root`'s own note on it is the right frame — "a vault with a link in
//     it is a configuration to look at, not an attack". So a symlinked
//     `theme.yaml` is a refused manifest, answered with the campaign's last good
//     theme and one error line. A 500 would be wrong in both directions: it tells
//     every reader of the campaign that the instance is broken, over a file the
//     campaign's owner can delete.
//   - anything else keeps its identity for the caller to classify as a fault.
//
// The sentinels are wrapped rather than replaced, so `errors.Is` still finds
// `content.ErrOutsideRoot` for a caller that wants to say so in a log.
//
// # Why the root has to be asked as well
//
// `content` has exactly two answers for an operation that failed: `ErrNotExist`
// and `ErrOutsideRoot`. Everything that is neither — a permission the process
// does not have, an `EISDIR` the pre-stat missed, and a **content root whose
// handle has been closed** — is reported as `ErrOutsideRoot`, because
// `content.classify` is documented as "a permission error is a refusal" and has
// nowhere else to put the rest.
//
// So `ErrOutsideRoot` arriving here does not mean the manifest left the root.
// By the time this runs, `Root.At` has already confined the name — `theme.yaml`
// is this package's own constant, and `checkSymlink` has answered for the link
// on it — which leaves two possibilities, and they have opposite answers: a
// path the policy refuses (keep the last good theme, tell the GM) and a handle
// we can no longer read through (a fault, 500). Taking the first answer for the
// second case would serve the core theme to every reader of a campaign whose
// root is dead, and the campaign owner would have no way to tell that from a
// `theme.yaml` they had deleted.
//
// The probe is `rootIsLive`, and it runs on this branch alone — the healthy
// campaign never reaches it, so the read pays nothing.
func classifyManifestRead(root *content.Root, err error) error {
	switch {
	case errors.Is(err, content.ErrNotExist), errors.Is(err, content.ErrNotDir):
		return ErrNoManifest

	case errors.Is(err, content.ErrSymlink), errors.Is(err, content.ErrOutsideRoot):
		if !rootIsLive(root) {
			return fmt.Errorf("%w: %w", ErrUnreadableManifest, err)
		}

		return &RefusalError{Reason: reasonConfinement}
	}

	return fmt.Errorf("%w: %w", ErrUnreadableManifest, err)
}

// rootIsLive reports whether a campaign's content root can still be operated on.
//
// `content.Root` puts its liveness check in `Walk` and nowhere else, because
// `os.Root.FS()` on a closed handle reports no error and no entries — a walk of
// a dead descriptor is an empty vault — so the check is a `Stat` on the root
// itself, the one operation whose answer differs between live and closed. Every
// exported operation that goes through `content.classify` loses the distinction
// on the way out.
//
// `Walk` is therefore being used as the probe rather than as a walk: the
// callback refuses to descend, so the cost is that single `Stat` and no
// `ReadDir`. The alternative — a second liveness API on `content.Root` — is the
// right long-term answer and is reported to the integrator; this is the shape
// that does not need a package outside this work item to change.
func rootIsLive(root *content.Root) bool {
	return root.Walk(func(string, fs.DirEntry, error) error {
		return fs.SkipAll
	}) == nil
}
