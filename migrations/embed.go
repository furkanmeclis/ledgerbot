// Package migrations embeds ledgerbot's SQL migrations (goose format).
package migrations

import "embed"

// FS holds the migration files.
//
//go:embed *.sql
var FS embed.FS
