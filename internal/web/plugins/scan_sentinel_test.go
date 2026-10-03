// This file exists to make one assertion in `internal/web` non-vacuous, and it is
// worth being explicit that it is **test code in a package whose other tests are
// about the registry**.
//
// # The claim it holds
//
// `static/css/app.css` names the roots Tailwind scans, and `internal/web/plugins` is
// one of them because §10.6's reference plugins render their centre slot from Go
// string constants — `dice`'s `widgetMarkup`, `linkpreview`'s `hookMarkup` — so
// every class a plugin puts on a page is a class in a Go file under this directory.
// Drop the `@source` glob that covers here and:
//
//   - nothing fails to compile;
//   - nothing fails to build;
//   - every §10.2 and §10.6 audit passes, because they read the *markup*, and the
//     markup is correct;
//   - a Tailwind utility in a plugin template is never emitted, so the plugin renders
//     unstyled.
//
// That is the same shape as the missing-`@import` failure this repository has already
// paid for once: "the shell renders" and "the shell is unstyled" are the same
// observation from every angle a test can take **unless the test asks whether the
// bytes exist**. `TestTheBuiltStylesheetCarriesTheTokensAndTheGrid` asks that about
// the `@import`s; `internal/web`'s `TestTheBuiltStylesheetScansThePluginSources` asks
// it about these globs, and it reads `ScanSentinelClass` out of this file to do so.
//
// # Why this is a *file* and not a comment in the test that reads it
//
// Because the assertion needs a class that exists **only** in this tree. Every class
// the shipped plugins use today is a component class from `shell.css` — `notice`,
// `form`, `field`, `button`, `target` — so all of them are in the built stylesheet
// whether or not this directory is scanned, and a test that walked the plugin sources
// looking for missing classes would pass with the glob deleted. A walk that proves
// coverage needs one class nothing else names.
//
// # What it costs, and why that is the cheaper half
//
// A few dozen bytes of CSS for one utility no page renders. That is the price of a
// coverage claim checked at build time rather than remembered, and it is cheaper than
// the alternative: a plugin that renders unstyled while every gate in this repository
// reports green.
//
// # Why a test file rather than a source file
//
// Because the class is only ever used by an assertion. Putting it in a `.go` file
// would ship a constant no product code reads, which is the field-nothing-reads
// defect this repository keeps paying for in other shapes.
package webplugins_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ScanSentinelClass names a Tailwind utility no other source in this repository uses.
//
// **Read by `internal/web`'s stylesheet gate, and by nothing else.** It is exported for
// that reader and for no other reason, and a reader that finds it in the built
// stylesheet is finding the proof that Tailwind scanned this directory.
//
// A table-numeral utility is the choice because it is a real Tailwind class no page
// needs, and deliberately **not** spelled in a way Tailwind's scanner ignores: an
// unreadable file would make the assertion pass for the wrong reason, which is the
// failure this whole arrangement exists to prevent. `TestTheScanSentinelIsNamedInNoOtherSource`
// holds the uniqueness, so the day a component uses this class the sentinel stops
// proving anything and that test says so.
const ScanSentinelClass = "tabular-nums"

// scannedSourceExtensions are the file types a class name can hide in on this side of
// the repository: Go source, templ files, and the hand-written stylesheets.
//
// Deliberately a closed list rather than "every file": `docs/assets/plans` holds two
// large design records and `.toolbin` holds a 110MB binary, and walking either to
// find a twelve-character string would cost more than the assertion is worth.
var scannedSourceExtensions = []string{".go", ".templ", ".css"}

// TestTheScanSentinelIsNamedInNoOtherSource is what keeps the sentinel a sentinel.
//
// **Without it the sentinel is a comment.** The claim `internal/web`'s scan test makes
// is "a class declared only under `internal/web/plugins` reaches the built stylesheet",
// and that claim is only worth anything while the class is unique to this tree. A
// component that grows `tabular-nums` in a month would leave the coverage test green
// with the `@source` glob deleted — the exact vacuous pass this repository has paid for
// twice already — and nothing would say so.
//
// So this walks the Go, templ and CSS sources under `cmd/` and `internal/`, skips this
// file, and fails on the first other mention. It is a cheap test over a small tree, and
// it fails the moment the premise it rests on stops being true.
func TestTheScanSentinelIsNamedInNoOtherSource(t *testing.T) {
	t.Parallel()

	const self = "scan_sentinel_test.go"

	// `internal/web/plugins` is three levels below the repository root: plugins → web →
	// internal → root. Resolving it from the test's own directory rather than from the
	// working directory is what lets the walk name the roots below.
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}

	mentions := 0

	for _, dir := range []string{"cmd", "internal"} {
		walked, err := countMentions(t, filepath.Join(root, dir), self)
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}

		mentions += walked
	}

	if mentions != 0 {
		t.Errorf("%d source file(s) outside %s name %q. The sentinel's whole job is to be a "+
			"class nothing else declares: a second mention would make the stylesheet's scan "+
			"coverage pass with the @source glob deleted, which is the silent pass this "+
			"existence is meant to prevent", mentions, self, ScanSentinelClass)
	}
}

// builtArtefact reports whether path is a generated stylesheet rather than a source.
//
// **Excluded, and it is the whole subtlety of this test.** `static/dist/app.css` is
// the built output of the very scan this sentinel exists to check, so it *contains*
// the class — that is the proof working, not a second declaration. Counting it would
// make this test fail the moment `make css` ran, which is a test that reports a
// success as a defect.
//
// The path is matched on the directory rather than on `app.css` by name so a second
// built artefact in the same tree is covered by the same reasoning.
func builtArtefact(path string) bool {
	return strings.Contains(filepath.ToSlash(path), "/static/dist/")
}

// countMentions returns how many scanned source files below root name the sentinel,
// skipping the file that declares it.
//
// `filepath.WalkDir` over a closed extension list rather than `strings.Count` over a
// concatenated blob, because the count has to name a **file**: "somewhere in the
// repository" is a location an author cannot act on and this test exists to be acted on.
func countMentions(t *testing.T, root, self string) (int, error) {
	t.Helper()

	mentions := 0

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			//nolint:failfast // A directory this process cannot read is reported by the
			// caller as a walk failure; returning here would silently undercount and a
			// green uniqueness claim is the failure this test exists to prevent.
			return err
		}

		if entry.IsDir() || filepath.Base(path) == self || builtArtefact(path) {
			return nil
		}

		scanned := false

		for _, extension := range scannedSourceExtensions {
			if strings.HasSuffix(path, extension) {
				scanned = true

				break
			}
		}

		if !scanned {
			return nil
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", path, readErr)
		}

		if strings.Contains(string(raw), ScanSentinelClass) {
			mentions++
			t.Errorf("%s names the scan sentinel %q; it must be declared in exactly one place, "+
				"or the stylesheet's coverage assertion proves nothing", path, ScanSentinelClass)
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("walk %s: %w", root, err)
	}

	return mentions, nil
}
