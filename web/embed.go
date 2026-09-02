//go:build !noui

// Package web carries the built single-page UI into the server binary. The Go tree deliberately
// depends on nothing else in web/: everything under node_modules and src is build input for
// pnpm, and dist/ is its output.
package web

import (
	"embed"
	"io/fs"
)

// Enabled reports whether this build carries the UI. A `-tags noui` build sets it false.
const Enabled = true

// dist is the pnpm build output. The `all:` prefix is required to pick up dot-files, which is
// how the committed dist/.gitkeep placeholder gets in: //go:embed of an empty or missing
// directory is a compile error, so a fresh clone that has never run pnpm would otherwise fail
// `go build ./...`. With the placeholder the build always succeeds and the handler simply finds
// no index.html.
//
//go:embed all:dist
var dist embed.FS

// FS returns the asset tree rooted at dist/.
func FS() (fs.FS, error) { return fs.Sub(dist, "dist") }
