package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded frontend filesystem rooted at dist/.
func DistFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
