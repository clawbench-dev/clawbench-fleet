package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var assets embed.FS

// FS returns the built frontend. A placeholder index.html is kept in the
// repository so `go test ./...` and `go build` work before the first build.
func FS() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		return assets
	}
	return sub
}
