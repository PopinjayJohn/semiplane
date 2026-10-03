package theme_test

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/httpapi/theme"
)

// The manifest: what a campaign may write, and what happens when it writes
// something else.
//
// The two directions the work item is about are `TestAPermittedOverrideLands` and
// `TestAForbiddenOverrideIsRefused`. Everything else in this file is the space
// around them: the refusals a hostile or careless manifest can hit, the two
// contrast floors, and the two properties that are easy to get wrong and hard to
// notice — that a refusal never carries the campaign's own bytes, and that which
// token a refusal names does not depend on Go's map order.

// brandAccent and brandInk are §4.12.1's two overridable names, spelled here so a
// test's manifest reads like the file a GM would write.
const (
	brandAccent = "--brand-accent"
	brandInk    = "--brand-accent-ink"
)

// The fixture pair, and why these two values.
//
// `#0e7c7b` on white measures 5.01:1 and against both pages 4.64:1 (light) and
// 3.55:1 (dark) — so it clears §4.12.3's 4.5 and 3 floors with the dark one
// cleared by 0.55, which is the narrowest margin a realistic brand has. It is
// also the fixture `tokens_contrast_test.go` already re-runs the whole contrast
// gate with, so the same pair is what the gate and this validator both hold to
// their floors, and a disagreement between them would show up as one of the two
// failing rather than as two implementations quietly differing.
const (
	fixtureAccent = "#0e7c7b"
	fixtureInk    = "#ffffff"
)

// otherAccent is a second brand that passes every floor, for the tests that need
// one campaign's brand to differ from another's. `#0b7a76` measures 5.17:1 against
// white and clears both pages (4.79 light, 3.45 dark) — a near neighbour of the
// fixture teal rather than a different family of colour, so a test that confuses
// one brand for the other is confusing two values a person would also confuse.
const otherAccent = "#0b7a76"

// manifest renders a manifest body for the brand pair.
func manifest(accent, ink string) string {
	return "tokens:\n  " + brandAccent + ": \"" + accent + "\"\n  " + brandInk + ": \"" + ink + "\"\n"
}

// TestAPermittedOverrideLands is the positive direction: §4.12.1's overridable
// column reaches the generated sheet.
//
// Asserted on the **declaration text**, not on the manifest having been accepted.
// Those are different claims, and only the first is the one a reader of a browser
// can check: a manifest that parses and is then dropped produces a valid theme
// with no brand, which every test that asserted "no error" would pass.
//
// Uppercase input is in the fixture on purpose, because the generator normalises:
// CSS hex is case-insensitive, so two manifests differing only in case describe
// one theme, and a sheet whose bytes differed would advertise two validators for it.
func TestAPermittedOverrideLands(t *testing.T) {
	t.Parallel()

	parsed, err := theme.Parse([]byte(manifest("#0E7C7B", fixtureInk)))
	if err != nil {
		t.Fatalf("Parse refused a valid brand pair: %v", err)
	}

	if !parsed.Branded() {
		t.Fatal("Parse accepted the pair but reports no theme; the manifest was " +
			"silently dropped, which is the failure a test asserting only \"no error\" passes")
	}

	for _, want := range []string{
		brandAccent + ": #0e7c7b;",
		brandInk + ": " + fixtureInk + ";",
	} {
		if !strings.Contains(parsed.Sheet(), want) {
			t.Errorf("the generated sheet does not declare %q:\n%s", want, parsed.Sheet())
		}
	}
}

// TestAForbiddenOverrideIsRefusedAndNothingElseIsApplied is the negative
// direction, and the second half of it is the half that matters.
//
// §4.12.3's failure direction is that a rejected theme degrades to the default.
// "Degrades" is doing real work in that sentence: applying the two names of the
// pair and refusing the third would produce a theme nobody wrote and no floor
// covers. So the refusal is total, and the proof is that **the permitted names in
// the same manifest are not applied either** — the returned sheet is empty rather
// than partly written.
//
// `--target-min` is the fixture because it is the sharpest case in the product: it
// is §4.12.1's 2.5.5 minimum, and a stylesheet a campaign could set it would let a
// GM shrink every target in the product for every reader.
func TestAForbiddenOverrideIsRefusedAndNothingElseIsApplied(t *testing.T) {
	t.Parallel()

	forbidden := manifest(fixtureAccent, fixtureInk) + "  --target-min: 20px\n"

	parsed, err := theme.Parse([]byte(forbidden))

	refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
	if !isRefusal {
		t.Fatalf("Parse returned %v, want a *theme.RefusalError: a campaign manifest "+
			"that names a non-overridable token must be refused (UI §4.12.2)", err)
	}

	if refusal.Token != "--target-min" {
		t.Errorf("the refusal names %q, want %q: the refusal has to name the token "+
			"the GM has to change", refusal.Token, "--target-min")
	}

	if parsed.Sheet() != "" {
		t.Errorf("Parse returned a sheet for a refused manifest:\n%s\nThe permitted "+
			"names in it must not be applied either — a partly applied theme is one "+
			"nobody wrote and no contrast floor covers", parsed.Sheet())
	}
}

// TestTheRefusalReasonDistinguishesTheContractFromTheOrdinary is what makes the
// refusal worth printing to a GM.
//
// Two product tokens, two reasons. `--focus-ring` is §4.12.1's protected column —
// the accessibility contract — and `--surface` is the product's own and simply not
// a campaign's. A single message covering both ("this token is protected") would
// be wrong for the second: `--surface` is not protected, it is just ours, and a GM
// reading that they had hit an accessibility rule would be told something false
// about their campaign.
//
// **Mutation:** making both branches return `reasonProtected` fails this test.
func TestTheRefusalReasonDistinguishesTheContractFromTheOrdinary(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"--focus-ring", "--surface"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := theme.Parse([]byte("tokens:\n  " + name + ": \"#000000\"\n"))

			refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
			if !isRefusal {
				t.Fatalf("Parse returned %v, want a *theme.RefusalError", err)
			}

			if refusal.Token != name {
				t.Errorf("the refusal names %q, want %q", refusal.Token, name)
			}
		})
	}

	contract := refusalFor(t, "--focus-ring")
	ordinary := refusalFor(t, "--surface")

	if contract == ordinary {
		t.Errorf("the refusals for --focus-ring and --surface are the same (%q); "+
			"one of them is the accessibility contract and the other is ours, and a "+
			"GM told the wrong one learns something untrue about their campaign",
			contract)
	}

	for _, name := range theme.CampaignTokens() {
		if strings.Contains(contract, name) || strings.Contains(ordinary, name) {
			t.Errorf("a refusal reason quotes the overridable token %s; the reasons are "+
				"fixed sentences and a token name belongs in the RefusalError's own "+
				"field", name)
		}
	}
}

// refusalFor is the reason a manifest naming token is refused with.
func refusalFor(t *testing.T, token string) string {
	t.Helper()

	_, err := theme.Parse([]byte("tokens:\n  " + token + ": \"#000000\"\n"))

	refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
	if !isRefusal {
		t.Fatalf("Parse(%s) returned %v, want a *theme.RefusalError", token, err)
	}

	return refusal.Reason
}

// TestAnUnknownTokenIsRefused distinguishes "not yours" from "not a thing".
//
// A campaign that misspells `--brand-accent` as `--brand-accent-colour` must be
// told the token is unknown, not that it is semiplane's own — and it must not be
// silently ignored, because a manifest whose only key is a typo validates as a
// manifest with no theme and the campaign's brand simply never appears.
func TestAnUnknownTokenIsRefused(t *testing.T) {
	t.Parallel()

	parsed, err := theme.Parse([]byte("tokens:\n  --brand-accent-colour: \"#0e7c7b\"\n"))

	refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
	if !isRefusal {
		t.Fatalf("Parse returned %v, want a *theme.RefusalError: an unknown token must "+
			"be refused rather than ignored", err)
	}

	if refusal.Token != "--brand-accent-colour" {
		t.Errorf("the refusal names %q, want the token the manifest wrote",
			refusal.Token)
	}

	if parsed.Sheet() != "" {
		t.Error("Parse returned a sheet for a refused manifest")
	}
}

// TestAnUnknownKeyIsRefused is the other strictness, and it is what makes
// `tokens:` worth its nesting level.
//
// A manifest written as `brand:` — the shorter shape a first-time author will
// reach for — must not decode to an empty document and a campaign with no brand.
// `KnownFields(true)` is what turns that into a refusal, so this test is the
// evidence for the decoder setting in Parse.
func TestAnUnknownKeyIsRefused(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"brand:\n  accent: \"#0e7c7b\"\n  ink: \"#ffffff\"\n",
		"fonts:\n  --font-ui: \"Some Face\"\n",
		"version: 1\ntokens:\n  " + brandAccent + ": \"#0e7c7b\"\n",
	} {
		_, err := theme.Parse([]byte(body))

		if !errors.Is(err, theme.ErrMalformedManifest) {
			t.Errorf("Parse(%q) returned %v, want ErrMalformedManifest", body, err)
		}
	}
}

// TestTheManifestIsCappedAtItsOwnLimit holds the cap in both directions.
//
// At the cap is not over it, and the difference matters: a cap that refuses its
// own boundary is a cap with an off-by-one, and the file it refuses is one an
// author wrote by hand and cannot easily find again.
func TestTheManifestIsCappedAtItsOwnLimit(t *testing.T) {
	t.Parallel()

	// Padded to exactly the limit with a YAML comment, which is the one way to
	// make a manifest of a precise length without inventing tokens.
	atCap := manifest(fixtureAccent, fixtureInk) + "# " +
		strings.Repeat("p", theme.MaxManifestBytes-len(manifest(fixtureAccent, fixtureInk))-2)

	if len(atCap) != theme.MaxManifestBytes {
		t.Fatalf("the fixture is %d bytes, want exactly %d", len(atCap), theme.MaxManifestBytes)
	}

	_, err := theme.Parse([]byte(atCap))
	if errors.Is(err, theme.ErrManifestTooLarge) {
		t.Errorf("Parse refused a manifest of exactly the %d-byte limit", theme.MaxManifestBytes)
	}

	_, err = theme.Parse([]byte(atCap + "p"))
	if !errors.Is(err, theme.ErrManifestTooLarge) {
		t.Errorf("Parse returned %v for a manifest one byte over the limit, want "+
			"ErrManifestTooLarge", err)
	}
}

// TestAnAliasBombIsRefused is S-4.7's half that is about the parser rather than
// about the size cap, applied to this file.
//
// The bomb is under a kilobyte, so the 16 KiB cap cannot be what stops it — which
// is the whole point: without the parser's own expansion limit this test would not
// fail its assertion, it would exhaust memory.
func TestAnAliasBombIsRefused(t *testing.T) {
	t.Parallel()

	bomb := aliasBomb(9)

	if len(bomb) > theme.MaxManifestBytes {
		t.Fatalf("the bomb is %d bytes, over the %d-byte limit, so the cap would be "+
			"what refuses it and this test would not be testing the alias limit",
			len(bomb), theme.MaxManifestBytes)
	}

	_, err := theme.Parse([]byte(bomb))
	if err == nil {
		t.Fatal("Parse accepted an alias bomb; the parser's expansion limit is either " +
			"absent or not enforced")
	}
}

// aliasBomb builds a YAML alias expansion bomb.
//
// The same construction `content.frontmatter_test.go` uses, for the same reason:
// two copies of a bomb that is *not* the same bomb is one copy that does not expand
// the way the author believed.
func aliasBomb(levels int) string {
	var out strings.Builder

	out.WriteString(`seed: &seed ["lol","lol","lol","lol","lol","lol","lol","lol","lol"]` + "\n")

	previous := "seed"

	for level := range levels {
		name := "level" + strconv.Itoa(level)

		out.WriteString(name + ": &" + name + " [*" + previous)

		for range 8 {
			out.WriteString(",*" + previous)
		}

		out.WriteString("]\n")

		previous = name
	}

	// A key an author would plausibly have written, so the test can assert that a
	// refused manifest yields nothing rather than nothing-at-all by accident.
	out.WriteString("tokens:\n  " + brandAccent + ": \"#0e7c7b\"\n")

	return out.String()
}

// TestTheBrandPairIsBothOrNeither refuses the half a manifest cannot be checked
// against a floor.
//
// §4.12.3 validates *the pair*: ink on accent at 4.5, accent on each page at 3. An
// accent with no ink is validated against the product's default `--on-accent`,
// which is white in the light theme and near-black in the dark one — so one of the
// two themes would be running a contrast pair nobody measured and the GM could not
// see. The alternative (validate against the defaults) means four more palette
// constants held in Go, for a case the record does not ask for.
//
// **Mutation:** deleting the partner loop from `document.brand` lets a lone accent
// through, and this is the only test that notices.
func TestTheBrandPairIsBothOrNeither(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"tokens:\n  " + brandAccent + ": \"" + fixtureAccent + "\"\n",
		"tokens:\n  " + brandInk + ": \"" + fixtureInk + "\"\n",
	} {
		_, err := theme.Parse([]byte(body))

		refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
		if !isRefusal {
			t.Fatalf("Parse(%q) returned %v, want a *theme.RefusalError: one half of "+
				"the pair cannot be measured against a floor", body, err)
		}

		if !strings.Contains(refusal.Reason, "both") {
			t.Errorf("the refusal reason is %q, and it should say the pair is two "+
				"tokens: a GM who set one needs to be told what to do about it",
				refusal.Reason)
		}
	}
}

// TestTheTwoContrastFloorsRefuseAPairThatMissesThem is §4.12.3's measurement,
// with one fixture per floor and each fixture shaped to miss **only** that floor.
//
// A fixture that missed both would pass a validator with one of the two checks
// deleted, which is the shape that makes these tests weak:
//
//   - `#0e7c7b` with ink `#0b1220` measures 3.73:1 on its own accent — under the
//     4.5 text floor — while still clearing both pages at 4.64 and 3.55. So this
//     fixture fails **only** the pair floor.
//   - `#0f6f6a` with white ink measures 6.00:1 on the accent, 5.55:1 on the light
//     page and 2.97:1 on the dark one. So this fixture fails **only** the dark page
//     floor, which is the case §4.12.4 exists for: one brand, two pages.
//
// The dark-only fixture is also the honest answer to "why two pages at all": it is
// a mid-tone teal that reads perfectly well on parchment and disappears on slate,
// and the refusal names the theme so the GM knows which page failed.
func TestTheTwoContrastFloorsRefuseAPairThatMissesThem(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		accent string
		ink    string
		reason string
	}{
		{
			name:   "the text floor, on the pair",
			accent: fixtureAccent,
			ink:    "#0b1220",
			reason: "ink",
		},
		{
			name:   "the page floor, in the dark theme only",
			accent: "#0f6f6a",
			ink:    fixtureInk,
			reason: "dark",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := theme.Parse([]byte(manifest(tc.accent, tc.ink)))

			refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
			if !isRefusal {
				t.Fatalf("Parse(%s on %s) returned %v, want a *theme.RefusalError",
					tc.accent, tc.ink, err)
			}

			if !strings.Contains(refusal.Reason, tc.reason) {
				t.Errorf("the refusal reason is %q and it does not mention %q; the "+
					"reason has to name which measurement failed", refusal.Reason, tc.reason)
			}

			if parsed.Sheet() != "" {
				t.Error("Parse returned a sheet for a pair that failed a contrast floor")
			}
		})
	}
}

// TestAnUnquotedColourIsRefusedWithTheReasonThatFits is the single most likely
// mistake in this file, and it gets its own answer.
//
// `--brand-accent: #0e7c7b` is what most people write. YAML reads `#` as the start
// of a comment, so the value arrives **empty**, and a validator that reported "not
// a colour" would be telling a GM that their `#0e7c7b` is not a colour — true, and
// useless. The null branch exists so the message can name the two characters they
// have to add.
//
// **Mutation:** removing the `raw == nil` branch from `claimToken` makes this a
// "not a colour" refusal and fails this test.
func TestAnUnquotedColourIsRefusedWithTheReasonThatFits(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"tokens:\n  " + brandAccent + ": #0e7c7b\n  " + brandInk + ": \"#ffffff\"\n",
		"tokens:\n  " + brandAccent + ": #fff\n  " + brandInk + ": #fff\n",
	} {
		_, err := theme.Parse([]byte(body))

		refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
		if !isRefusal {
			t.Fatalf("Parse(%q) returned %v, want a *theme.RefusalError", body, err)
		}

		if !strings.Contains(refusal.Reason, "quoted") {
			t.Errorf("the refusal reason for %q is %q, and it does not tell the GM "+
				"that a colour has to be quoted — which is the mistake they made",
				body, refusal.Reason)
		}
	}
}

// TestANonStringColourIsRefused: a value of the wrong shape is a refusal, not a
// coercion.
//
// `--brand-accent: 123456` decodes as a YAML integer and `--brand-accent:
// [0e7c7b]` as a list, and in both cases there is a plausible-looking coercion —
// format the int as hex, join the list — that would silently produce a *different*
// colour from the one the author believed they wrote. Refusing is the only answer
// that cannot be wrong in that direction.
func TestANonStringColourIsRefused(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"123456", "[\"#0e7c7b\"]", "0x0e7c7b", "true"} {
		body := "tokens:\n  " + brandAccent + ": " + value + "\n"

		_, err := theme.Parse([]byte(body))

		refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
		if !isRefusal {
			t.Errorf("Parse(%s) returned %v, want a *theme.RefusalError: a value of the "+
				"wrong shape must be refused rather than coerced", value, err)
		}
	}
}

// TestARefusalNeverEchoesTheManifest is S-12.3, applied to this route's input.
//
// The manifest is a campaign file written by a sync client, and a refusal reaches
// a log line. So a value — or a token *name* — that is not one of the vocabulary's
// own spellings must not appear in the error, and a name that is not even a
// well-formed custom property must not appear at all.
//
// The name half is the one that is easy to get wrong: a YAML key is an arbitrary
// byte string, so `tokenName`'s grammar is a log-safety boundary and not only a
// tidiness rule. Without it, a key carrying a newline writes a second line into
// somebody's log aggregator.
//
// **Mutation:** making `refused()` fall back to echoing the raw name and value
// fails this test.
func TestARefusalNeverEchoesTheManifest(t *testing.T) {
	t.Parallel()

	// A key with a newline, a quote and a colon in it, and a value that is a
	// colour-shaped attempt at a CSS escape. Neither may appear in the error.
	hostile := "tokens:\n" +
		"  \"--brand-accent\\nFatal: injected\": \"\\\\000e7c7b\"\n"

	_, err := theme.Parse([]byte(hostile))

	refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
	if !isRefusal {
		t.Fatalf("Parse returned %v, want a *theme.RefusalError", err)
	}

	if refusal.Token != "" {
		t.Errorf("the refusal carries the token %q, but that name is not a "+
			"well-formed custom property and must not reach a log line", refusal.Token)
	}

	for _, forbidden := range []string{"Fatal", "injected", "000e7c7b", "\\"} {
		if strings.Contains(refusal.Error(), forbidden) {
			t.Errorf("the refusal text %q contains %q, which came from the "+
				"campaign's file: a refusal is a log line and a log line is not a "+
				"channel for a sync client", refusal.Error(), forbidden)
		}
	}
}

// TestTheSameManifestAlwaysProducesTheSameSheet is determinism, and it is
// asserted on the bytes rather than on the parse.
//
// Two claims in one test, because the same map iteration would break both: the
// generated sheet must not reorder itself between runs (its `ETag` is a hash of
// those bytes), and a refusal must not name a different token each time (two log
// lines about the same manifest would read as two manifests).
func TestTheSameManifestAlwaysProducesTheSameSheet(t *testing.T) {
	t.Parallel()

	// The same pair, written in both orders. YAML mappings are unordered, so
	// these two documents are the same manifest.
	first, err := theme.Parse([]byte(manifest(fixtureAccent, fixtureInk)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	permuted, err := theme.Parse([]byte("tokens:\n  " + brandInk + ": \"" + fixtureInk +
		"\"\n  " + brandAccent + ": \"" + fixtureAccent + "\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if first.Sheet() != permuted.Sheet() {
		t.Errorf("two orderings of the same manifest produced different sheets:\n%s\n---\n%s",
			first.Sheet(), permuted.Sheet())
	}

	// A manifest with two forbidden names, refused twenty times. Whichever it
	// names, it names the same one every time.
	twoBad := manifest(fixtureAccent, fixtureInk) + "  --dur-base: 1s\n  --radius-md: 9px\n"

	for attempt := range 20 {
		_, err := theme.Parse([]byte(twoBad))

		refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
		if !isRefusal {
			t.Fatalf("Parse returned %v, want a *theme.RefusalError", err)
		}

		if refusal.Token != "--dur-base" {
			t.Fatalf("attempt %d was refused as %q, want %q: the refusal has to name "+
				"the same token every time or two log lines read as two manifests",
				attempt, refusal.Token, "--dur-base")
		}
	}
}

// TestAnEmptyManifestIsNotATheme covers the two documents that decode to nothing.
//
// Both are states a GM produces by accident — a file they created and have not
// filled in, and one they wrote the explanation into — and both must serve the
// core theme rather than fail. The route reads the sheet either way, and a route
// that 500s for an empty file would be a route a GM cannot recover from by typing.
//
// The other half matters just as much: `Sheet()` is still non-empty, because the
// bytes served for "no theme" have to be a valid stylesheet — that is what lets the
// route answer every request with 200 and no `if`.
func TestAnEmptyManifestIsNotATheme(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"", "# the colours go here\n", "tokens:\n"} {
		parsed, err := theme.Parse([]byte(body))
		if err != nil {
			t.Errorf("Parse(%q) returned %v, want no error: an empty manifest is a "+
				"campaign with no theme, not a fault", body, err)
		}

		if parsed.Branded() {
			t.Errorf("Parse(%q) reports a theme; an empty manifest describes none", body)
		}

		if parsed.Sheet() != theme.CoreSheet() {
			t.Errorf("Parse(%q) served %q, want the core sheet", body, parsed.Sheet())
		}

		if parsed.Sheet() == "" {
			t.Errorf("Parse(%q) served an empty body; the response has to be a valid "+
				"stylesheet whether or not it carries declarations", body)
		}
	}
}

// TestTheColourGrammarIsSevenCharactersAndNothingElse pins the closed grammar, on
// the parser rather than through a manifest — a manifest that is refused for an
// unknown *token* never reaches the colour parser at all, so most of these cases
// would otherwise be unreachable.
//
// The subset is deliberate and it loses nothing: §6.2 and §6.3 are hex throughout,
// and a grammar that accepted `var(--accent)` or `rgba()` would be a second way for
// a campaign to reach a value the validator did not measure.
//
// Uppercase is accepted because CSS hex is case-insensitive and a GM will type
// `#0E7C7B`; it is normalised on output, which is what `TestAPermittedOverrideLands`
// asserts.
func TestTheColourGrammarIsSevenCharactersAndNothingElse(t *testing.T) {
	t.Parallel()

	accepted := []string{"#0e7c7b", "#0E7C7B", "#000000", "#ffffff", "#010203"}
	rejected := []string{
		"", "#", "#fff", "#ffff", "#0e7c7", "#0e7c7bb", "#0e7c7bbb",
		"0e7c7b", " #0e7c7b", "#0e7c7b ", "0x0e7c7b", "#0E7C7G",
		"rgb(14,124,123)", "var(--accent)", "teal", "#0e7c7b; --bg: red",
	}

	for _, value := range accepted {
		if _, err := theme.ParseColour(value); err != nil {
			t.Errorf("ParseColour(%q) refused it: %v", value, err)
		}
	}

	for _, value := range rejected {
		if _, err := theme.ParseColour(value); err == nil {
			t.Errorf("ParseColour(%q) accepted it; the grammar is #rrggbb and "+
				"nothing else", value)
		}
	}
}

// TestTheContrastFormulaAgreesWithTheRatiosTheRecordQuotes is what holds this
// package's copy of WCAG's arithmetic to the gate's copy.
//
// There are two implementations of the contrast ratio in this repository and there
// have to be: `tokens_contrast_test.go` measures the stylesheet in a test, and this
// one measures a campaign's colour at request time. Two implementations of one
// formula is a hazard, and the answer is not one implementation — it is to pin
// them to numbers that are not either of them.
//
// Every ratio below is quoted by §6.2, §6.3 or §6.4, including the one the record
// uses to *argue*: §6.4's worked example, that the light theme's `#1B4D8F` on the
// dark theme's `#14181D` measures 2.13:1 and therefore fails as a fill. If this
// formula loses the gamma encode, or a channel weight, these numbers move and the
// build fails — and a formula that is *too strict* is the quiet version of the
// same fault, because it rejects legible brand colours and looks like judgement.
//
// **Mutation:** dropping `linearise`'s power branch (returning the encoded channel
// unchanged) fails this test on every row.
func TestTheContrastFormulaAgreesWithTheRatiosTheRecordQuotes(t *testing.T) {
	t.Parallel()

	const tolerance = 0.005

	rows := []struct {
		foreground string
		background string
		quoted     float64
		from       string
	}{
		{"#1b4d8f", "#14181d", 2.13, "§6.4's worked example: the light fill on the dark page"},
		{"#5b9be8", "#14181d", 6.20, "§6.3: --accent-solid on --bg"},
		{"#0b1220", "#5b9be8", 6.51, "§6.3: --on-accent on --accent-solid"},
		{"#4a443c", "#efede8", 8.22, "§6.2: --callout-secret on --callout-surface"},
		{"#8a8377", "#efede8", 3.21, "§6.2: --callout-border on --callout-surface"},
		{"#5f594f", "#f7f6f3", 6.42, "§6.2: --text-subtle on --bg"},
		{"#ffffff", "#1b4d8f", 8.38, "§6.2: --on-accent on --accent-solid"},
	}

	for _, row := range rows {
		got := theme.ContrastRatio(mustColour(t, row.foreground), mustColour(t, row.background))
		if math.Abs(got-row.quoted) > tolerance {
			t.Errorf("contrast(%s, %s) = %.4f, want about %.2f — %s. This formula is "+
				"the one §4.12.3's floors are measured with, and the gate in "+
				"internal/web has its own copy; if the two disagree, one campaign "+
				"brand colour is accepted or refused wrongly.",
				row.foreground, row.background, got, row.quoted, row.from)
		}
	}
}

// TestThePageBackgroundsAreTheOnesTheStylesheetDeclares holds the one copy of the
// palette this package has to own.
//
// §4.12.3 measures the brand accent against `--bg`, the request-time validator
// cannot read `tokens.css` (the binary embeds the *built* stylesheet, which is
// minified), so the two page colours are constants here — and a copy nothing checks
// is the second answer `tokens_contrast_test.go` exists to prevent. This is the
// check on it: both theme blocks are read out of the real file and compared.
//
// A GM changing `--bg` gets a build failure naming the theme, rather than a brand
// validator that has quietly stopped measuring the page its readers see.
//
// **Mutation:** changing `--bg` in the light block of tokens.css fails this test.
func TestThePageBackgroundsAreTheOnesTheStylesheetDeclares(t *testing.T) {
	t.Parallel()

	backgrounds := theme.PageBackgrounds()

	for index, themeName := range []string{"light", "dark"} {
		if index >= len(backgrounds) {
			t.Fatalf("PageBackgrounds() returned %d pages, want one per theme",
				len(backgrounds))
		}

		declared, ok := tokensInThemeBlock(t, themeName)["--bg"]
		if !ok {
			t.Fatalf("the %s theme declares no --bg; the walk is broken, and a walk "+
				"that finds nothing passes this test for the wrong reason", themeName)
		}

		if got := backgrounds[index].String(); got != declared {
			t.Errorf("PageBackgrounds()[%d] is %s, and tokens.css's %s theme declares "+
				"--bg as %s. This package measures §4.12.3's page floor against its "+
				"own copy, so a drift here means brand colours are validated against "+
				"a page no reader sees.",
				index, got, themeName, declared)
		}
	}
}

// mustColour parses a fixture colour, failing the test if it does not parse.
func mustColour(t *testing.T, value string) theme.Colour {
	t.Helper()

	parsed, err := theme.ParseColour(value)
	if err != nil {
		t.Fatalf("ParseColour(%q): %v", value, err)
	}

	return parsed
}
