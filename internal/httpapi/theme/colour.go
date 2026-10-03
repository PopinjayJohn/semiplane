package theme

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// Colour: parsing a campaign's colour, and measuring what WCAG says about it.
//
// Two separate claims live here, and it is worth saying which is which:
//
//   - **The grammar is closed.** A campaign's colour is `#rrggbb` and nothing
//     else. Not `#rgb`, not `#rrggbbaa`, not `rgb(…)`, not a named colour, not
//     `var(--accent)`. The reason is not tidiness: the value is written into a
//     stylesheet the browser parses, so every additional grammar is additional
//     syntax an untrusted sync client can reach. One seven-character shape means
//     the generated declaration is a fixed literal and there is no expression
//     language anywhere near it — which is why `Manifest.Sheet` can assert that
//     every declaration it writes matches one pattern.
//   - **The measurement is WCAG's, unmodified.** Relative luminance and the
//     contrast ratio are the definitions in WCAG 2.2, to two decimals, and they
//     are what §4.12.3's floors are written against. A second, looser formula —
//     the one people reach for when a design system wants brand colours to pass —
//     would be a validator that disagrees with the gate in
//     `internal/web/tokens_contrast_test.go` about the same two colours, and one
//     of the two would be wrong.
//
// The ratios §6.2 and §6.3 quote are asserted against this implementation in
// `TestTheContrastFormulaAgreesWithTheRatiosTheRecordQuotes`. That is the whole
// defence against this file drifting away from the gate's copy of the same
// formula: two implementations of WCAG in one repository is unavoidable when one
// of them runs at request time and the other runs in a test, and the honest
// response is to pin them to each other with the record's own published numbers.

// errNotAColour is a manifest value that is not a colour.
//
// A sentinel rather than a formatted error because **the value must never be
// echoed**: it is a byte string from a campaign's content root, written by a sync
// client, and an error whose text varies with a campaign file's contents is a
// line in somebody's log aggregator holding whatever that file said. The refusal
// names the token and the rule it broke, never the bytes.
var errNotAColour = errors.New("theme: not a colour")

// hexDigits is how many digits a `#rrggbb` value carries after the hash.
//
// A named constant rather than a literal at the two use sites, because the two
// sites are the *acceptance* test and the *parse* and they have to agree; a
// length checked against one spelling and parsed against another accepts a value
// the parser then truncates.
const hexDigits = 6

// hashPrefix opens a hexadecimal colour literal.
const hashPrefix = "#"

// colour is an 8-bit sRGB triple.
//
// Eight bits per channel rather than floats because that is what the CSS
// `#rrggbb` grammar delivers and what a browser rasterises, and because the
// contrast gate measures the colour that is painted rather than a colour someone
// would compute. The name is `colour` in this package's prose for the same reason
// `tokens_contrast_test.go` spells it that way: `misspell` is configured for
// British English and objecting once per file is worse than the spelling.
type colour struct {
	red   uint8
	green uint8
	blue  uint8
}

// pageBackgrounds are the two `--bg` values, and the reason §4.12.3's second
// floor has two answers instead of one.
//
// §4.12.3 says "`--brand-accent` against `--bg` at ≥3:1" without naming a theme,
// and §4.12.4 says a campaign theme is not per-theme — one brand pair applies to
// both. So the only reading that holds in both themes is the strict one: the
// accent must clear 3:1 against **each** page, which is what makes a mid-tone
// brand colour a real failure rather than a formality. The contrast gate's own
// fixture says the same thing in prose: teal clears the dark floor "by 0.55 … so
// a brand colour even slightly lighter fails".
//
// **These two values are a copy of the palette, and that is a hazard the
// repository has an opinion about.** `tokens_contrast_test.go` exists precisely
// because a test holding its own copy of the palette cannot catch a typo in it.
// A request-time validator cannot read `tokens.css` — the binary embeds the
// *built* stylesheet, which is minified — so the values have to be here, and the
// only honest response is to pin them to the file they came from:
// `TestThePageBackgroundsAreTheOnesTheStylesheetDeclares` reads both theme
// blocks out of tokens.css and fails the build when they drift. A copy that a
// test holds against its original is a dependency; a copy nothing checks is a
// second answer.
//
// Ordered light then dark, and the order is the order the validator reports a
// failure in, so a GM reading the refusal is told which theme's page their brand
// failed on.
var pageBackgrounds = []colour{
	{0xf7, 0xf6, 0xf3}, // §6.2's "parchment"
	{0x14, 0x18, 0x1d}, // §6.3's "slate"
}

// themeNames names the two pages in a refusal message, index for index into
// `pageBackgrounds`.
//
// A message that says "the accent is 2.97:1 on the page" and not which page is a
// message a GM cannot act on, because a brand colour that works on parchment and
// fails on slate is the *expected* outcome of a mid-tone brand and the one thing
// they should be told explicitly.
var themeNames = []string{"light", "dark"}

// parseColour reads a `#rrggbb` value.
//
// The returned error is always `errNotAColour`, unwrapped and unformatted, and
// the reason is in errNotAColour's own comment: the value came out of a
// campaign's content root and must not reach a message. `strconv.ParseUint` on a
// substring is the whole parse — the grammar is closed, so there is nothing to
// interpret, and a hand-rolled hex decoder would be a place for a bug to live.
func parseColour(value string) (colour, error) {
	digits := strings.TrimPrefix(value, hashPrefix)

	if !strings.HasPrefix(value, hashPrefix) || len(digits) != hexDigits {
		return colour{}, errNotAColour
	}

	parsed := [3]uint8{}

	for channel := range parsed {
		// Two digits at a time, so each channel is one byte rather than two
		// nibbles added together.
		part := digits[channel*2 : channel*2+2]

		// `ParseUint` with base 16 is what rejects `rgb`, `0x1b4d8f`, a sign, a
		// space and every other spelling of "not a colour"; the length check
		// above has already excluded an empty part.
		value, err := strconv.ParseUint(part, 16, 8)
		if err != nil {
			return colour{}, errNotAColour
		}

		parsed[channel] = uint8(value)
	}

	return colour{red: parsed[0], green: parsed[1], blue: parsed[2]}, nil
}

// String renders the colour as the lowercase `#rrggbb` the generator writes.
//
// Lowercase unconditionally, and the reason is the validator rather than the
// browser: two manifests that differ only in the case of their hex digits
// describe the same colour, and a stylesheet whose bytes differ for a difference
// nobody can see is a stylesheet with two `ETag`s and one theme. CSS hex is
// case-insensitive, so lowercasing loses nothing.
func (c colour) String() string {
	return hashPrefix + hexByte(c.red) + hexByte(c.green) + hexByte(c.blue)
}

// hexByte renders one channel as two lowercase digits.
func hexByte(channel uint8) string {
	const digits = "0123456789abcdef"

	return string([]byte{
		digits[channel>>4],
		digits[channel&0x0f],
	})
}

// luminance is the channel's WCAG 2.2 relative luminance: 0.0 for black, 1.0 for
// white.
//
// The piecewise transfer function rather than a single power curve, because that
// is the definition: the linear segment below 0.03928 exists because the exponent
// applied to a dark channel otherwise pushes it below the 8-bit quantisation
// floor. WCAG calls this relative luminance and every ratio in §6.2, §6.3 and
// §4.12.3 is defined in terms of it.
//
// The gamma encode is the step that goes missing. Without it the value is
// linear-light and the ratios come out low by a wide margin — which would make
// this validator *stricter* than the gate and reject brand colours that are
// perfectly legible. A stricter validator is the safe direction, so the failure is
// quiet rather than loud, which is exactly why it needs pinning: the encode is
// asserted by the record's own published ratios below.
func (c colour) luminance() float64 {
	return 0.2126*linearise(c.red) + 0.7152*linearise(c.green) + 0.0722*linearise(c.blue)
}

// linearChannel is where WCAG's transfer function changes slope, as a fraction of
// full scale. The specification states it as 0.03928 and this is that number.
const linearChannel = 0.03928

// channelCeiling is 255, the top of an 8-bit channel, as a float.
const channelCeiling = 255

// linearise is the sRGB electro-optical transfer function for one channel.
//
// Named after the direction it converts (sRGB's encoded value to linear light)
// because the reverse is a different function and conflating the two is the
// classic way to get a contrast ratio subtly wrong.
func linearise(channel uint8) float64 {
	encoded := float64(channel) / channelCeiling
	if encoded <= linearChannel {
		return encoded / 12.92
	}

	return math.Pow((encoded+0.055)/1.055, 2.4)
}

// contrastRatio is the WCAG 2.2 contrast ratio between two colours: `(L1+0.05) /
// (L2+0.05)` over the lighter and the darker.
//
// The `+0.05` is the specification's, and it is what makes black and white 21:1
// rather than infinity. The order is computed rather than assumed, so a caller
// cannot produce a ratio below 1 by passing its arguments the wrong way round.
func contrastRatio(a, b colour) float64 {
	first, second := a.luminance(), b.luminance()
	if second > first {
		first, second = second, first
	}

	return (first + 0.05) / (second + 0.05)
}

// The two floors §4.12.3 states, as the numbers the validator compares against.
//
// From WCAG 2.2 rather than from the design, because they are the same floors the
// accessibility gate enforces everywhere else, and a campaign's brand is not a place
// where the standard becomes advisory:
//
//   - **4.5:1** is SC 1.4.3 Contrast (Minimum), AA. The brand ink sits *on* the
//     brand accent, so this pair is text on a background and takes the text floor.
//   - **3:1** is SC 1.4.11 Non-text Contrast, AA. The accent against the page is
//     the same shape as a control's boundary, and `tokens_contrast_test.go`
//     classifies `--brand-accent` as a boundary token for exactly this reason.
//
// Untyped float constants, so they compare against `contrastRatio`'s float64 with
// no conversion at the call site. A floor written as a fraction, or left in a
// comment, would be one more place for the number to be wrong.
const (
	brandPairFloor = 4.5
	brandPageFloor = 3.0
)
