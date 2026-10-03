package theme_test

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semiplane/semiplane/internal/content"
	"github.com/semiplane/semiplane/internal/httpapi/theme"
)

// Fonts and the header image: §4.12.1's font row and §4.12.2's two `url()` paths.
//
// These are the tests for `font.go`, and what they share is the thing that file
// argues: every value here left an attacker-reachable manifest and is on its way
// into a stylesheet a browser will act on. So each test reads the **emitted sheet**
// rather than a return value, because the sheet is the artefact — a resolver that
// returned the right string and a generator that wrote the wrong one are the same
// failure to a reader, and only one of them is visible from here.
//
// The roots are real `os.Root`s, built by `newHarness`, so "confined" is the same
// boundary the route uses rather than a fixture that agrees with itself.

// rootFor returns the test campaign's content root, failing the test if it is not
// there.
func rootFor(t *testing.T, served *harness) *content.Root {
	t.Helper()

	root, err := served.registry.Get(testSlug)
	if err != nil {
		t.Fatalf("get the content root for %s: %v", testSlug, err)
	}

	return root
}

// fontManifest is a manifest whose only content is one font slot.
//
// No `tokens:` at all, which is the case `document.theme` treats as its own: a
// campaign that has chosen to change its typeface and not its colours is a
// complete theme, not a half-written one.
func fontManifest(slot, family, src string) string {
	return "fonts:\n  " + slot + ":\n    family: \"" + family + "\"\n    src: " + src + "\n"
}

// writeFile puts a file inside a harness's campaign root, at a root-relative path.
func writeFile(t *testing.T, served *harness, rel string) {
	t.Helper()

	full := filepath.Join(append([]string{served.dirs[testSlug]}, strings.Split(rel, "/")...)...)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("make the directory for %s: %v", rel, err)
	}

	if err := os.WriteFile(full, []byte("not really a font"), 0o600); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestAFaceLandsInTheTokenItsOwnSlotNames is the bug `faceFor`'s header records:
// a face keyed on its position in a list rather than on its own slot.
//
// The two slots are independently optional, so a manifest naming only `ui` is not
// a hypothetical — it is a GM who wants the chrome in their typeface and the prose
// in the product's. With a positional lookup that manifest's face is at index 0
// and lands in `--font-prose`, and the failure is a campaign whose *prose* changed
// type while their interface did not, with nothing in any log to say so.
//
// **Mutation:** replacing `face.token()` at the point the generator writes the
// declaration with the *loop's* slot fails this test on whichever row the
// manifest did not name — the face is right and the token is wrong, which is the
// half a happy-path fixture never reaches. `TestTwoFacesLandInTheirOwnSlots` is
// the other direction: `faceFor` ignoring `face.slot` gives both slots the prose
// face, and only a manifest naming both can see it.
func TestAFaceLandsInTheTokenItsOwnSlotNames(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		slot       string
		family     string
		file       string
		wantToken  string
		otherToken string
	}{
		"the ui slot only": {
			slot: "ui", family: "Campaign Sans", file: "fonts/sans.woff2",
			wantToken: "--font-ui", otherToken: "--font-prose",
		},
		"the prose slot only": {
			slot: "prose", family: "Campaign Serif", file: "fonts/serif.woff2",
			wantToken: "--font-prose", otherToken: "--font-ui",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			served := newHarness(t)
			writeFile(t, served, testCase.file)

			parsed, err := theme.Parse(
				[]byte(fontManifest(testCase.slot, testCase.family, testCase.file)),
				rootFor(t, served), testSlug,
			)
			if err != nil {
				t.Fatalf("Parse refused a one-slot font: %v", err)
			}

			sheet := parsed.Sheet()

			want := testCase.wantToken + ": \"" + testCase.family + "\", " +
				theme.SystemStacks()[testCase.slot] + ";"
			if !strings.Contains(sheet, want) {
				t.Errorf("the sheet does not declare %q:\n%s", want, sheet)
			}

			if strings.Contains(sheet, testCase.otherToken+":") {
				t.Errorf("the sheet declares %s for a manifest that named only "+
					"%s:\n%s\nThe face would land in the other slot's token, and the "+
					"campaign would see the wrong half of its own typeface",
					testCase.otherToken, testCase.slot, sheet)
			}
		})
	}
}

// TestTwoFacesLandInTheirOwnSlots is the direction a one-slot manifest cannot
// reach.
//
// `faceFor` used to walk `fontSlots` and `faces` in step, and the row above is
// the test that caught it — but a manifest naming only one slot is also the row
// where a lookup that ignores `face.slot` still writes a *correct* token, because
// the token is `face.token()` and there is only one face. What a lookup that
// ignores the slot produces with both declared is a `--font-ui` carrying the
// prose family, which is a campaign whose headings changed type while their
// interface did not, and it is invisible from a single-slot fixture.
//
// Each token is also required **once**: a lookup returning the wrong face for a
// slot writes that face's token twice — the same declaration, so nothing looks
// broken to a reader, and nothing looks broken to the assertions above either.
//
// **Mutation:** replacing `face.slot == slot` in `faceFor` with a plain
// `return faces[0]` fails this test on the repeated-declaration assertion: both
// loop iterations then return the prose face, so `--font-prose` is written twice
// and `--font-ui` is not written at all. Dropping one of the two files fails it
// on the missing token rather than on a refusal, because a font that will not
// resolve is `Parse`'s answer and this test wants the generator's.
func TestTwoFacesLandInTheirOwnSlots(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	writeFile(t, served, "fonts/serif.woff2")
	writeFile(t, served, "fonts/sans.woff2")

	document := "fonts:\n" +
		"  prose:\n    family: \"Campaign Serif\"\n    src: fonts/serif.woff2\n" +
		"  ui:\n    family: \"Campaign Sans\"\n    src: fonts/sans.woff2\n"

	parsed, err := theme.Parse([]byte(document), rootFor(t, served), testSlug)
	if err != nil {
		t.Fatalf("Parse refused a manifest naming both slots: %v", err)
	}

	sheet := parsed.Sheet()

	for slot, family := range map[string]string{
		"prose": "Campaign Serif",
		"ui":    "Campaign Sans",
	} {
		want := "--font-" + slot + ": \"" + family + "\", " +
			theme.SystemStacks()[slot] + ";"

		if !strings.Contains(sheet, want) {
			t.Errorf("the sheet does not declare %q:\n%s", want, sheet)
		}

		if occurrences := strings.Count(sheet, "--font-"+slot+":"); occurrences != 1 {
			t.Errorf("the sheet declares --font-%s %d times, want 1:\n%s\nA "+
				"repeated declaration is a lookup that matched the wrong face, and "+
				"the second copy is the one a reader never sees", slot, occurrences,
				sheet)
		}
	}

	// The claim itself: neither family in the other's slot.
	for slot, wrong := range map[string]string{
		"prose": "Campaign Sans",
		"ui":    "Campaign Serif",
	} {
		if strings.Contains(sheet, "--font-"+slot+": \""+wrong+"\"") {
			t.Errorf("the sheet puts %q in --font-%s:\n%s\nThe two faces swapped, "+
				"which is a slot-keyed lookup that is not keyed on the slot (§4.12.1)",
				wrong, slot, sheet)
		}
	}
}

// TestTheAssetURLPercentEncodesWhatWouldEndTheURLEarly is the `)` problem, and the
// filename is a legal one.
//
// A bare `url(…)` token ends at the first `)`, so `fonts/a)background.woff2`
// would emit a `src` that closed early and left `background.woff2)` as the start
// of a declaration nobody wrote. Quoting is the first half of the answer and
// percent-encoding is the second: the quote moves the terminator to a character
// the encoder has already escaped, so neither half alone is the property.
//
// The assertion is over the **emitted sheet**, not over `EncodePath`, because the
// claim is about what a browser is handed. The decode round-trip is what keeps it
// from being vacuous: an encoder that dropped the character entirely would pass a
// "no raw `)`" check and serve a `src` that names a file which does not exist.
//
// **Mutation:** replacing `url.PathEscape(segment)` with `segment` fails the
// character assertions; replacing it with `url.QueryEscape` fails the round-trip
// (`+` for a space is wrong in a path).
func TestTheAssetURLPercentEncodesWhatWouldEndTheURLEarly(t *testing.T) {
	t.Parallel()

	const rel = "fonts/a) background;url(x.woff2"

	served := newHarness(t)
	writeFile(t, served, rel)

	parsed, err := theme.Parse(
		[]byte(fontManifest("ui", "Campaign Sans", rel)),
		rootFor(t, served), testSlug,
	)
	if err != nil {
		t.Fatalf("Parse refused a confined path: %v", err)
	}

	sheet := parsed.Sheet()

	if !strings.Contains(sheet, `src: url("`) {
		t.Fatalf("the sheet carries no url() src:\n%s", sheet)
	}

	quoted, ok := urlToken(sheet)
	if !ok {
		t.Fatalf("the sheet's url() could not be read:\n%s", sheet)
	}

	for _, forbidden := range []string{")", " ", ";", "\"", "\\", "\n"} {
		if strings.Contains(quoted, forbidden) {
			t.Errorf("the emitted src %q carries a raw %q, which ends the url() "+
				"token or the declaration it sits in:\n%s", quoted, forbidden, sheet)
		}
	}

	if !strings.HasPrefix(quoted, "/c/"+testSlug+"/assets/") {
		t.Errorf("the emitted src is %q, want the absolute /c/%s/assets/ prefix: a "+
			"relative URL resolves against wherever the browser thinks the document "+
			"is", quoted, testSlug)
	}

	// The round-trip: what the browser decodes has to be the confined path, or
	// the encoding is a transformation the assets route cannot undo.
	decoded, err := url.PathUnescape(strings.TrimPrefix(quoted, "/c/"+testSlug+"/assets/"))
	if err != nil {
		t.Fatalf("the emitted src does not decode: %v", err)
	}

	if decoded != rel {
		t.Errorf("the emitted src decodes to %q, want %q: an encoder that dropped "+
			"a character instead of escaping it serves a src naming a file that "+
			"does not exist", decoded, rel)
	}

	// And the pinned output for the two characters the argument is about, so a
	// change of encoder is a visible diff rather than a behaviour change.
	if got := theme.EncodePath("a)b.png"); got != "a%29b.png" {
		t.Errorf("EncodePath(%q) = %q, want %q", "a)b.png", got, "a%29b.png")
	}

	if got := theme.EncodePath("a b.png"); got != "a%20b.png" {
		t.Errorf("EncodePath(%q) = %q, want %q: a space must be percent-encoded "+
			"and not written as a `+`, which means something else in a query and "+
			"nothing in a path", "a b.png", got, "a%20b.png")
	}
}

// urlToken reads the value of the sheet's first `url("…")`.
func urlToken(sheet string) (string, bool) {
	_, after, quoted := strings.Cut(sheet, `url("`)
	if !quoted {
		return "", false
	}

	value, _, closed := strings.Cut(after, `")`)
	if !closed {
		return "", false
	}

	return value, true
}

// TestTheGeneratedSheetQuotesEveryURLItEmits is the quoting half, checked against
// the generator rather than against the encoder.
//
// `TestTheAssetURLPercentEncodesWhatWouldEndTheURLEarly` proves the encoder never
// hands over a `)` — this one asks what the generator would do with one anyway,
// because the two are separate steps and only one of them is under test there.
// A bare `url(a)b.png)` is not a hypothetical shape: it is what the first draft of
// `urlValue` emitted, and it parses as a url of `a` followed by junk the browser
// drops on the floor.
//
// The `"` case is the deeper one. Quoting is what makes a `)` safe, so a `"`
// inside the quotes defeats it entirely — everything after it becomes a
// declaration. `encodePath` percent-encodes it before a URL can arrive, so the
// branch is unreachable from a manifest; it is still replaced on the way out,
// which is what this test holds.
//
// **Mutation:** making `urlValue` emit `url(` + url + `)` fails both cases; making
// it drop the replacement fails the second.
func TestTheGeneratedSheetQuotesEveryURLItEmits(t *testing.T) {
	t.Parallel()

	brand, err := theme.ParseColour(fixtureAccent)
	if err != nil {
		t.Fatalf("ParseColour: %v", err)
	}

	for name, testCase := range map[string]struct {
		image string
		want  string
	}{
		"a parenthesis would end a bare url": {
			image: `/c/greyhaven/assets/art/a)b.png`,
			want:  `url("/c/greyhaven/assets/art/a)b.png")`,
		},
		"a quote would end the quoted one": {
			image: `/c/greyhaven/assets/art/a"b.png`,
			want:  `url("/c/greyhaven/assets/art/a%22b.png")`,
		},
		"a backslash would start an escape": {
			image: `/c/greyhaven/assets/art/a\b.png`,
			want:  `url("/c/greyhaven/assets/art/a%5Cb.png")`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sheet := theme.Generator(
				map[string]theme.Colour{brandAccent: brand}, testCase.image, nil,
			)

			if !strings.Contains(sheet, testCase.want) {
				t.Errorf("the sheet does not carry %q:\n%s", testCase.want, sheet)
			}

			// The structural claim, over every url() in the sheet rather than over
			// the one the fixture named: each is a quoted string that contains no
			// quote, so the declaration ends where the generator meant it to.
			for _, token := range urlTokens(sheet) {
				if strings.ContainsAny(token, "\"\\") {
					t.Errorf("the url() value %q carries a quote or a backslash, "+
						"which ends the string it is in:\n%s", token, sheet)
				}
			}
		})
	}
}

// urlTokens is every value inside a `url("…")` in sheet.
func urlTokens(sheet string) []string {
	var found []string

	rest := sheet
	for {
		at := strings.Index(rest, `url("`)
		if at < 0 {
			return found
		}

		rest = rest[at+len(`url("`):]

		end := strings.Index(rest, `")`)
		if end < 0 {
			return found
		}

		found = append(found, rest[:end])
		rest = rest[end+2:]
	}
}

// TestTheSystemFontStacksAreTheOnesTheStylesheetDeclares is the copy, held against
// its original.
//
// `font.go`'s `systemStacks` is a literal transcription of shell.css's two
// declarations, and a request-time generator cannot read a stylesheet — so the
// values have to live in Go, and the only honest response to a copy is a test that
// fails when it drifts. Drift here is not cosmetic: the stack is the *tail* of
// every generated `--font-prose`, so a campaign whose webfont 404s falls back to
// whatever this map says, and a stale entry means falling back to a typeface the
// product stopped using three releases ago with no error anywhere.
//
// **Mutation:** deleting `Arial, ` from `systemStacks`'s ui entry fails this test
// against the untouched shell.css.
func TestTheSystemFontStacksAreTheOnesTheStylesheetDeclares(t *testing.T) {
	t.Parallel()

	sheet := stripComments(readSheet(t, "shell.css"))

	stacks := theme.SystemStacks()
	if len(stacks) == 0 {
		t.Fatal("SystemStacks() is empty; every assertion below passes for the " +
			"wrong reason")
	}

	for slot, stack := range stacks {
		token := "--font-" + slot

		declared := declarationValues(sheet, token)
		if len(declared) == 0 {
			t.Errorf("shell.css declares no %s, so there is no original for the "+
				"copy in font.go to agree with", token)

			continue
		}

		matches := false
		for _, value := range declared {
			if value == stack {
				matches = true
			}
		}

		if !matches {
			t.Errorf("font.go's %s stack is\n    %s\nand shell.css declares\n%s\n"+
				"The generator writes its copy as the tail of every generated "+
				"declaration, so a campaign whose webfont fails would fall back to a "+
				"stack the product no longer uses", slot, stack,
				indentAll(declared))
		}
	}
}

// declarationValues every value `name` is declared with in a stylesheet.
func declarationValues(sheet, name string) []string {
	var values []string

	for _, match := range valuePattern.FindAllStringSubmatch(sheet, -1) {
		if match[1] == name {
			values = append(values, strings.TrimSpace(match[2]))
		}
	}

	return values
}

// indentAll renders candidate values for a failure message.
func indentAll(values []string) string {
	var out strings.Builder

	for _, value := range values {
		out.WriteString("    ")
		out.WriteString(value)
		out.WriteString("\n")
	}

	return out.String()
}

// TestAHeaderImageLandsAsAQuarantinedURL is the positive direction for §4.12.1's
// third overridable name, and it is the assertion that keeps the whole image path
// from being written and never reached.
//
// The manifest names a **path**, the sheet carries a **URL**, and the two differ
// by exactly one thing: the `/c/{slug}/assets/` prefix a server built after
// `os.Root` had already confined the path. A manifest that named a URL would be a
// manifest naming a host, and `headerImage`'s grammar refuses one before any
// resolution happens.
//
// The second half matters as much as the first: a manifest naming only the image
// must leave the brand *pair* alone. The pair is measured together and this
// document contains no pair, so writing `--brand-accent` from an empty colour map
// would be the zero colour reaching every campaign that themed an image.
//
// **Mutation:** deleting the `if name == brandImage { continue }` skip in
// `document.theme` fails this test with "a colour is written #rrggbb", which is
// the bug the skip exists for.
func TestAHeaderImageLandsAsAQuarantinedURL(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	writeFile(t, served, "art/banner.png")

	parsed, err := theme.Parse(
		[]byte("tokens:\n  --brand-header-image: \"art/banner.png\"\n"),
		rootFor(t, served), testSlug,
	)
	if err != nil {
		t.Fatalf("Parse refused a confined header image: %v", err)
	}

	if !parsed.Branded() {
		t.Fatal("Parse accepted the image but reports no theme; the manifest was " +
			"silently dropped")
	}

	sheet := parsed.Sheet()

	want := brandImage + `: url("/c/` + testSlug + `/assets/art/banner.png");`
	if !strings.Contains(sheet, want) {
		t.Errorf("the sheet does not declare %q:\n%s", want, sheet)
	}

	if strings.Contains(sheet, brandAccent+":") {
		t.Errorf("an image-only manifest wrote a brand colour:\n%s\nThe pair is "+
			"measured together and this document names neither half, so the zero "+
			"colour would reach the campaign", sheet)
	}
}

// TestAnImageThatIsNotAFileInTheCampaignIsRefused holds the two refusals the image
// can hit, and both are answers rather than faults.
//
// An empty value is the case `headerImage` singles out: a GM writing
// `--brand-header-image: ""` means "none", and honouring it would emit a URL for a
// file nobody named — while the product's own `none` is one key-stroke away in
// deleting the line. An extension outside the closed table is the case where the
// path resolves and the *format* is what is refused: `.svg` is excluded on its own
// merits, because a background image is the one place a campaign's file would be
// parsed as markup rather than drawn.
//
// **Mutation:** returning `"", nil` from `headerImage` for an empty value fails
// the first row; widening `imageMedia` to include `.svg` fails the second.
func TestAnImageThatIsNotAFileInTheCampaignIsRefused(t *testing.T) {
	t.Parallel()

	served := newHarness(t)
	writeFile(t, served, "art/banner.svg")

	for name, testCase := range map[string]struct {
		value  string
		reason string
	}{
		"an empty path": {value: "", reason: "empty"},
		"a format the header never draws": {
			value: "art/banner.svg", reason: ".png, .jpg",
		},
		"a path that is not there": {
			value: "art/missing.png", reason: ".png, .jpg",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := "tokens:\n  " + brandImage + ": \"" + testCase.value + "\"\n"

			parsed, err := theme.Parse([]byte(body), rootFor(t, served), testSlug)

			refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
			if !isRefusal {
				t.Fatalf("Parse returned %v, want a *theme.RefusalError", err)
			}

			if refusal.Token != brandImage {
				t.Errorf("the refusal names %q, want %q", refusal.Token, brandImage)
			}

			if !strings.Contains(refusal.Reason, testCase.reason) {
				t.Errorf("the reason is %q, want it to mention %q", refusal.Reason,
					testCase.reason)
			}

			if parsed.Sheet() != "" {
				t.Errorf("Parse returned a sheet for a refused image:\n%s",
					parsed.Sheet())
			}
		})
	}
}

// TestAPathOutsideTheCampaignRootIsRefused is the confinement failure direction,
// for both of §4.12.2's paths.
//
// `os.Root` is the authority (S-3.5), and the refusal is the same answer for a
// font and for the header image because it is the same boundary: a manifest naming
// `../other/logo.png` is not a typo to be resolved, it is a path this route will
// not name. Two things are asserted, and the second is the one that would be easy
// to lose: the sheet the campaign keeps serving carries **no** part of the refused
// document — a refusal that still wrote the font through would be a confinement
// check on the wrong side of the generator.
//
// **Mutation:** swapping `root.At(rel)` for a `filepath.Join` of the campaign's
// directory in `assetURL` fails this test only if the joined path exists, which is
// why the fixture puts a real file outside the root for it to find.
func TestAPathOutsideTheCampaignRootIsRefused(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		manifest   string
		outside    string
		wantToken  string
		wantInPath string
	}{
		"a font src": {
			manifest:   fontManifest("ui", "Campaign Sans", "../outside/sans.woff2"),
			outside:    "outside/sans.woff2",
			wantToken:  "fonts:",
			wantInPath: "sans.woff2",
		},
		"a header image": {
			manifest:   "tokens:\n  --brand-header-image: \"../outside/banner.png\"\n",
			outside:    "outside/banner.png",
			wantToken:  "--brand-header-image",
			wantInPath: "banner.png",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			served := newHarness(t)

			// A real file one level above the campaign root, so a resolver that
			// joined rather than confined has something to find.
			neighbour := filepath.Dir(served.dirs[testSlug])
			outside := filepath.Join(neighbour, strings.Split(testCase.outside, "/")[0])
			if err := os.MkdirAll(outside, 0o700); err != nil {
				t.Fatalf("make the neighbouring directory: %v", err)
			}

			if err := os.WriteFile(filepath.Join(neighbour, testCase.outside),
				[]byte("bytes"), 0o600); err != nil {
				t.Fatalf("write the outside file: %v", err)
			}

			parsed, err := theme.Parse(
				[]byte(testCase.manifest), rootFor(t, served), testSlug,
			)

			refusal, isRefusal := errors.AsType[*theme.RefusalError](err)
			if !isRefusal {
				t.Fatalf("Parse returned %v, want a *theme.RefusalError: a path "+
					"outside the campaign's own root is a refusal, not a resolution",
					err)
			}

			if parsed.Sheet() != "" {
				t.Errorf("Parse returned a sheet for a manifest whose path escaped "+
					"the root:\n%s", parsed.Sheet())
			}

			if refusal.Token != testCase.wantToken {
				t.Errorf("the refusal names %q, want %q: the notice has to point a GM "+
					"at the section they wrote", refusal.Token, testCase.wantToken)
			}

			// The refusal is a sentence this package wrote, and a path is not in
			// it — S-12.3 again, one level down: the manifest's own bytes never
			// reach a log line or a notice, not even a path in one.
			if strings.Contains(refusal.Error(), testCase.wantInPath) {
				t.Errorf("the refusal echoes the manifest's own path (%q): %q",
					testCase.wantInPath, refusal.Error())
			}
		})
	}
}
