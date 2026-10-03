// Package mapjs holds the tabletop's map client's shipped modules.
//
// # Why this package exists
//
// C1 shipped four `.js` files and their Go-side gates, and no accessor — so
// nothing outside this directory could name them, and the page that mounts the
// canvas had no way to reference the modules that draw it. `TestTheMapClientIsServed`
// caught the consequence: `components/play` mounted `[data-map-surface]`, and
// `table.js` was never loaded, so the canvas stayed blank with every gate green.
//
// The accessor mirrors `static/js/live` and `static/js/tokens` exactly, and the
// mirroring is the point rather than the tidiness: three module trees with three
// different ways of naming their files is three answers to "what does this page
// load", and `TestEveryModuleTreeThePageDependsOnIsServed` walks the directories
// and requires each file to appear, which is only possible if each tree can say
// what it holds.
//
// # The order is a dependency
//
// `table.js` imports from the other three and nothing imports `table.js`, so the
// order `Order()` returns is the order the module graph resolves in. It is
// returned as a clone for the same reason `tokens.Order()` is: a caller that
// mutated the slice would corrupt the next page's load order, and that is the kind
// of shared mutable state `determinism` forbids in rule code.
//
// `Names()` exists separately from `Order()` for the same reason it does in the
// other two trees: a `.js` file dropped into this directory has to appear in
// *someone's* audit, and a listing is what makes that checkable.

package mapjs

import (
	"embed"
	"fmt"
	"io/fs"
	"slices"
)

// The files, in the order the page loads them.
//
// `TableFile` is **last** and the names are exported rather than spelled at each
// use so that a rename fails the build instead of producing a 404 at runtime.
const (
	CameraFile  = "camera.js"
	PaletteFile = "palette.js"
	SceneFile   = "scene.js"
	TableFile   = "table.js"
)

// order is the module graph's resolution order, and `TableFile` is last because
// it imports the other three.
//
// The names are the file names on purpose. `Href` builds a URL from the same
// constant, so a module's name and its address cannot disagree — the failure
// being a script element pointing at a path that 404s, which is a blank map and
// no error anywhere.
var order = []string{CameraFile, PaletteFile, SceneFile, TableFile}

//go:embed *.js
var sources embed.FS

// Source returns a module's shipped bytes.
//
// Panicking on an unknown name, for the reason `tokens.Source` does: a template
// cannot handle an error from a script it is about to reference, and a missing
// module means the behaviour this page promised is **absent** rather than
// degraded. A blank canvas with no console error is the worst of the three
// outcomes, and a panic at boot is the one that cannot be shipped silently.
func Source(name string) string {
	raw, err := sources.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("map: %s is not embedded: %v", name, err))
	}

	return string(raw)
}

// Order returns the modules in load order, as a clone.
func Order() []string {
	return slices.Clone(order)
}

// Names returns every embedded file, sorted, for the drift test to walk rather
// than for the renderer to iterate.
func Names() []string {
	found, err := fs.Glob(sources, "*.js")
	if err != nil {
		// Unreachable for an embed.FS with a constant pattern, and a panic is the
		// right answer for the same reason `Source` panics.
		panic(fmt.Sprintf("map: list the embedded modules: %v", err))
	}

	slices.Sort(found)

	return found
}

// AssetPrefix is where `make stage-assets` publishes this tree.
//
// A constant and not a derived string, because the URL is load-bearing — it is
// what a `<script src>` carries — and a prefix assembled from a relative path at
// two call sites is a second place to get the leading slash wrong.
const AssetPrefix = "/assets/js/map/"

// Href is a module's served address.
func Href(name string) string {
	return AssetPrefix + name
}
