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

//go:embed 007_decisions.cypher
var Decisions string

//go:embed 008_dreaming.cypher
var Dreaming string

//go:embed 009_lifecycle.cypher
var Lifecycle string

//go:embed 010_lessons.cypher
var Lessons string

// SupersedeBackfill retires verified project revisions that predate automatic
// supersession, keeping the newest published revision of each claim verified.
// It is idempotent and reruns on every schema initialization.
//
//go:embed 011_supersede_backfill.cypher
var SupersedeBackfill string

// Access holds the access audit rollup schema.
//
//go:embed 012_access.cypher
var Access string

// OwnerKeys holds the retired owner key history.
//
//go:embed 013_owner_keys.cypher
var OwnerKeys string

// OAuth holds dynamic client registrations for OAuth grants.
//
//go:embed 014_oauth.cypher
var OAuth string
