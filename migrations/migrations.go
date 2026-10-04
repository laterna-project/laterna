// Package migrations embeds the versioned SQL schema (goose). The same files are the schema sqlc
// reads: a single source of truth.
package migrations

import "embed"

// FS holds the migrations, applied in the order of their number.
//
//go:embed *.sql
var FS embed.FS
