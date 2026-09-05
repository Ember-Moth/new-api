// Package web embeds the built dashboard for the new-api executable.
package web

import "embed"

//go:embed dist
var Files embed.FS

//go:embed dist/index.html
var IndexPage []byte
