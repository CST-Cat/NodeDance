// Package webassets embeds the production Web build into the Core binary.
package webassets

import "embed"

// Files is populated by `pnpm build` before the Go binary is compiled.
//
//go:embed all:dist
var Files embed.FS
