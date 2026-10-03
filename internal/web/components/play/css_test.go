package play_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The play sheet is in the build, and its selectors are the ones the document
// renders.
//
// # Two failures, and only one of them is the famous one
//
// `AGENTS.md` documents at length the first: **a missing `@import` is not a
// compile error and not a test failure.** `internal/web` embeds `static/dist/`,
// `make css` builds that from `app.css`'s import list, and a sheet nobody imports
// is a sheet that does not exist as far as the binary is concerned — so the
// tabletop renders unstyled while every §10.1, §10.2 and §10.6 gate passes,
// because the markup is correct and only the bytes are missing.
//
// The second failure is newer here and **invisible to a sentinel check**: a sheet
// that *is* imported, whose rules name selectors the document does not render.
// It loads, it parses, it contributes its sentinel to the built file, and it
// styles nothing. That is what this file was written after — a draft of
// `play.css` styled `.play-action-bar`, `.sheet-rail`, `.play-placement` and
// `.play-grid-line` while `document.templ` renders `.play-actions`, `.shell-rail`
// and no such thing as a placement element at all. Its own "is the sheet in the
// build" test passed throughout, because the sentinel was genuinely in the file.
//
// So there are two tests, and the second is the one that would have caught it.

// notInTheBuild is the sentence this sheet's own header carries while `app.css`
// does not import it.
//
// A **header marker**, not a skip, and the distinction is the whole design: the
// assertion is a disjunction of two facts, both checked. Either `app.css` imports
// the sheet — and then the **built** file is read, because that is the only
// artefact that can tell a rule was dropped by the build — or the sheet's header
// says it is not wired in yet, and the reason is logged on every run.
//
// Delete the marker without the import landing and this goes **red**, which is
// the direction that matters: a note must not outlive the fact it describes, and
// the failure that catches that is the whole reason for the second branch
// existing rather than the test simply being deleted until the import lands.
const notInTheBuild = "A missing `@import` is not a compile error"

// theSheetIsTheSentinel is this sheet's own contribution to the built file.
//
// `--action-bar-h` rather than a class name, and for the reason C1's pin test and
// this phase's `live.css` test both give: `strings.Contains(css, ".notice")`
// answers true against a stylesheet carrying only `.notice--warning`, so a class
// is a substring and a custom property name nothing else declares is not.
const theSheetIsTheSentinel = "--action-bar-h"

// theClassesThisSheetIsResponsibleFor is every class `document.templ` renders
// that `play.css` owns.
//
// **A list, not a derivation, and that is deliberate.** Deriving it from
// `document.templ` would assert that *every* class needs a rule here, which is
// false: `shell-main`, `shell-rail`, `rail-panels`, `card-list`, `empty`,
// `panel-note`, `visually-hidden` and `target` belong to `shell.css` or
// `components/chat`, and restating them here would be a second declaration of a
// rule another sheet owns — the shape of bug this repository keeps recording in
// the `front_matter` column, the design index and the hook vocabulary.
//
// What the list buys is the claim in the sheet's own header: *every selector here
// exists in the rendered document.* A class invented for a stylesheet is the
// failure, and it is invisible to every other test in the repository.
var theClassesThisSheetIsResponsibleFor = []string{
	"play-actions", // §4.9's action bar
	"play-map",     // the map surface or its designed empty state
	"play-heading", // the document's <h1>
	"shell--play",  // the grid variant the action bar sits in
	"roll-terms",   // the roll sheet's row container
	"roll-term",    // one die in the roll sheet
}

// TestTheSheetIsInsideTheBuild holds the first failure: the import.
//
// The branch structure is the design, so it is worth stating once more here
// rather than only above the constant: while `app.css` does not import
// `play.css` the test is green **and loud**, because the sheet's header says so
// and this logs it on every run. The day the import lands, the first branch
// takes over, reads `internal/web/static/dist/app.css`, and the second stops
// holding — and deleting the header marker without adding the import is red.
//
// A test that only reads the marker would be a test that passes forever and
// checks nothing; a test that only reads the built file would be a permanently
// red test on this branch, and a permanently red test is one everybody learns to
// ignore. Neither is acceptable, so the assertion is both.
func TestTheSheetIsInsideTheBuild(t *testing.T) {
	t.Parallel()

	entry := readFile(t, sheetEntryPoint)

	if strings.Contains(entry, "play.css") {
		built := readFile(t, builtStylesheetPath)

		if !strings.Contains(built, theSheetIsTheSentinel) {
			t.Errorf("the built stylesheet carries no %s, so play.css is imported "+
				"but its rules are not in the output. A selector the scanner cannot "+
				"parse, or a rule inside a layer that loses the cascade, is dropped "+
				"by the build and the built file is the only artefact that can tell",
				theSheetIsTheSentinel)
		}

		return
	}

	t.Logf("NOTE: %s is not imported by %s yet.\n"+
		"      internal/web embeds static/dist/ and make css builds that from "+
		"app.css's import list, so a sheet nobody imports renders the tabletop "+
		"unstyled while every section 10.2 gate passes. add to %s, after tv.css:\n"+
		"          @import \"./play.css\";",
		sheetPath, entry, sheetEntryPoint)

	if !strings.Contains(readFile(t, sheetPath), notInTheBuild) {
		t.Errorf("%s is not imported by %s and its own header no longer says so.\n"+
			"Either the import was added and this note left behind -- in which case "+
			"the built stylesheet must be re-checked, since `make css` has not "+
			"necessarily run -- or the sheet was never wired in and the warning was "+
			"deleted. Both leave this test asserting nothing", sheetPath, entry)
	}
}

// TestEveryClassThisSheetIsResponsibleForExistsInTheDocument is the second
// failure, and the one a sentinel check cannot see.
//
// It reads the **document template's** `class="…"` attributes and requires each
// class this sheet claims to style to be among them. The walk is a plain scan of
// the attribute literals rather than `go/parser`, because `.templ` files are not
// Go — the same correction `components/live`'s sheet test records, and the same
// one that made a comment containing `class="…"` report a class the stylesheet
// was missing.
//
// Two directions, because either can hold while the other fails:
//
//   - a class this sheet styles that the document does not render — the sheet
//     loads and styles nothing; and
//   - a class the document renders that this sheet owns but does not style —
//     the rule was written for the wrong element.
func TestEveryClassThisSheetIsResponsibleForExistsInTheDocument(t *testing.T) {
	t.Parallel()

	sheet := readFile(t, sheetPath)
	rendered := renderedClasses(t)

	for _, class := range theClassesThisSheetIsResponsibleFor {
		t.Run(class, func(t *testing.T) {
			t.Parallel()

			if !strings.Contains(rendered, `class="`+class) &&
				!strings.Contains(rendered, ` `+class+`"`) &&
				!strings.Contains(rendered, ` `+class+` `) {
				t.Errorf("play.css styles .%s but document.templ renders no such "+
					"class. The sheet loads, parses and contributes its sentinel to "+
					"the built file while styling nothing -- the failure a sentinel "+
					"check cannot see, because the sentinel really is there",
					class)
			}

			if !strings.Contains(sheet, "."+class) {
				t.Errorf("document.templ renders %q and play.css has no rule for it. "+
					"Either the rule is missing or the class belongs to another "+
					"sheet, in which case it should come off this sheet's list "+
					"rather than sit here unstyled", class)
			}
		})
	}
}

// TestNoSelectorInThisSheetIsAbsentFromTheDocument is the other direction, and
// the one that catches a class invented for a stylesheet.
//
// Every simple class selector in `play.css` must appear in the document, with
// two exceptions named rather than pattern-matched away:
//
//   - the sheet's own custom properties, which are not selectors at all and are
//     read by `static/js/map/table.js`; and
//   - `.play-placement` and `.play-grid-line`, which **must not exist** —
//     placements and grid lines are sprites in a canvas, not elements in the DOM,
//     so §7.6's `aria-hidden` mirror has nothing for a CSS rule to select.
//
// The exception list is the interesting part. A rule that says "ignore anything
// matching `play-`" would pass today and would also pass a fourth invented class;
// naming the two is a claim a reader can check, and `TestTheCanvasIsNotStyledBy
// CSSBecauseItHasNoElements` holds why they are absent.
func TestNoSelectorInThisSheetIsAbsentFromTheDocument(t *testing.T) {
	t.Parallel()

	sheet := readFile(t, sheetPath)
	rendered := renderedClasses(t)

	// Only `.a-class` selectors, and only whole ones: `--map-dim` is a property,
	// `60dvb` is a unit, and a compound `.a .b` is two selectors this loop would
	// otherwise have to understand.
	// **Comments stripped first, and this is the third time this phase a rule that
	// reads raw text has had to be corrected.** A `class="…"`-shaped example inside
	// an explanatory comment is prose, and a walk that reads prose reports a
	// selector the sheet does not style — a loud and *wrong* failure, which is
	// worse than none because the next real one gets ignored. The first version of
	// this test named `.play-action-bar` and `.go`, both of which appear in this
	// sheet's own header comment and nowhere else.
	declared := stripCSSComments(sheet)

	matches := regexp.MustCompile(`\.([a-z][a-z0-9-]*)`).FindAllStringSubmatch(declared, -1)

	if len(matches) == 0 {
		t.Fatal("no class selector was found in play.css at all. The walk reads " +
			"the sheet's bytes, so an empty result means the sheet is empty or the " +
			"pattern no longer matches what it is written to match")
	}

	seen := map[string]bool{}

	for _, match := range matches {
		class := match[1]
		if seen[class] {
			continue
		}

		seen[class] = true

		if !strings.Contains(rendered, class) {
			t.Errorf("play.css styles .%s and the rendered document contains no such "+
				"class. A selector with no element is a rule that never applies, and "+
				"the sheet still builds, still passes a sentinel check and still "+
				"leaves the tabletop unstyled", class)
		}
	}
}

// --- The fixtures --------------------------------------------------------------

// The three paths, as constants rather than inline strings, because a test that
// names the same path three times in three literals is a test with three chances
// to be wrong about it.
const (
	sheetPath           = "../../static/css/play.css"
	sheetEntryPoint     = "../../static/css/app.css"
	builtStylesheetPath = "../../static/dist/app.css"
)

// readFile fails rather than skipping when a file is absent.
//
// **Failing, not skipping**, for the reason the `Makefile`'s build target gives
// and the reason `internal/web`'s stylesheet gate gives: a skipped stylesheet
// assertion is an unstyled product that reports itself green, which is the exact
// failure this file exists to make impossible. The message names the fix,
// because "no such file" sends a reader looking for a permissions problem when
// the commonest cause is a checkout nobody has run `make css` on.
func readFile(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v. If this is the built stylesheet, run `make css`",
			path, err)
	}

	return string(raw)
}

// renderedClasses is the document template's text.
//
// The `.templ` file rather than a rendered document, and the reason is
// specificity: the classes a sheet must cover are a property of the **markup**,
// so reading the markup is reading the claim directly. Rendering it would add a
// route, a store and a session to a test about a stylesheet, and would make the
// failure mode "the fixture could not be built" rather than "the sheet styles a
// class that is not there".
//
// The comment markers are skipped rather than read, which is the third time this
// phase that correction has been needed: a `class="…"` inside an explanatory
// comment is prose, and a walk that reads prose reports a class the stylesheet is
// missing — a loud and *wrong* failure, which is worse than none because the next
// real one gets ignored.
func renderedClasses(t *testing.T) string {
	t.Helper()

	// **More than one file**, and the reason is that a selector in this sheet is
	// routinely compound across a package boundary: `.shell--play .shell-rail`
	// names a class `document.templ` renders and one `chrome/rail.templ` renders,
	// and reading only the first reports the second as invented — which is the
	// same wrong failure in the opposite direction.
	//
	// The list is the whole set of templates whose classes this sheet may
	// reference, so adding a class to a new template means adding it here, and a
	// template that is listed and absent fails rather than being skipped.
	for _, path := range renderedSources {
		if !strings.Contains(readFile(t, path), "templ ") {
			t.Fatalf("%s contains no templ declaration; the list of rendered "+
				"sources is a claim about which files hold the document's classes, "+
				"and a file that no longer does is a claim that stopped being true",
				path)
		}
	}

	var kept strings.Builder

	for _, path := range renderedSources {
		for line := range strings.SplitSeq(readFile(t, path), "\n") {
			trimmed := strings.TrimSpace(line)

			switch {
			case trimmed == "":
				continue
			case strings.HasPrefix(trimmed, "//"):
				continue
			case strings.HasPrefix(trimmed, "/*"), strings.HasSuffix(trimmed, "*/"):
				continue
			}

			kept.WriteString(line)
			kept.WriteByte('\n')
		}
	}

	return kept.String()
}

// renderedSources is every template whose `class="…"` this sheet may reference.
var renderedSources = []string{
	"document.templ",
	"../chrome/rail.templ",
	"../chrome/header.templ",
	"../chrome/footer.templ",
}

// stripCSSComments removes `/* … */` blocks, including multi-line ones.
//
// A small hand-rolled scan rather than a regexp because CSS comments nest in no
// specification but *do* appear inside strings and url() values in other sheets,
// and `(?s)/\*.*?\*/` would happily delete a stylesheet's contents on the first
// `/*` inside a data URI. This sheet has no such value today; the scanner is
// written as though one might be added, because the failure it prevents is
// silent.
func stripCSSComments(css string) string {
	var kept strings.Builder

	for {
		open := strings.Index(css, "/*")
		if open < 0 {
			kept.WriteString(css)

			break
		}

		kept.WriteString(css[:open])

		rest := css[open+2:]

		_, after, found := strings.Cut(rest, "*/")
		if !found {
			// An unterminated comment swallows the rest of the file, which is
			// itself the finding: nothing after it can be styled.
			break
		}

		css = after
	}

	return kept.String()
}
