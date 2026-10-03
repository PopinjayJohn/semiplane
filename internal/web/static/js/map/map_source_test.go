// The map client's source audits, and the vendor pin.
//
// # Why audits over source at all
//
// The camera's arithmetic is *evaluated* (`camera_arith_test.go`), but nothing else
// in this tree can be evaluated without a browser, and this repository has none in
// CI by design. So the remaining claims are made against the **shipped bytes**: not
// a copy, not a summary, the files a browser would fetch.
//
// Two properties make these more than greps.
//
//  1. **Comments are removed first.** A colour or a banned word inside a comment
//     explaining why it is banned is prose, and an audit that reads raw text reports
//     it as a violation — a loud and *wrong* failure, which is worse than none
//     because the next real one gets ignored. The scanner blanks comments to
//     spaces, preserving offsets so line numbers still point at the right place.
//  2. **Every audit is paired with a mutation.** `TestEveryMapSourceAuditRejectsIts
//     Violation` applies each violation to a copy of the source in memory and
//     requires the audit to object. An audit nobody can fail is not an audit, and
//     three of phase 5's first gate tests did not fail.
//
// # What these audits cannot do
//
// They read text. They can prove that `scene.js` asks for a 3px outline and that no
// colour literal exists; they cannot prove what a WebGL context did with the result.
// UI §3.7's §10.3–§10.7 sweeps remain agent-assisted for that reason, and every
// finding from them becomes a committed test here rather than a comment.

package mapjs_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The map client's source files, relative to this test's directory.
var mapSources = []string{"camera.js", "palette.js", "scene.js", "table.js"}

// The vendor tree and its manifest, relative to this test's directory.
//
// Five levels up is the repository root: `map` → `js` → `static` → `web` →
// `internal` → root. The vendor tree and its manifest hang off that root rather than
// off this directory, because those two paths are the contract and putting a copy of
// the manifest beside the assets it describes would be a second answer.
const (
	vendorTree     = "../../../../../internal/web/static/vendor"
	vendorManifest = "../../../../../tools/vendor.json"
	repositoryDir  = "../../../../.."
)

// --- Comment-stripping ---------------------------------------------------------

// blankComments replaces every comment in a JavaScript source with spaces, keeping
// offsets so a diagnostic can still name a line.
//
// A block comment ends at the first `*/` and a line comment at the first newline.
// Neither form nests in JavaScript, and neither appears inside a string literal in
// this tree, which is checked by `TestTheAuditsDoNotMisreadAString` rather than
// assumed.
func blankComments(source string) string {
	out := []byte(source)
	quote := byte(0)

	for index := 0; index < len(source); index++ {
		char := source[index]

		if quote != 0 {
			switch char {
			case '\\':
				index++

			case quote:
				quote = 0
			}

			continue
		}

		switch {
		case char == '\'' || char == '"' || char == '`':
			quote = char

		case char == '/' && index+1 < len(source) && source[index+1] == '/':
			for ; index < len(source) && source[index] != '\n'; index++ {
				out[index] = ' '
			}

		case char == '/' && index+1 < len(source) && source[index+1] == '*':
			for end := index + 2; end+1 < len(source); end++ {
				if source[end] == '*' && source[end+1] == '/' {
					for blank := index; blank <= end+1; blank++ {
						if out[blank] != '\n' {
							out[blank] = ' '
						}
					}

					index = end + 1

					break
				}
			}
		}
	}

	return string(out)
}

// jsStringLiterals returns every string literal in a comment-free source, with the
// line each one is on.
//
// Template literals are included with their interpolations left in, which is right
// for an audit: a banned word in an interpolated expression is still a banned word
// if the expression can produce one, and a colour in a template is still a colour.
func jsStringLiterals(source string) []stringLiteral {
	literals := []stringLiteral{}
	quote := byte(0)
	start := 0
	line := 1

	for index := 0; index < len(source); index++ {
		char := source[index]

		if char == '\n' {
			line++

			continue
		}

		if quote == 0 {
			if char == '\'' || char == '"' || char == '`' {
				quote = char
				start = index
			}

			continue
		}

		if char == '\\' {
			index++

			continue
		}

		if char == quote {
			literals = append(literals, stringLiteral{text: source[start+1 : index], line: line})
			quote = 0
		}
	}

	return literals
}

type stringLiteral struct {
	text string
	line int
}

func readMapSource(t *testing.T, name string) string {
	t.Helper()

	source, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}

	return blankComments(string(source))
}

// --- The vendor pin ------------------------------------------------------------

// vendorManifestDoc is `tools/vendor.json`.
//
// `description` and `why` are arrays of prose lines rather than one string, because
// a JSON string with newlines in it has to be escaped and a reader has to count
// `\n` to find the next sentence.
type vendorManifestDoc struct {
	Schema   int             `json:"schema"`
	Packages []vendorPackage `json:"packages"`
}

type vendorPackage struct {
	Name    string       `json:"name"`
	Version string       `json:"version"`
	License string       `json:"license"`
	Why     []string     `json:"why"`
	Source  vendorSource `json:"source"`
	Files   []vendorFile `json:"files"`
}

type vendorSource struct {
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	Integrity string `json:"integrity"`
}

type vendorFile struct {
	InPackage string `json:"inPackage"`
	Path      string `json:"path"`
	MediaType string `json:"mediaType"`
	Origin    string `json:"origin"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
}

func readVendorManifest(t *testing.T) vendorManifestDoc {
	t.Helper()

	raw, err := os.ReadFile(vendorManifest)
	if err != nil {
		t.Fatalf("read %s: %v. The vendor pin is D13: without it `make vendor-check` has "+
			"nothing to check and a swapped blob is trusted.", vendorManifest, err)
	}

	var doc vendorManifestDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", vendorManifest, err)
	}

	if doc.Schema != 1 {
		t.Fatalf("%s declares schema %d; this gate reads schema 1. A manifest whose shape "+
			"has moved needs a reader that knows the new shape, not a reader that reads the "+
			"old one loosely.", vendorManifest, doc.Schema)
	}

	return doc
}

// TestTheVendoredBytesMatchThePin is `make vendor-check`, in Go.
//
// Three claims, in the order a swap would break them: every declared file is there,
// every declared file's bytes hash to its declared digest, and **every file in the
// tree is declared**. The third is the one that catches the interesting accident — a
// hand-copied script dropped into a directory the product serves, which no digest
// covers because nobody wrote a digest for it.
func TestTheVendoredBytesMatchThePin(t *testing.T) {
	t.Parallel()

	manifest := readVendorManifest(t)

	if len(manifest.Packages) == 0 {
		t.Fatalf(
			"%s pins no packages, so nothing is vendored and nothing is checked.",
			vendorManifest,
		)
	}

	declared := map[string]bool{}

	for index := range manifest.Packages {
		pkg := &manifest.Packages[index]

		if pkg.Name == "" || pkg.Version == "" || pkg.License == "" {
			t.Errorf("a package in %s is missing a name, a version or a licence. A pinned "+
				"artefact with no recorded licence is one nobody checked.", vendorManifest)
		}

		if !strings.HasPrefix(pkg.Source.Integrity, "sha512-") {
			t.Errorf("%s pins %s with integrity %q; the tarball digest must be sha512, "+
				"which is what a registry publishes and what `make vendor` verifies.",
				vendorManifest, pkg.Name, pkg.Source.Integrity)
		}

		if pkg.Source.Kind != "npm" && pkg.Source.Kind != "archive" {
			t.Errorf("%s declares %s with source kind %q, and this reader knows npm "+
				"and archive. A kind it does not know is an upstream `make vendor` "+
				"refuses, so the pin describes bytes nothing can fetch",
				vendorManifest, pkg.Name, pkg.Source.Kind)
		}

		for file := range pkg.Files {
			declared[pkg.Files[file].Path] = true

			if len(pkg.Files[file].SHA256) != 64 {
				t.Errorf("%s declares %s with sha256 %q, which is not 64 hex characters",
					vendorManifest, pkg.Files[file].Path, pkg.Files[file].SHA256)
			}

			if pkg.Files[file].Origin == "archive" &&
				pkg.Source.Kind != "npm" && pkg.Source.Kind != "archive" {
				t.Errorf("%s marks %s as extracted from the pinned archive, but %s "+
					"declares source kind %q, and neither of the two kinds fetched "+
					"over https can supply it. A file claiming an upstream this "+
					"manifest does not name is a file nothing fetches",
					vendorManifest, pkg.Files[file].Path, pkg.Name, pkg.Source.Kind)
			}
		}
	}

	assertVendorTreeComplete(t, manifest, declared)
}

// assertVendorTreeComplete checks every committed file against the pin and every
// pinned file against the disk.
func assertVendorTreeComplete(t *testing.T, manifest vendorManifestDoc, declared map[string]bool) {
	t.Helper()

	root, err := filepath.Abs(vendorTree)
	if err != nil {
		t.Fatalf("resolve %s: %v", vendorTree, err)
	}

	for index := range manifest.Packages {
		for file := range manifest.Packages[index].Files {
			assertDigestMatches(t, manifest.Packages[index].Files[file])
		}
	}

	onDisk := map[string]bool{}

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}

		if entry.IsDir() {
			return nil
		}

		relative, rerr := filepath.Rel(repositoryRoot(t), path)
		if rerr != nil {
			return fmt.Errorf("vendor tree walk: %w", rerr)
		}

		onDisk[filepath.ToSlash(relative)] = true

		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", vendorTree, err)
	}

	for path := range onDisk {
		if !declared[path] {
			t.Errorf("%s is in the served vendor tree and declared in no pin.\\n"+
				"`make vendor-check` verifies the files the manifest lists; a file nobody "+
				"listed is a file nobody hashed, and the vendor tree is served to every "+
				"reader of every campaign.", path)
		}
	}
}

func assertDigestMatches(t *testing.T, file vendorFile) {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(repositoryRoot(t), file.Path))
	if err != nil {
		t.Errorf("%s is pinned and committed, and it is not on disk: %v", file.Path, err)

		return
	}

	if int64(len(body)) != file.Bytes {
		t.Errorf("%s is %d bytes, pinned at %d", file.Path, len(body), file.Bytes)
	}

	digest := sha256.Sum256(body)

	if got := hex.EncodeToString(digest[:]); got != file.SHA256 {
		t.Errorf("%s hashes to %s, pinned at %s.\\n"+
			"Either the artefact moved upstream or somebody edited a vendored file. Both are "+
			"what the pin exists for: a browser asset is executable code served to every "+
			"reader of every campaign, and an unhashed one is trusted rather than checked.",
			file.Path, got, file.SHA256)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	// `map` → `js` → `static` → `web` → `internal` → the repository root.
	root, err := filepath.Abs(repositoryDir)
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}

	return root
}

// TestTheVendoredFileIsThePinnedOneBytes is the narrower claim, and it is the one
// that says what the vendored blob actually *is*.
//
// A digest test proves the file has not changed since somebody wrote the digest. It
// does not prove the digest was ever computed from the npm package. So this one
// records the module's own version string, which is inside the artefact and comes
// from its build: `pixi.min.mjs` embeds `8.22.0` as the version it was compiled
// from, and a file whose embedded version disagrees with the pinned version is a
// swapped blob however consistent its hash is.
func TestTheVendoredFileIsThePinnedOneBytes(t *testing.T) {
	t.Parallel()

	manifest := readVendorManifest(t)

	for index := range manifest.Packages {
		pkg := &manifest.Packages[index]

		for file := range pkg.Files {
			if !strings.HasSuffix(pkg.Files[file].Path, ".mjs") {
				continue
			}

			body, err := os.ReadFile(filepath.Join(repositoryRoot(t), pkg.Files[file].Path))
			if err != nil {
				t.Errorf("%s: %v", pkg.Files[file].Path, err)

				continue
			}

			if banner := pixiBanner(pkg.Version); !strings.Contains(string(body), banner) {
				t.Errorf("%s does not carry the banner %q.\n"+
					"PixiJS writes `PixiJS - v<version>` into its minified build; finding it is "+
					"how a reader of a vendored blob can tell which upstream release it is "+
					"without trusting the digest that describes it.",
					pkg.Files[file].Path, banner)
			}
		}
	}
}

// --- The source audits ---------------------------------------------------------

// colourShapes match the ways a colour can be written. The probe sentinel in
// `palette.js` is the one allowance, and it is checked separately rather than
// exempted here: a rule with an unnamed exemption is a rule somebody deletes.
var colourShapes = []*regexp.Regexp{
	regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`),
	//nolint:misspell // The last name is a CSS function's own spelling.
	regexp.MustCompile(`^(rgb|rgba|hsl|hsla|hwb|lab|lch|oklab|oklch|color)\(`),
	regexp.MustCompile(`^(0x|#)[0-9a-fA-F]+$`),
}

// The one permitted colour string, and where it may appear.
//
// `"#000000"` is the sentinel `palette.js` paints into a throwaway 2D context to
// discover whether the browser *accepted* a token's value: an invalid `fillStyle`
// leaves the previous one in place, so without a sentinel "refused" and "black" look
// alike and every unreadable colour silently paints opaque black.
//
// It is held in a named constant rather than written twice — once into the probe and
// once in the comparison — because two string literals are two chances to write one of
// them differently, and that failure is invisible. So the gate holds the constant's
// *definition* rather than a use site: exactly one occurrence of the literal in the
// tree, and that occurrence is the right-hand side of `probeSentinel =`. A second
// copy — a real painted colour — is therefore still a failure.
const (
	probeSentinel     = `"#000000"`
	probeSentinelDecl = `probeSentinel = "#000000"`
)

// TestTheMapSourceCarriesNoColourOfItsOwn is "read the tokens, do not hardcode
// colours", over the whole tree.
//
// §6.2 makes `--map-dim` translucent in light, denser in dark, and worth nothing in
// luminance terms, and `--map-outline` an alias of `--text-subtle` that
// `prefers-contrast: more` promotes. A canvas holding its own copy of either would be
// a second answer: right on the day it was written, wrong the moment forced colours
// turn on, and invisible to `tokens_contrast_test.go`, which measures the stylesheet
// rather than the script.
func TestTheMapSourceCarriesNoColourOfItsOwn(t *testing.T) {
	t.Parallel()

	for _, name := range mapSources {
		if found := colourViolations(readMapSource(t, name)); len(found) > 0 {
			t.Errorf("%s carries colours of its own: %s\n"+
				"Every colour this client paints is read from a CSS custom property by "+
				"palette.js and normalised through the browser. A literal here is a second "+
				"answer to the token layer, and it is invisible to the contrast gate.",
				name, strings.Join(found, ", "))
		}
	}

	palette := readMapSource(t, "palette.js")

	if count := strings.Count(palette, probeSentinel); count != 1 {
		t.Errorf("palette.js holds the probe sentinel %d times, want exactly 1.\n"+
			"It exists to distinguish \"the browser refused this value\" from \"the value "+
			"is black\", so a second copy is a painted colour wearing the sentinel's name.",
			count)
	}

	if !strings.Contains(palette, probeSentinelDecl) {
		t.Errorf("palette.js does not declare the sentinel as %s.\n"+
			"The sentinel has to be one named constant used by both the assignment and the "+
			"comparison: two literals are two chances to write one of them differently, and "+
			"that failure is invisible.", probeSentinelDecl)
	}
}

// TestTheMapSourceReadsTheContrastTokens is the other half of the contract: the two
// tokens §7.6 names must be read, and the list must be a closed one.
//
// "Carries no colour of its own" is satisfiable by reading nothing at all and
// refusing to draw, so this asserts the positive side. It reads the array literally
// out of `palette.js` rather than importing it — there is no JavaScript runtime in
// CI — and a closed list is what makes that safe: an entry added to the array shows up
// here and in a review of the theme layer that has to override it.
func TestTheMapSourceReadsTheContrastTokens(t *testing.T) {
	t.Parallel()

	palette := readMapSource(t, "palette.js")

	if listed := mapTintTokensOf(
		readMapSource(t, "palette.js"),
	); !slices.Equal(
		listed,
		wantMapTokens,
	) {
		t.Errorf("palette.js's mapTintTokens is [%s]\nwant [%s].\n"+
			"--map-dim and --map-outline are UI §7.6's contrast overlay and are not optional;\n"+
			"the rest are the tokens this canvas paints with, and the list is closed so that "+
			"adding one is a review rather than an accident.",
			strings.Join(listed, ", "), strings.Join(wantMapTokens, ", "))
	}

	if !strings.Contains(palette, `"`+wantScaleToken+`"`) {
		t.Errorf("palette.js does not name %s.\n"+
			"UI §7.6 requires in-canvas labels to scale with --type-scale, and a client that "+
			"reads no scale reads 1 — which is wrong at every tier except the 1280 one.",
			wantScaleToken)
	}

	scene := readMapSource(t, "scene.js")

	for _, token := range []string{`"map-dim"`, `"map-outline"`} {
		if !strings.Contains(scene, token) {
			t.Errorf("scene.js never reads palette[%s]. The dim scrim and the 3px outline "+
				"are UI §7.6's first-class contrast overlay, and a token that is read but "+
				"never painted is the same failure as one that is never read.", token)
		}
	}
}

// TestTheMapSourceNamesNoWorldAndNoSession is the vocabulary rule, scoped to what a
// script can put in the interface.
//
// AGENTS.md: the words appear nowhere in the interface — not in an `aria-label`, not
// in a `title`, not in an error. A screen reader sees exactly what a JavaScript string
// literal becomes, so string literals are the surface this audit reads. It reads
// **literals and comments separately on purpose**: a comment may use either word to
// explain the rule, and an audit that read raw text would report the explanation as
// the violation — a loud and wrong failure, which trains a reader to ignore it.
func TestTheMapSourceNamesNoWorldAndNoSession(t *testing.T) {
	t.Parallel()

	for _, name := range mapSources {
		if found := vocabularyViolations(readMapSource(t, name)); len(found) > 0 {
			t.Errorf("%s has strings carrying a banned word: %s\n"+
				"AGENTS.md: the words appear nowhere in the interface — not in an "+
				"`aria-label`, not in a `title`, not in an error. A script string becomes "+
				"exactly those, and the live tabletop is the Table.",
				name, strings.Join(found, ", "))
		}
	}
}

// TestTheMapSourceNeverGivesTheCanvasAnApplicationRole is UI §7.6's prohibition, held
// where the canvas is built.
//
// `role="application"` tells a screen reader to stop reading keys, and the canvas
// takes keys only in the sense that the browser hands it pointer events — so it is a
// lie that makes the map unusable. The check is on the *comparison* rather than on
// the word: the tree must mention `application` exactly once, in the guard that
// refuses a surface carrying the role, and nowhere else.
func TestTheMapSourceNeverGivesTheCanvasAnApplicationRole(t *testing.T) {
	t.Parallel()

	for _, name := range mapSources {
		if stray := applicationRoleViolations(readMapSource(t, name)); stray > 0 {
			t.Errorf("%s uses the application role in %d place(s) outside the guard that "+
				"refuses it.\n"+
				"The only permitted mention is `getAttribute(\"role\") === \"application\"`, "+
				"read from the server-rendered surface so mount can refuse it.",
				name, stray)
		}
	}
}

// TestTheMapSourceOpensNoTransport is architecture §7's "the play page opens exactly
// one WS and one SSE".
//
// A browser allows about six connections per origin over HTTP/1.1 and every SSE
// stream holds one permanently, so a map that opened its own socket would spend one
// of them on a decoration. C3's live chrome owns the single connection and hands
// state in through `handle.setState`; this is the assertion that the map has not
// quietly opened a second door.
func TestTheMapSourceOpensNoTransport(t *testing.T) {
	t.Parallel()

	for _, name := range mapSources {
		if found := transportViolations(readMapSource(t, name)); len(found) > 0 {
			t.Errorf("%s opens a transport: %s.\n"+
				"The map is fed by `handle.setState`; the play page's single WebSocket and "+
				"single SSE belong to the live chrome, and a browser allows about six "+
				"connections per origin over HTTP/1.1.",
				name, strings.Join(found, ", "))
		}
	}
}

// TestTheMapSourceLoadsPixiFromTheVendoredCopy is D13's serving half.
//
// A CDN is a second origin, which means authenticating a player to it and trusting
// two answers for one asset. A relative specifier means the bytes come from the same
// origin the campaign's authorisation already covers — and it also means the import
// only resolves if `make vendor` stages `static/vendor/` into the asset tree, so
// this assertion doubles as a check that the staged layout is the one the code
// expects.
func TestTheMapSourceLoadsPixiFromTheVendoredCopy(t *testing.T) {
	t.Parallel()

	if found := importViolations(readMapSource(t, "scene.js")); len(found) > 0 {
		t.Errorf("scene.js imports from somewhere other than the vendored copy: %s\n"+
			`The vendored copy is served from the app's own origin, so the specifier is `+
			"relative to `static/js/map/` and resolves to `static/vendor/pixi.min.mjs` "+
			"once `make vendor` stages it beside `static/js/`. A CDN is a second origin, "+
			"which means authenticating a player to it.",
			strings.Join(found, ", "))
	}
}

// TestTheResizePathNeverCallsFitCamera is the wiring half of the resize invariant.
//
// The camera's half is structural — `resizeCamera` cannot see map bounds. This is the
// other half: the function `scene.js` runs when the viewport changes must reach for
// `resizeCamera`, and must not reach for `fitCamera`.
//
// It reads the body rather than counting occurrences because `fitCamera` is
// legitimately called once in `zoom`, for the zoom-out bound. A count would either
// miss the bug or forbid the legitimate call; a body read tells them apart.
func TestTheResizePathNeverCallsFitCamera(t *testing.T) {
	t.Parallel()

	body := methodBody(readMapSource(t, "scene.js"), "resize", "(viewport)")

	if body == "" {
		t.Fatal("scene.js declares no `resize(viewport)` method, so the resize path cannot " +
			"be read. A resize observer with nothing behind it leaves the canvas at its " +
			"initial size forever, and the requirement is that a resize does not re-frame.")
	}

	if !strings.Contains(body, "camera.resizeCamera(") {
		t.Errorf("scene.js's resize does not call camera.resizeCamera:\n%s", body)
	}

	if strings.Contains(body, "fitCamera") {
		t.Errorf("scene.js's resize calls fitCamera:\n%s\n"+
			"Re-fitting on a resize re-centres on the middle of the map and re-scales to fit, "+
			"which is the re-framing UI §7.6 forbids and which "+
			"TestTheMapSurvivesAResizeWithoutReframing fails.", body)
	}

	if !strings.Contains(body, "app.renderer.resize") {
		t.Errorf("scene.js's resize does not resize the renderer:\n%s\n"+
			"A camera that does not move but a canvas that is never told its new size "+
			"renders the old frame stretched.", body)
	}
}

// TestTheMapSourceAsksForThreePixelsAndHidesTheCanvas holds the two §7.6 obligations
// that are one line each and would otherwise be satisfied by a comment.
//
// Three claims that are one line each and would otherwise be satisfied by a comment.
//
// The canvas's `aria-hidden` is C4's markup, so `mount` **refuses** rather than
// trusting it — and `scene.js` sets the same attribute on the canvas it creates,
// because the canvas did not exist when the server rendered the surface. The 3px
// outline is a constant that `camera.js`'s `worldWidthForScreenPixels` converts into
// map units. Any of the three could be deleted and nothing else in the suite would
// fail.
func TestTheMapSourceAsksForThreePixelsAndHidesTheCanvas(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		`const outlinePixels = 3;`,
		`camera.worldWidthForScreenPixels(`,
		`canvas.setAttribute("aria-hidden", "true")`,
		`canvas.removeAttribute("role")`,
	} {
		if !strings.Contains(readMapSource(t, "scene.js"), want) {
			t.Errorf("scene.js does not contain %s.\n"+
				"UI §7.6's contrast overlay is 3px --map-outline on every placement and grid "+
				"line, and the conversion that keeps it 3 screen pixels at every zoom is "+
				"camera.js's `worldWidthForScreenPixels`.", want)
		}
	}

	if !strings.Contains(readMapSource(t, "table.js"), `getAttribute("aria-hidden") !== "true"`) {
		t.Errorf("table.js's mount does not refuse a surface that is not aria-hidden.\n" +
			"The canvas is a decorative mirror and the token list is the interface, so a " +
			"surface that is not hidden announces an unlabelled graphic.")
	}
}

// TestEveryMapSourceAuditRejectsItsViolation is the meta-test for the audits above.
//
// Each entry is a violation injected into one file's source in memory, and the
// predicate that must then object. Both halves call the **same function**, so this
// cannot pass by checking a copy of the rule that has drifted from the one the tests
// use.
//
// Without it, an audit whose pattern stopped matching — because a refactor renamed a
// variable, say — would pass for ever and the coverage it reported would be fictional.
// Three of phase 5's first gate tests did not fail, and `make a11y` was green while
// three route packages contributed nothing to it.
func TestEveryMapSourceAuditRejectsItsViolation(t *testing.T) {
	t.Parallel()

	for _, audit := range []struct {
		name    string
		file    string
		mutate  func(string) string
		offends func(string) bool
	}{
		{
			name:    "a hex colour literal",
			file:    "scene.js",
			mutate:  appendLine(`const accidental = "#ff00ff";`),
			offends: func(source string) bool { return len(colourViolations(source)) > 0 },
		},
		{
			name:    "an rgb() literal",
			file:    "palette.js",
			mutate:  appendLine(`const accidental = "rgb(1, 2, 3)";`),
			offends: func(source string) bool { return len(colourViolations(source)) > 0 },
		},
		{
			name:    "the word world in a string",
			file:    "scene.js",
			mutate:  appendLine(`const accidental = "the world bounds";`),
			offends: func(source string) bool { return len(vocabularyViolations(source)) > 0 },
		},
		{
			name:    "the word session in a string",
			file:    "table.js",
			mutate:  appendLine(`const accidental = "this session";`),
			offends: func(source string) bool { return len(vocabularyViolations(source)) > 0 },
		},
		{
			name:   "a second probe sentinel",
			file:   "palette.js",
			mutate: appendLine(`const accidental = "#000000";`),
			offends: func(source string) bool {
				return strings.Count(source, probeSentinel) != 1
			},
		},
		{
			name: "a dropped contrast token",
			file: "palette.js",
			mutate: func(source string) string {
				return strings.Replace(source, `"--map-outline",`, "", 1)
			},
			offends: func(source string) bool {
				return !slices.Equal(mapTintTokensOf(source), wantMapTokens)
			},
		},
		{
			name: "a re-fit on resize",
			file: "scene.js",
			mutate: func(source string) string {
				return replaceMethodBody(source, "resize", "(viewport)",
					"{\n  cameraState = camera.fitCamera(map, viewport);\n}")
			},
			offends: resizePathCallsFitCamera,
		},
		{
			name: "the resize path stopped reaching for the camera",
			file: "scene.js",
			mutate: func(source string) string {
				return replaceMethodBody(source, "resize", "(viewport)",
					"{\n  app.renderer.resize(viewport.width, viewport.height);\n}")
			},
			offends: func(source string) bool { return !resizePathUsesResizeCamera(source) },
		},
		{
			name:    "an application role on the canvas",
			file:    "table.js",
			mutate:  appendLine(`const accidental = canvas.setAttribute("role", "application");`),
			offends: func(source string) bool { return applicationRoleViolations(source) > 0 },
		},
		{
			name:    "a second socket",
			file:    "table.js",
			mutate:  appendLine(`const accidental = new WebSocket(url);`),
			offends: func(source string) bool { return len(transportViolations(source)) > 0 },
		},
		{
			name:    "an event stream",
			file:    "table.js",
			mutate:  appendLine(`const accidental = new EventSource(url);`),
			offends: func(source string) bool { return len(transportViolations(source)) > 0 },
		},
		{
			name:    "a CDN import",
			file:    "scene.js",
			mutate:  appendLine(`const accidental = "https://unpkg.com/pixi.js";`),
			offends: func(source string) bool { return len(importViolations(source)) > 0 },
		},
		{
			name: "a dropped 3px outline",
			file: "scene.js",
			mutate: func(source string) string {
				return strings.Replace(source, "const outlinePixels = 3;", "const outlinePixels = 1;", 1)
			},
			offends: func(source string) bool {
				return !strings.Contains(source, "const outlinePixels = 3;")
			},
		},
		{
			name: "a surface that need not be hidden",
			file: "table.js",
			mutate: func(source string) string {
				return strings.Replace(source,
					`getAttribute("aria-hidden") !== "true"`,
					`getAttribute("aria-hidden") === "true"`, 1)
			},
			offends: func(source string) bool {
				return !strings.Contains(source, `getAttribute("aria-hidden") !== "true"`)
			},
		},
	} {
		t.Run(audit.name, func(t *testing.T) {
			t.Parallel()

			source := blankComments(audit.mutate(readMapSource(t, audit.file)))

			if audit.offends(source) {
				return
			}

			t.Fatalf("injecting the violation into %s left the audit silent.\n"+
				"An audit that cannot fail is a green light wired to nothing, and the coverage "+
				"it reports is fictional. Either the pattern stopped matching or the audit "+
				"does not read what it claims to read.", audit.file)
		})
	}
}

// TestTheAuditsDoNotMisreadAString is the other direction for the comment-blanking
// scanner: a `//` or `/*` inside a string literal must not be treated as a comment.
//
// Without it, `palette.js`'s regex for a hex colour would blank the rest of the file
// and every later audit would read nothing. The two directions have to be tested
// together or a scanner that mangles everything passes both.
// TestAColourInACommentIsNotAViolation is the negative of the colour audit, and it is
// in this file because the failure it prevents is one this repository has already
// shipped once.
//
// `TestTheBuiltStylesheetScansThePluginSources` had to be taught to walk Go string
// literals through `go/parser` rather than raw text, because a `class="untargeted"`
// inside a comment explaining that a rename would be caught is prose. A raw-text
// audit reported it as a missing class: a loud and **wrong** failure, which is worse
// than no audit, because the next real one gets ignored along with it.
//
// These source files discuss `#ff00ff` and the banned words by name — the comments
// above say what a violation looks like — so an audit that read raw text would fail on
// its own documentation.
func TestAColourInACommentIsNotAViolation(t *testing.T) {
	t.Parallel()

	commented := blankComments("// the accidental fill is \"#ff00ff\"\n" +
		"// and the banned words are world and session\n" +
		"const real = 1;\n")

	if found := colourViolations(commented); len(found) > 0 {
		t.Errorf("the colour audit reported %v inside a comment. A comment explaining what a "+
			"violation looks like is prose, and a loud and wrong failure is worse than none.",
			found)
	}

	if found := vocabularyViolations(commented); len(found) > 0 {
		t.Errorf("the vocabulary audit reported %v inside a comment.", found)
	}
}

func TestTheAuditsDoNotMisreadAString(t *testing.T) {
	t.Parallel()

	const tricky = "const a = \"// not a comment\";\nconst b = '/* also not */';\nconst c = 1;\n"

	blanked := blankComments(tricky)

	if !strings.Contains(blanked, "// not a comment") {
		t.Error("blankComments blanked a `//` inside a string literal, which would make " +
			"every audit in this file read the file as shorter than it is.")
	}

	if !strings.Contains(blanked, "const c = 1;") {
		t.Error("blankComments stopped early at a `/*` inside a string literal.")
	}

	if got := len(jsStringLiterals(blankComments("const a = 1; // world\n"))); got != 0 {
		t.Errorf("jsStringLiterals found %d literals in a line with only a comment", got)
	}
}

// --- Small helpers -------------------------------------------------------------

// pixiBanner is the version banner PixiJS writes into its minified ESM build.
//
// A constant rather than a format string because the whole point is that it is the
// one place a reader has to look when a digest has been regenerated from the wrong
// tarball, and a second copy of that pattern is a second thing to get wrong.
func pixiBanner(version string) string {
	return "PixiJS - v" + version
}

// wantMapTokens is palette.js's `mapTintTokens`, sorted, as the gate expects it.
//
// Sorted because the source's order is a *reading* order — the two map tokens first,
// because they are the ones the requirement names — and a gate that demanded a
// particular reading order would fail on a harmless reshuffle. What the gate demands
// is the set. `--type-scale` is not in it: it is a multiplier and lives in
// `mapScaleToken`, and the gate holds that separately.
var wantMapTokens = []string{
	"--accent",
	"--danger",
	"--map-dim",
	"--map-outline",
	"--success",
	"--surface",
	"--text",
	"--warning",
}

// wantScaleToken is the multiplier palette.js reads on its own.
//
// Held separately because it is a number: a gate that demanded `--type-scale` be in
// the colour list would be demanding a `null` field under a colour's name, and a
// record with a field that is always `null` is a field somebody will eventually read.
const wantScaleToken = "--type-scale"

// colourViolations lists the colour-shaped literals in one file.
func colourViolations(source string) []string {
	found := []string{}

	for _, literal := range jsStringLiterals(source) {
		if literal.text == strings.Trim(probeSentinel, `"`) {
			continue
		}

		for _, shape := range colourShapes {
			if shape.MatchString(literal.text) {
				found = append(found, literal.text)
			}
		}
	}

	return found
}

// vocabularyViolations lists the literals carrying a banned word.
func vocabularyViolations(source string) []string {
	found := []string{}

	for _, literal := range jsStringLiterals(source) {
		lowered := strings.ToLower(literal.text)

		if strings.Contains(lowered, "world") || strings.Contains(lowered, "session") {
			found = append(found, literal.text)
		}
	}

	return found
}

// applicationRoleViolations counts the quoted uses of the application role.
//
// Only the **quoted** form counts, because only the quoted form can be an attribute
// value. The guard's own diagnostic mentions the role in prose — `role=application is
// prohibited` — and an audit that counted bare words would report the explanation as
// the violation, which is a loud and wrong failure that trains a reader to ignore the
// rule.
func applicationRoleViolations(source string) int {
	allowed := regexp.MustCompile(`getAttribute\("role"\)\s*===?\s*"application"`)

	return strings.Count(source, `"application"`) - len(allowed.FindAllString(source, -1))
}

// transportViolations lists the transports a file reaches for.
func transportViolations(source string) []string {
	found := []string{}

	for _, forbidden := range []string{
		"WebSocket", "EventSource", "XMLHttpRequest", "fetch(", "navigator.sendBeacon",
	} {
		if strings.Contains(source, forbidden) {
			found = append(found, forbidden)
		}
	}

	return found
}

// importViolations lists the ways a file reaches for a copy of PixiJS that is not the
// committed one.
// vendorSpecifier is the relative import `scene.js` must use for the vendored copy.
//
// **Two levels up, not one**, and that is not a detail: `static/js/map/` is two
// directories below `static/`, so `../vendor/` resolves to `static/js/vendor/` — a
// directory nothing stages — and the module fails to load with an error that names
// only the import. It is the kind of mistake a text audit cannot see and a browser
// reports as "Failed to fetch dynamically imported module" with no file named.
const vendorSpecifier = `from "../../vendor/pixi.min.mjs"`

func importViolations(source string) []string {
	found := []string{}

	if !strings.Contains(source, vendorSpecifier) {
		found = append(found, "no import "+vendorSpecifier)
	}

	for _, forbidden := range []string{"https://cdn", "unpkg.com", "jsdelivr", "esm.sh"} {
		if strings.Contains(source, forbidden) {
			found = append(found, forbidden)
		}
	}

	return found
}

// mapTintTokensOf reads palette.js's `mapTintTokens` array and returns it sorted.
func mapTintTokensOf(source string) []string {
	listed := quotedStringsBetween(source, "export const mapTintTokens = [", "];")
	slices.Sort(listed)

	return listed
}

// resizePathCallsFitCamera reports whether scene.js's resize method re-fits.
func resizePathCallsFitCamera(source string) bool {
	return strings.Contains(methodBody(source, "resize", "(viewport)"), "fitCamera")
}

// resizePathUsesResizeCamera reports whether scene.js's resize method reaches for the
// camera at all.
func resizePathUsesResizeCamera(source string) bool {
	return strings.Contains(methodBody(source, "resize", "(viewport)"), "camera.resizeCamera(")
}

// appendLine adds one line of JavaScript to a comment-free source.
func appendLine(line string) func(string) string {
	return func(source string) string {
		return source + "\n" + line + "\n"
	}
}

// quotedStringsBetween returns the quoted literals inside a slice of source, in order.
func quotedStringsBetween(source, opener, closer string) []string {
	_, rest, opened := strings.Cut(source, opener)
	if !opened {
		return nil
	}

	body, _, closed := strings.Cut(rest, closer)
	if !closed {
		return nil
	}

	quoted := strings.IndexByte(body, '"')
	if quoted < 0 {
		return []string{}
	}

	literals := jsStringLiterals(body[quoted:])
	found := make([]string, 0, len(literals))

	for _, literal := range literals {
		found = append(found, literal.text)
	}

	return found
}

// methodBody extracts one method's body from a comment-free source, brace-matched
// from the signature.
//
// Returns "" rather than failing, because one of the callers has to be able to say
// "and there was no such method at all", which is the loudest form of the failure.
func methodBody(source, name, signature string) string {
	header := name + signature + " {"
	start := strings.Index(source, header)
	if start < 0 {
		return ""
	}

	open := start + len(header) - 1
	depth := 0

	for index := open; index < len(source); index++ {
		switch source[index] {
		case '{':
			depth++

		case '}':
			depth--

			if depth == 0 {
				return source[open+1 : index]
			}
		}
	}

	return ""
}

// replaceMethodBody swaps one method's whole body, braces included.
//
// The mutation the meta-test needs, and it has to replace the *braces* rather than the
// text inside them: `scene.js`'s resize contains a nested call, so a regex that stopped
// at the first `}` would leave half a method behind and produce a parse error that
// reads like a defect in the mutation rather than in the audit.
func replaceMethodBody(source, name, signature, body string) string {
	header := name + signature + " {"
	start := strings.Index(source, header)
	if start < 0 {
		return source
	}

	open := start + len(header) - 1
	depth := 0
	end := -1

	for index := open; index < len(source); index++ {
		switch source[index] {
		case '{':
			depth++

		case '}':
			depth--

			if depth == 0 {
				end = index
			}
		}

		if end >= 0 {
			break
		}
	}

	if end < 0 {
		return source
	}

	return source[:open] + body + source[end+1:]
}
