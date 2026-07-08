// Package webui embeds the built web/ SPA into the dictum-api binary.
//
// go:embed can only reach inside its own module tree, and web/ is a
// sibling directory outside api/'s module root — so the built web/dist
// can't be embedded directly from where Vite writes it. dist/ here is a
// build-time copy target: `npm run build` in web/ writes to web/dist, and
// the Docker build (and any local packaging step) copies that output into
// dist/ here before `go build` runs. dist/.gitkeep keeps the directory
// present in git so `go build`/`go test` succeed even before that copy
// step has ever run; the placeholder is silently shadowed once real
// assets are copied in.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded SPA build rooted at dist/, so paths like
// "index.html" and "assets/..." resolve without a dist/ prefix.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// dist/ is always present at compile time (go:embed guarantees it,
		// even if only dist/.gitkeep) — fs.Sub can't fail against it.
		panic(err)
	}
	return sub
}
