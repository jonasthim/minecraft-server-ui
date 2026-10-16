// Package web embeds the compiled browser application. Run make web before
// release builds; .gitkeep lets backend-only development use a fresh checkout.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

func Dist() (fs.FS, error) { return fs.Sub(dist, "dist") }
