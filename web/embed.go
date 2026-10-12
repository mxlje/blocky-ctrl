// Package web holds the UI, embedded into the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// FS holds index.html (a template) and the assets it references.
var FS, _ = fs.Sub(static, "static")
