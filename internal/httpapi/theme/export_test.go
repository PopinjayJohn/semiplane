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
// this file's job: it lets `colour_test.go` pin the contrast arithmetic against
// the ratios §6.2 and §6.3 quote, without a test in `package theme` reaching into
// the implementation and without any of it being exported from the production
// build.

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
func CoreSheet() string { return generate(brand{}) }

// Generator is the generator, for the tests that check what it writes without
// going through a manifest — a manifest cannot express every malformed state a
// generator could be broken in.
func Generator(branded bool, values map[string]Colour) string {
	tokens := make(map[string]colour, len(values))
	maps.Copy(tokens, values)

	return generate(brand{branded: branded, tokens: tokens})
}
