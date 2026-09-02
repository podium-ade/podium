//go:build noui

// Package web is the stub half of the UI embed: `-tags noui` compiles the server with no assets
// at all, so a Go-only checkout never needs Node or pnpm.
package web

import (
	"embed"
	"io/fs"
)

// Enabled reports whether this build carries the UI.
const Enabled = false

// dist is the zero value of embed.FS, which is a valid empty tree: every Open returns
// fs.ErrNotExist, which is exactly what the handler treats as "no UI in this binary".
var dist embed.FS

// FS returns an empty asset tree.
func FS() (fs.FS, error) { return fs.Sub(dist, "dist") }
