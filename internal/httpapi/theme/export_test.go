package theme

import (
	"maps"
	"slices"
)

// The seams the tests in `theme_test` need, and nothing else.
//
// An alias for the unexported `colour` rather than a re-declaration, so the type
// the tests compare is the same type the package produces — a copy would be a
// second answer to "what colour is this" and the two could drift.
//
// Each function is one line wrapping one unexported call. That is the whole of
// this file's job: it lets `manifest_test.go` pin the contrast arithmetic against
// the ratios §6.2 and §6.3 quote, and `sheet_test.go` drive the generator over
// states a manifest cannot express — a generator that is broken in a way the
// schema prevents is exactly the generator this file exists to test.

// Colour is a parsed `#rrggbb` value.
type Colour = colour

// ParseColour reads one `#rrggbb` literal.
func ParseColour(value string) (Colour, error) { return parseColour(value) }

// ContrastRatio is the WCAG 2.2 contrast ratio between two colours.
func ContrastRatio(first, second Colour) float64 { return contrastRatio(first, second) }

// PageBackgrounds returns the two `--bg` values the page floor is measured
// against, in light-then-dark order.
func PageBackgrounds() []Colour { return slices.Clone(pageBackgrounds) }

// ThemeNameAt names the page at an index into PageBackgrounds.
func ThemeNameAt(index int) string { return themeNames[index] }

// CoreSheet is the stylesheet a campaign with no theme is served: the generated
// header and no declarations.
func CoreSheet() string { return generate(theming{}) }

// Generator is the generator, for the tests that check what it writes without
// going through a manifest — a manifest cannot express every malformed state a
// generator could be broken in.
//
// `image` and `faces` are the two halves `Parse` would have resolved, passed in
// already-built so a test can hand the generator a URL or a face it could not
// otherwise produce — including one carrying a `"` or a `)`, which is how
// `TestTheGeneratedSheetQuotesEveryURLItEmits` checks the quoting rather than the
// encoder.
func Generator(brandColours map[string]Colour, image string, faces []FontFace) string {
	colours := make(map[string]colour, len(brandColours))
	maps.Copy(colours, brandColours)

	themed := theming{themed: true, colours: colours, image: image}
	for _, face := range faces {
		themed.fonts = append(themed.fonts, fontFace{
			slot:   face.Slot,
			family: face.Family,
			url:    face.URL,
			format: face.Format,
		})
	}

	return generate(themed)
}

// FontFace is a `@font-face` handed to Generator.
type FontFace struct {
	Slot   string
	Family string
	URL    string
	Format string
}

// Quoted renders a family name as the CSS literal the generator would emit, or
// reports that it refuses it. Exported so the grammar is testable without a
// content root or a manifest.
func Quoted(family string) (string, bool) { return quoted(family) }

// EncodePath is the per-segment percent-encoding applied to a confined path.
func EncodePath(confined string) string { return encodePath(confined) }

// ImageAllowed reports whether an extension is in the header image's closed table.
func ImageAllowed(extension string) bool { return imageMedia[extension] }

// FontFormat is the `format()` descriptor for a font extension, and false when the
// extension is not in the closed table.
func FontFormat(extension string) (string, bool) {
	format, ok := fontMedia[extension]

	return format, ok
}

// AssetPrefix is the URL prefix every path the generator emits begins with,
// as a format string taking the slug.
func AssetPrefix() string { return assetPrefix }
