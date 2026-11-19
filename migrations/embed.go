// Package migrations embeds ordered PostgreSQL DDL and seed SQL files.
package migrations

import "embed"

// Files holds versioned *.sql files applied sequentially on startup.
//
//go:embed *.sql
var Files embed.FS
