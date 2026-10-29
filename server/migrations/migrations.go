// Package migrations embeds the authoritative initial graph schema.
package migrations

import _ "embed"

// Identity is idempotent and must finish before accepting service traffic.
//
//go:embed 001_identity.cypher
var Identity string

//go:embed 002_clients.cypher
var Clients string

//go:embed 003_jobs.cypher
var Jobs string

//go:embed 004_evidence.cypher
var Evidence string

//go:embed 005_memory.cypher
var Memory string
