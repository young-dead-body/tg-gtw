package migrations

import "embed"

// FS embeds the SQL migration files so they can be applied at runtime
// without relying on external files being present alongside the binary.
//
//go:embed *.sql
var FS embed.FS
