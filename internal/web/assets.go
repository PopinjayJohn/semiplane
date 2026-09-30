// Package web holds the server-rendered interface: templ components and the
// static assets they reference.
package web

import (
	"embed"
	"io/fs"
)

// The built stylesheet is embedded rather than read from disk. A container
// image has no working directory it can rely on, and a stylesheet that 404s
// because a path was wrong is a silent failure — the page renders, just
// unstyled, and every visual bug becomes a debugging session about CSS.
//
//go:embed all:static/dist
var distFS embed.FS

// Dist returns the built asset tree rooted at the stylesheet's own directory,
// so a caller can serve it as /assets/app.css without knowing the layout.
//
// fs.FS rather than fs.ReadFileFS: a caller mounting this in an
// http.FileServer needs only Open, and the extra methods would be an interface
// no production caller has a use for.
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "static/dist")
	if err != nil {
		// Unreachable: the embed directive above guarantees the directory
		// exists, and a failure here would mean the build is broken rather
		// than that a request is. Panicking beats returning an error every
		// caller would have to handle for a constant.
		panic("web: embedded dist tree missing: " + err.Error())
	}

	return sub
}
