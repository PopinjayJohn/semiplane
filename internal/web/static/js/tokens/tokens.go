// Package tokens holds the tabletop client's two modules and the one arithmetic
// kernel they share, and serves them to the page as inline script elements.
//
// # Why this package exists at all
//
// The JavaScript is a checked-in file because a person edits JavaScript rather
// than a Go string, and it is served inline because `/assets/` does not stage
// `static/js/` yet — the Makefile copies `static/css/` into `static/dist/` and
// nothing copies a module tree, so a `<script src>` for anything under this
// directory 404s and the behaviour silently does not arrive. Rather than depend
// on a Makefile change this work item does not own, `go:embed` carries the bytes
// into the binary and `play` writes them into the document.
//
// That is the `head.js` arrangement exactly: a file to edit, a constant to serve,
// and a test that fails the build when the two drift. `TestTheServedScriptsAreTheCheckedInScripts`
// is that test, and the bytes it compares are read from disk rather than from
// this package, so it is a comparison of two artefacts rather than a tautology.
//
// **If the integrator stages `static/js/` into `static/dist/`, the delivery here
// becomes unnecessary** — and this package becomes the one place the modules can
// still be read and tested from, which is worth keeping either way.
//
// # The three files, and the two that are not modules
//
// They are classic scripts rather than ES modules, in a fixed order, sharing one
// global per file (`spFocusStep`). A module graph would be tidier and cannot be
// used here: an inlined classic script is one element, and a module is a separate
// fetch, and §3.7's rule about the critical path plus the staging gap above make
// the second round trip not worth a naming convention.
//
// `step.js` is the third file and has no browser in it at all — it is one
// arithmetic expression, present so that Go can evaluate the *shipped bytes*
// over every input. `step_arith_test.go` is what reads it.
package tokens

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
)

// The three files, in the order they must run.
//
// **Order is a dependency and not a preference.** `tokens.js` calls `spFocusStep`,
// so `step.js` is first. The names are exported rather than spelled at each use
// so a rename fails the build instead of producing a script that loads and does
// nothing.
const (
	// StepFile is the arithmetic kernel.
	StepFile = "step.js"
	// RailTabsFile is D16's default tab and the tabs keyboard model.
	RailTabsFile = "railtabs.js"
	// TokensFile is the token list's keyboard model.
	TokensFile = "tokens.js"
)

// order is the sequence the page writes them in.
var order = []string{StepFile, RailTabsFile, TokensFile}

// The scripts, embedded rather than read from disk.
//
// `embed.FS` rather than three `//go:embed name` variables: the order is a list,
// and a list of `embed.FS` lookups is one list rather than a variable per file.
//
//go:embed step.js railtabs.js tokens.js
var sources embed.FS

// Source returns one file's exact bytes.
//
// A named lookup rather than a directory listing because the caller renders one
// `<script>` element per file, in order, and the order is a dependency — see
// `order`.
func Source(name string) string {
	raw, err := sources.ReadFile(name)
	if err != nil {
		// Unreachable for the three names above, and a panic is the right answer
		// for anything else: a template cannot handle an error from a script it
		// is about to write into a document, and a missing module means the
		// behaviour this page promised is absent rather than degraded.
		panic(fmt.Sprintf("tokens: %s is not embedded: %v", name, err))
	}

	return string(raw)
}

// Order returns the files in the order they must run.
func Order() []string {
	return slices.Clone(order)
}

// Names returns every embedded file, sorted, for the drift test to walk rather
// than for the renderer to iterate.
//
// A directory listing rather than `order`, and the distinction is the point: a
// new `.js` file dropped into this directory has to appear in *someone's*
// ordering, and the only assertion that can notice is one that reads the
// directory and compares it against the order.
func Names() []string {
	entries, err := fs.Glob(sources, "*.js")
	if err != nil {
		// Unreachable: the pattern is a literal over an `embed.FS` whose contents
		// are known at compile time.
		panic("tokens: cannot list the embedded modules: " + err.Error())
	}

	slices.Sort(entries)

	return entries
}
