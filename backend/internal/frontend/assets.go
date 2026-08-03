// Package frontend exposes the Vue production build embedded in the Go binary.
// The checked-in dist/index.html is only a source-build placeholder; Docker and
// release workflows replace the entire dist directory with the real Vite build
// before compiling the backend.
package frontend

import (
	"embed"
	"io/fs"
)

//go:embed dist
var content embed.FS

// FS returns the embedded production web root.
func FS() fs.FS {
	root, err := fs.Sub(content, "dist")
	if err != nil {
		panic(err)
	}
	return root
}
