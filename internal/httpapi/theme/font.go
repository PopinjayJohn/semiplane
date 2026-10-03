package theme

// Fonts and the header image: §4.12.2's two paths.
//
// # What these two features share, and why they are one file
//
// Both are a **path out of an attacker-reachable manifest and into a `url()` the
// browser will fetch**, and both are the same decision four times over: is the
// string a path this route is willing to name, does it resolve inside the
// campaign's `os.Root`, does its extension come from a closed table, and is
// everything after the extension free of characters that would end the `url()`
// early. Answer it once, here, for both — because four copies of a confinement
// check is three chances to have written the fourth differently, and this is the
// one place in the package where a mistake is a stylesheet that fetches something
// the campaign's author chose.
//
// # The asset URL, and why the server builds it rather than the author
//
// Every URL this package emits is `/c/{slug}/assets/` followed by the confined,
// percent-encoded path. Three properties, and each is load-bearing:
//
//   - **The slug comes from the request context**, resolved by `campaigns.RequireRead`,
//     and not from the manifest. A manifest naming `../../other-campaign/logo.png`
//     resolves through `os.Root` and is refused, so there is no spelling that
//     reaches another campaign's asset route — and if there were, the asset route's
//     own gate would answer 404 anyway. Two gates, one of which is the filesystem.
//   - **The path is percent-encoded per segment.** The confined path can contain
//     `)`, `"`, `\` and spaces — a file is allowed to be called
//     `a) ; background: url(evil.css`. Emitting it raw ends the `url()` token early
//     and everything after it is a declaration.
//   - **The extension is from a closed table below.** Not `mime.TypeByExtension`
//     and not a sniff: the bytes are never read by this package, the file is
//     served by the assets route, and the only question here is whether this
//     route is willing to *name* it.
//
// # Fonts specifically, and the three rules the record states
//
// §4.12.1's left column says "`--font-prose` / `--font-ui` via `@font-face`
// (system stack always retained as fallback)", and §4.12.2 says `font-display:
// swap` is **forced**. Both are implemented as properties of the generator rather
// than as inputs:
//
//   - **`font-display: swap` is not in the schema.** There is no key a manifest can
//     set it with. A `fonts: prose: { font_display: block }` key is an unknown key
//     and `KnownFields(true)` refuses the document, so the honest way to force a
//     value is to make the value unreachable.
//   - **The system stack is retained**, and the stack itself is a copy of the one
//     `shell.css` declares. That is a copy, so it is pinned by
//     `TestTheSystemFontStacksAreTheOnesTheStylesheetDeclares`, which reads
//     `--font-prose` and `--font-ui` out of that file and fails the build when
//     they drift — the same arrangement, and for the same reason, as
//     `pageBackgrounds`. A request-time generator cannot read the built
//     stylesheet, so the values have to live in Go, and the only honest response
//     to a copy is a test that holds it against its original.
//   - **The family name is quoted and grammar-checked**, because it is written
//     into a stylesheet as a string literal. The grammar is the printable ASCII
//     minus the two characters that would close the quote or start an escape.

import (
	"fmt"
	"maps"
	"net/url"
	"path"
	"strings"

	"github.com/semiplane/semiplane/internal/content"
)

// The two slots §4.12.1's left column names.
//
// A fixed set of two rather than a map keyed by the manifest's own spelling,
// because the manifest's spelling has to be rejected if it is not one of these —
// and because each slot has its own counterpart in the product's stylesheet
// (`--font-prose`, `--font-ui`) and its own system stack, and a generator that
// looked them up by string would need a third table to say which slot is which.
const (
	slotProse = "prose"
	slotUI    = "ui"
)

// fontSlots is the order the generator emits `@font-face` blocks in.
//
// Sorted by name rather than by the order §4.12.1 prints them, because the sheet's
// bytes are hashed into its `ETag` and a manifest that renamed nothing must not
// revalidate as new when a reader reorders a struct field.
var fontSlots = []string{slotProse, slotUI}

// fontToken is the product token each slot's declaration lands in.
var fontToken = map[string]string{
	slotProse: fontProse,
	slotUI:    fontUI,
}

// fontFace is one validated `@font-face` and the system stack behind it.
type fontFace struct {
	// slot is the manifest's key for this face — `prose` or `ui`, and nothing
	// else. Carried on the value rather than recovered from its position in a
	// list, because a manifest that names only `ui` would otherwise make the face at
	// index 0 be `prose`'s and the generator would write the wrong token. That is a
	// real shape rather than a hypothetical one: the two slots are independently
	// optional.
	slot string

	// family is the quoted family name, validated and escaped by `quoted`.
	family string

	// url is the absolute, percent-encoded `/c/{slug}/assets/…` URL. Built by
	// `assetURL` from a path `os.Root` resolved, so it cannot address anything
	// outside the campaign.
	url string

	// format is the `format()` descriptor, from the extension's closed table.
	format string
}

// stackValue is the declaration this face lands in: its quoted family first, then
// the product's retained system stack.
//
// **The order is the fallback.** CSS resolves a family list left to right and takes
// the first the reader's machine can render, so the campaign's family is tried and
// the system stack is what remains when none of it can be. A campaign that names a
// font which 404s, which is corrupt, or which the reader's machine does not have
// gets the product's design — which is §4.12.1's parenthetical and §5.2's stated
// degradation, and it is why there is no branch anywhere in this package that
// could drop the tail.
func (f fontFace) stackValue() string {
	return f.family + ", " + systemStacks[f.slot]
}

// token is the product custom property this face's declaration lands in.
func (f fontFace) token() string {
	return fontToken[f.slot]
}

// fontFamily is the product's system stack for one slot, and the tail of every
// declaration the generator writes for it.
//
// **A copy of `shell.css`, and it is the third one in this package after
// `pageBackgrounds`** — so it gets the third paragraph of this file's header. The
// number is small because it is a fixed, tiny surface: three literals, each
// asserted against the stylesheet that declares it.
//
// A *single* string rather than a slice, because the sheet writes it verbatim and
// a slice would need a joining rule; the grammar is CSS's own font list and the
// only thing this package must guarantee is that it ends in `sans-serif` or
// `serif`, so that a webfont which fails to load still resolves to the generic
// family the reader's operating system picked.
var systemStacks = map[string]string{
	slotProse: `ui-serif, Georgia, Cambria, "Times New Roman", Times, serif`,
	slotUI:    `system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif`,
}

// SystemStacks returns the product's retained stacks, by slot, for the test that
// pins them to `shell.css`.
func SystemStacks() map[string]string {
	return maps.Clone(systemStacks)
}

// maxFamilyLen bounds a family name at 64 characters.
//
// The same bound `maxTokenNameLen` uses and for the same reason: the name is
// written into a generated declaration as a string literal, and a YAML value is
// an arbitrary-length byte string. §5.2 has no opinion, so this is a choice —
// and a generous one, since the longest name in circulation is well under half
// it.
const maxFamilyLen = 64

// quoted renders a family name as a CSS string literal, or refuses it.
//
// **The grammar is the printable ASCII range minus `"` and `\`**, which is what
// makes the function total: a value that survives it cannot close the quote, begin
// an escape, or introduce a control character, so the emitted literal is the value
// and nothing else. Everything non-ASCII is refused rather than escaped, and the
// cost is named in the refusal reason: a campaign wanting a family with an accent
// in its name gets told the name is refused rather than getting a sheet that
// renders with the product's font and no indication why. Silently working is the
// worse failure, and §4.12.3's direction is toward the default.
func quoted(family string) (string, bool) {
	if family == "" || len(family) > maxFamilyLen {
		return "", false
	}

	for _, letter := range family {
		switch {
		case letter >= ' ' && letter <= '~':
		default:
			return "", false
		}

		if letter == '"' || letter == '\\' {
			return "", false
		}
	}

	return `"` + family + `"`, true
}

// fontMedia is what one font extension is, in the two answers the generator needs:
// the `format()` descriptor a browser matches on, and whether the assets route
// will serve it at all.
//
// The table is keyed by extension **including the dot**, and the lookup is on the
// extension `path.Ext` produced — so a name with no extension, or one whose
// extension is in no row, is refused. An open fallback here would be how a closed
// table stops being closed, which is the argument `assets/media.go` makes at
// length; a font whose extension nobody decided about is not served by the assets
// route either, so accepting it would produce a sheet whose `src` 404s.
var fontMedia = map[string]string{
	".woff2": "woff2",
	".woff":  "woff",
	".ttf":   "truetype",
	".otf":   "opentype",
}

// imageMedia is the closed table for `--brand-header-image`.
//
// Narrower than `assets`' table on purpose, and the difference is worth one
// sentence: the assets route serves an image because a page can put it in an
// `<img>`, whereas this value becomes a **`background-image` on the campaign's
// header**, which means a format that can carry an alpha channel and a `noindex`
// flag is worth more than one that cannot. `.svg` is excluded on its own merits
// rather than by caution: an SVG is a document, and a background image is the one
// place this project would let a campaign's file be parsed as markup.
var imageMedia = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".webp": true,
	".avif": true,
}

// assetPrefix is the URL prefix every path this package emits begins with.
//
// `/c/{slug}/assets/` rather than a relative `../assets/`, and the reason is
// §4.12.2's own sentence: a font `src` in a stylesheet at `/c/{slug}/theme.css`
// resolves relatively, and a relative URL is one more thing whose resolution
// depends on where the browser thinks the document is. Absolute, and built from a
// slug the gate resolved.
const assetPrefix = "/c/%s/assets/"

// assetURL renders a confined, root-relative path as the URL the browser fetches,
// once its extension has been checked against `allowed`.
//
// Three steps, in this order, and the order is the argument:
//
//  1. **Resolve through `os.Root` first.** `root.At` returns the cleaned,
//     confined path and refuses a symlink and every escape, so what reaches the
//     URL builder has already been through the boundary the record names. A URL
//     built from an unresolved string would be a second confinement check, and a
//     second one is a weaker one.
//  2. **Check the extension against a closed table.** Not a sniff and not a
//     content type: this package never reads the bytes, and the media type the
//     browser is given is the assets route's decision from its own table.
//  3. **Percent-encode each segment.** `url.PathEscape` per segment, so a `)`
//     inside a directory name is `%29` and cannot close the `url()` token.
//
// The existence check is a `Stat`, and a missing file is a **refusal** rather
// than a 404: the manifest is not a request, there is no status to answer with,
// and §4.12.3's failure direction is the same answer for every way a theme can be
// wrong. A GM whose font file has not synced yet gets the product's system stack
// and an error line naming `fonts:` — a state they can fix, and a state they can
// see, which is the whole of §4.12.3's second requirement.
func assetURL(
	root *content.Root, slug, rel string, allowed map[string]bool,
) (string, string, bool) {
	target, err := root.At(rel)
	if err != nil {
		return "", "", false
	}

	confined := target.Path()

	extension := path.Ext(confined)
	if !allowed[extension] {
		return "", "", false
	}

	if _, err := target.Stat(); err != nil {
		return "", "", false
	}

	return fmt.Sprintf(assetPrefix, slug) + encodePath(confined), extension, true
}

// encodePath percent-encodes each segment of a confined path, keeping the
// separators.
//
// Per segment rather than whole-path, because `url.PathEscape` escapes `/` and a
// whole-path call would turn `fonts/x.woff2` into one segment — harmless here,
// since a browser would decode it back, and a needless surprise for anybody
// reading the emitted sheet in a network panel. The output is what
// `TestTheAssetURLPercentEncodesWhatWouldEndTheURLEarly` pins.
func encodePath(confined string) string {
	segments := strings.Split(confined, "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}

	return strings.Join(segments, "/")
}

// imageURL is `--brand-header-image`'s value: a confined, encoded asset URL.
//
// A boolean rather than a `url, format` pair because `format()` has no meaning for
// an image, and a function that returned an empty string for it would be a
// signature inviting a caller to interpolate it.
func imageURL(root *content.Root, slug, rel string) (string, bool) {
	value, _, ok := assetURL(root, slug, rel, imageMedia)

	return value, ok
}

// fontURL is a `src`'s value and the `format()` descriptor that goes with it.
//
// **The descriptor is looked up from the extension `assetURL` actually
// resolved**, not from `path.Ext` of the string the manifest wrote — the two
// differ the moment the manifest writes `fonts/x.woff2/../y.woff2`, which
// `path.Clean` folds, and pairing a cleaned URL with an uncleaned descriptor would
// emit a `format()` the browser rejects and the face would not load.
func fontURL(root *content.Root, slug, rel string) (src, format string, ok bool) {
	src, extension, ok := assetURL(root, slug, rel, fontAllowed)
	if !ok {
		return "", "", false
	}

	return src, fontMedia[extension], true
}

// fontAllowed is `fontMedia` keyed for `assetURL`'s membership test.
//
// Derived rather than a second table, because two tables over four extensions is
// two answers to "which extensions are fonts", and the answer is one list with the
// descriptors attached.
var fontAllowed = func() map[string]bool {
	allowed := make(map[string]bool, len(fontMedia))
	for extension := range fontMedia {
		allowed[extension] = true
	}

	return allowed
}()
