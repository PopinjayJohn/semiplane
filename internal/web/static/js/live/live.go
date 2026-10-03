// Package live holds the live chrome's client modules and the one arithmetic
// kernel they share.
//
// # Why this package exists at all
//
// The JavaScript is a checked-in file because a person edits JavaScript rather than
// a Go string, and it is served from `/assets/` because `make stage-assets` copies
// `static/js/` into `static/dist/` with its layout intact — a `<script>` per file,
// in the order the imports demand. That is the arrangement `static/js/map` uses and
// the one the C2 report describes as a workaround for a staging gap which has since
// been closed, so this package takes the better one: two real files at two real
// URLs rather than a file and a compiled copy of it.
//
// `TestTheServedModulesAreTheCheckedInModules` compares the embedded bytes against
// the bytes on disk, and it reads the disk copy rather than this package's, so the
// comparison is of two artefacts rather than a tautology.
//
// # The two files, and the one that has no browser in it
//
// They are ES modules with an import between them, so the order is a dependency and
// not a preference: `chrome.js` imports `spFollowGap` from `gap.js`.
//
// `gap.js` is the third file and has no browser in it at all — one arithmetic
// expression, present so that Go can evaluate the *shipped bytes*. `gap_arith_test.go`
// is what reads it, and the reason is the note on `make a11y`: there is no Node in
// this repository, deliberately, so the alternative to evaluating the file is not
// running it and hoping.
//
// `source_test.go` covers the rest — the claims that are structural rather than
// arithmetic: one WebSocket, no second transport, no client-authored markup — by
// reading the shipped bytes with the comments blanked, and every one of its audits
// is paired with a mutation.
package live

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
)

// The files, in the order the page loads them.
//
// `GapFile` is first because `chrome.js` imports it, and the names are exported
// rather than spelled at each use so a rename fails the build instead of producing a
// module that loads and does nothing.
const (
	// GapFile is the arithmetic kernel.
	GapFile = "gap.js"
	// ChromeFile is the connection.
	ChromeFile = "chrome.js"
)

// order is the sequence the page loads them in.
var order = []string{GapFile, ChromeFile}

// The modules, embedded rather than read from disk.
//
// `embed.FS` rather than one `//go:embed name` per file, for the reason
// `static/js/tokens` gives: the order is a list, and a list of `embed.FS` lookups is
// one list rather than a variable per file.
//
//go:embed gap.js chrome.js
var sources embed.FS

// Source returns one file's exact bytes.
//
// A named lookup rather than a directory listing because the caller renders one
// `<script>` element per file, in order, and the order is a dependency — see
// `order`.
func Source(name string) string {
	raw, err := sources.ReadFile(name)
	if err != nil {
		// Unreachable for the names above, and a panic is the right answer for
		// anything else: the behaviour this page promised would be absent rather than
		// degraded, and a template cannot handle an error from a script it is about to
		// write into a document.
		panic(fmt.Sprintf("live: %s is not embedded: %v", name, err))
	}

	return string(raw)
}

// Order returns the files in the order they must run.
func Order() []string {
	return slices.Clone(order)
}

// Names returns every embedded file, sorted, for the drift test to walk rather than
// for the renderer to iterate.
//
// A directory listing rather than `order`, and the distinction is the point: a new
// `.js` file dropped into this directory has to appear in *someone's* ordering, and
// the only assertion that can notice is one that reads the directory and compares it
// against the order.
func Names() []string {
	entries, err := fs.Glob(sources, "*.js")
	if err != nil {
		// Unreachable: the pattern is a literal over an `embed.FS` whose contents are
		// known at compile time.
		panic("live: cannot list the embedded modules: " + err.Error())
	}

	slices.Sort(entries)

	return entries
}

// AssetPrefix is the URL prefix a module is served at, for a play document that loads
// it.
//
// **`/assets/js/live/<name>`**, and it is spelled here rather than left to the caller
// because the layout under `static/dist/` is `make stage-assets`'s decision and this
// is the one place in the tree that has to agree with it. `make css` and
// `make stage-assets` are the integrator's; a `<script src>` written at a call site
// is a second spelling of the same path, and the failure when the two drift is a
// 404 that is silently absent behaviour.
const AssetPrefix = "/assets/js/live/"

// Href is the URL one module is served at.
func Href(name string) string {
	return AssetPrefix + name
}
