// Package frontend is the TV interface: plain HTML, CSS and JS in dist,
// with no build step. The desktop app embeds it, and airwavesd serves it
// at /tv/ for browsers and the Android TV app.
package frontend

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the interface's files, index.html at the root.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded, so it's there
	}
	return sub
}
