// Package webui embeds the built React application into the binary, so a
// deployment is a single file with nothing to serve from disk.
package webui

import (
	"embed"
	"io/fs"
)

// dist holds the Vite build output. The tracked placeholder index.html keeps
// this compiling before the frontend has ever been built.
//
//go:embed all:dist
var dist embed.FS

// FS returns the built site rooted at its index.html.
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
