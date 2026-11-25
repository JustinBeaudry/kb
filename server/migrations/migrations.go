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

//go:embed 006_verification.cypher
var Verification string

//go:embed 009_lifecycle.cypher
var Lifecycle string

// SupersedeBackfill retires verified project revisions that predate automatic
// supersession, keeping the newest published revision of each claim verified.
// It is idempotent and reruns on every schema initialization.
//
//go:embed 011_supersede_backfill.cypher
var SupersedeBackfill string
