# ADR 0001: authoritative graph storage and API contracts

Date: 2026-10-29

Status: Neo4j Community qualified locally for F1. Object storage and production
operations remain separate qualification gates.

## Decision and pins

Use Neo4j as the authoritative graph for the private, single-owner service.
Evidence payloads belong in separately qualified private object storage. No
relational/document fallback is authorized by this decision.

| Component | Qualified pin |
| --- | --- |
| Go compiler | 1.27.3, darwin/arm64 |
| Neo4j Community | 2026.10.1, linux/arm64 |
| Graph image | `neo4j:2026.10.1-community@sha256:86f8e01402fd0d5fd23e8968375fefdc21284a5c1751fa3fa3de8091a08b6dd6` |
| Neo4j Go driver | `github.com/neo4j/neo4j-go-driver/v6 v6.4.0` |
| Echo | `github.com/labstack/echo/v4 v4.16.2` |
| oapi-codegen | `github.com/oapi-codegen/oapi-codegen/v2 v2.8.1` |
| Echo request validator | `github.com/oapi-codegen/echo-middleware v1.2.1` |
| OpenAPI library / generated runtime | `github.com/getkin/kin-openapi v0.152.0` / `github.com/oapi-codegen/runtime v1.8.0` |
| Integration harness | `github.com/testcontainers/testcontainers-go v0.44.0` |
| Assertions | `github.com/stretchr/testify v1.12.1` |
| Linter | `github.com/golangci/golangci-lint/v2 v2.15.0` |
| Docker server | 29.6.2 |

`deploy/versions.json`, `server/go.mod` and `server/go.sum` record these pins.
Generator and linter are module-pinned Go tools: run `go generate ./api` and
`go tool golangci-lint run` from `server/`. No global tool installation is needed.
Image qualification is architecture-specific; qualify an amd64 digest before
using one in deployment. The pinned arm64 image is not proof of Railway platform
compatibility. Official [driver compatibility](https://neo4j.com/docs/go-manual/current/install/)
covers the 6.x driver with the 2026.x server; the integration tests exercise the
actual pinned pair. [Release notes](https://neo4j.com/release-notes/)
and the [official image catalog](https://hub.docker.com/_/neo4j) were checked on
this ADR's date.

## Transaction correctness

Neo4j's default isolation is read committed. A plain `MATCH` and expected-revision
comparison can accept multiple writers. The real qualification red test observed
20 accepted / 0 conflicted transactions from the same expected head.

`graph.Store.Within` acquires the single private `OwnerState {id:'owner'}` write
lock before invoking a callback. A separate query sets and removes a dummy
property, consuming its result before any revision, authorization or deletion
read. Neo4j retains the write lock through commit/rollback. All shared decisions
must use this path and acquire this lock first; repository methods must never
open transactions. This follows the documented [explicit-lock protocol](https://neo4j.com/docs/operations-manual/current/database-internals/concurrent-data-access/).

The green race observed exactly 1 accepted / 19 conflicted transactions. The
initial green run took 467 ms for 20 contenders; a subsequent complete
qualification observed 451 ms. These are tiny-fixture measurements, not sustained
throughput or latency guarantees. The serial lock intentionally limits concurrency.

Callbacks run once in explicit transactions: there is no driver-managed callback
retry, so an ambiguous commit returns unavailable and callers reconcile using
idempotency keys. Network effects stay outside callbacks; jobs/outbox writes stay
inside the state transaction. Transactions have a 15-second server timeout and
cleanup has separate 5-second deadlines even after caller cancellation.
Opaque `domain.Tx` exposes only `TransactionID()`. Repository-only `Store.Unwrap`
rejects foreign or completed handles; a handle cannot escape its callback or be
used concurrently. Driver types do not enter domain or service ports.

Fencing qualification demonstrates rejecting an old worker/fence after a new
lease commits under the same lock, preserving the original head. Lease expiry,
claim scheduling, job deduplication and outbox execution belong to F3 and must
pass their own tests before use. Retrieval's final epoch validation and
bounded buffered serialization belong to F5; engine qualification alone cannot
establish those guarantees.

## Schema and search

`migrations/001_identity.cypher` creates idempotent uniqueness constraints for
opaque IDs, the `(clientID,idempotencyKey)` event key, job scheduling/scope indexes,
and a native fulltext claim index. `Open` applies this embedded initial migration
before returning a usable Store. Future migrations must follow the same
versioned source discipline and be applied before accepting traffic.

The real suite proves duplicate identities and natural event keys fail, and
schema reruns preserve existing heads. Application-required IDs still require
service-boundary validation: uniqueness does not enforce property existence on
Community. Domain identifiers are nonempty opaque strings, not UUID-only values
or Neo4j `elementId` values.

A depth-2 traversal with a 50-record output bound and the native fulltext index
were exercised with a 121-record fixture. This qualifies available Cypher/search
features; it does not prove authorization filtering, total visited-record
accounting, representative relevance or query resource bounds. No vector index
or second search service is selected.

## Offline backup and restore

Community [dump/load](https://neo4j.com/docs/operations-manual/current/backup-restore/planning/)
is offline. [Online backup](https://neo4j.com/docs/operations-manual/current/backup-restore/online-backup/)
is an Enterprise capability. Production must use an explicit maintenance window:
pause API mutations, drain transactions and workers, stop the graph cleanly,
dump both `neo4j` and `system`, encrypt/export artifacts and record a manifest.
Collectors keep spooling while the service is unavailable. A filesystem copy is
not a substitute for a database dump.

The real test runs `neo4j-admin database dump <database> --to-path=/backups`,
then `neo4j-admin database load <database> --from-path=/backups
--overwrite-destination=true` into a fresh volume with the same image. It starts
the restored database and compares opaque claim/revision/event IDs, relationships,
auth/deletion epochs, fence and tombstone identity, then checks restored
uniqueness. Both database dumps were actually loaded; no mocked restore was used.
The six-node/two-edge application fixture plus system dump occupied 48 KiB;
dump/load, restart and identity checks took 15.99 seconds after the source stopped.

A production recovery must additionally verify a graph/object consistency
manifest, object digests, deletion/tombstone reconciliation and separate encryption
key custody. A copied tombstone alone does not prove deletion cannot resurrect.
Daily backups, 30-day retention, RPO <=24 hours and RTO <=4 hours remain targets
until O2 exercises representative encrypted graph/object recovery. The local
fixture establishes no cloud durability or production acceptance.

## Resource and hosting limits

Measured image size: 378,233,561 bytes on arm64. With a 256 MiB initial heap,
512 MiB maximum heap and 128 MiB page cache, Docker reported 603.2 MiB memory
usage and 2.53% CPU shortly after starting the tiny race fixture. These are one
sample of container memory usage and CPU, not RSS, a peak or a production sizing
result. Containers run sequentially in tests, have bounded startup/command/cleanup
waits, and clean up only fixture containers and named volumes.

Before deployment, measure a representative dataset, restart/disk growth,
maintenance downtime, head contention, queue recovery and tail latency under an
explicit memory/CPU/disk cap. Record a hosting budget from those measurements;
no paid resources or price guarantees were created here.

Railway's [private networking](https://docs.railway.com/networking/private-networking)
provides internal connectivity; [Neo4j Docker configuration](https://neo4j.com/docs/operations-manual/current/docker/configuration/)
controls bind addresses and persistent paths. Deployment must qualify private
Bolt connectivity and DNS/address-family behavior, persist `/data`, avoid public
graph exposure, verify TLS and storage/backup encryption, and test volume recovery.
None of those live Railway checks ran in F1.

The [Neo4j Community source license](https://github.com/neo4j/neo4j/blob/2026.10/LICENSE.txt)
is GPL version 3. This selection is the unmodified Community server for private
operation, not Enterprise, clustering or a distribution of modified Neo4j.
Revisit licensing if redistribution, modifications or Enterprise features enter
scope. No commercial license was purchased.

## S3 qualification procedure, deferred to capture/operations

No object provider is qualified by this ADR. Before F4 accepts evidence, run its
object-store gateway against a pinned disposable S3-compatible server with
synthetic fixtures. Establish the provider's actual private access, API, deletion,
retention and encryption behavior; an S3-compatible label alone proves none of it.

1. Put/get known bytes; independently recompute SHA-256. Corrupt/truncate data and
   require failure. Exercise wrong digest/length, multipart completion and retry
   idempotency. Do not assume an ETag is a content digest. See official
   [integrity documentation](https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity.html).
2. Require anonymous and wrong-prefix reads/listing to fail. Qualify the exact
   provider's scoped credentials and signed-URL expiry. Never expose object keys,
   SDK types or storage credentials through API DTOs.
3. Test staged upload interruption before object completion, after upload but
   before graph commit, and after commit but before acknowledgement. A retry
   produces one usable artifact/event/job, or an explicit incomplete receipt.
4. Delete scoped evidence and any versions, replay delayed jobs/imports, and
   restore a pre-deletion backup. Tombstones must win; pinned retained evidence
   must not bypass explicit deletion. Qualify lifecycle handling and versioning.
5. Restore graph plus objects from a consistent manifest and verify every digest,
   reference, deletion boundary and missing-object failure. Only then measure
   representative RPO/RTO.
6. For a future approved live provider, verify TLS, encryption at rest, backup
   encryption and separate key custody from official provider evidence plus live
   checks. AWS's [SSE-S3 documentation](https://docs.aws.amazon.com/AmazonS3/latest/userguide/UsingServerSideEncryption.html)
   is not evidence that a compatible non-AWS endpoint encrypts at rest. Do not
   create cloud resources as part of local qualification.

## Wire contract and verification

`server/api/openapi.yaml` is the HTTP source of truth for all 15 requested paths
(18 operations), generated as strict Echo types in `internal/handler/api.gen.go`.
No API/worker runtime is installed by F1. Composition roots must register only
implemented handlers and return explicit unavailable problems for gaps; schema
presence never implies runtime success. F2 must install authentication and
`OapiRequestValidator` before any service operation is exposed.

Remote responses use `schema_version: "3"`. Claim status, origin trust and an
optional verification outcome are separate closed enums. A candidate has no
invented supported outcome. Submitted events/candidates/evidence cannot supply
authoritative Origin/status fields. Safe Problem DTOs include stable code,
message/action, request ID and runbook reference, with optional retry/minimum
budget metadata. Enrollment alone may return a one-time bearer; list/status never
include it.

Golden vectors cover candidate, verified, conflict, no-results, unavailable and
tiny budget, with evidence locator/revision bindings. OpenAPI contract tests reject
submitted authority fields and validate these vectors. HTTP JSON, CLI
length-prefixed output and MCP framing require independent complete-wire
accounting in F5/C3. Limits are 4 KiB orientation, 16 KiB evidence, depth 2 and
50 visited records. Local schema-v2 is unchanged.

Run from `server/`:

```sh
go generate ./api
go vet ./...
go test -race ./...
go tool golangci-lint run ./...
```

`go test -race ./internal/platform/graph -run TestQualified -v` requires real
Docker/Neo4j; missing infrastructure is a failure, never a silent skip. F1's
generated handler code has no maintained behavior and is excluded from the
application coverage floor; O2 owns CI enforcement for maintained code.
