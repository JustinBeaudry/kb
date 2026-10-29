# Capture and Harness Clients Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Automatically collect permitted evidence without blocking foreground work, and expose the service to portable clients.

**Architecture:** A local collector filters before spooling/network access. Harness adapters supply normalized exposed events; explicit remote CLI and MCP operations use the common API. Local vault semantics remain unchanged.

**Tech Stack:** Existing Bun/strict TypeScript, Go MCP handler, OpenAPI-generated client, OS credential storage; no model calls in collectors.

**Spec:** [Approved design](../specs/2026-10-29-persistent-memory-design.md); [shared contracts](2026-10-29-persistent-memory.md).

## Global Constraints

Shared plan constraints apply verbatim. Filter before upload/spooling; raw history 90 days, inactive task state 30 days. Preserve schema-v2 local behavior; use explicit remote profile and schema-v3. No historical raw/session import by default. Credentials never appear in argv, logs, fixtures, or model-visible setup output.

## Review Focus

- Secret split across chunks or nested tool results: redaction precedes chunk boundaries (C1).
- Pause/exclusion change while offline: old queued content cannot transmit under stale policy (C1).
- Transcript rotation, partial JSON, and repeated hook delivery: no invented completeness or duplicate event (C2).
- Remote failure: report it instead of silently substituting local facts (C3).
- Symlinked import source or restored deleted ID: deny traversal and resurrection (C4).

### Task C1: Filtered capture policy and bounded offline spool

**Files:** Create `src/capture/{types.ts,policy.ts,spool.ts,uploader.ts,credentials.ts}`, `src/services/capture.service.ts`, `src/commands/capture.ts`, `tests/{capture-policy,capture-spool,capture-upload}.test.ts`; modify `src/cli.ts` to add the explicit `capture` command.

**Interfaces:** `filterEvent(event: CaptureEvent, policy: CapturePolicy, knownSecrets: readonly string[]): FilterOutcome`; FilterOutcome is accepted filtered event, withheld metadata-only reason, or excluded metadata-only marker. `CaptureService.enqueue(event: CaptureEvent): Promise<Result<Receipt,CaptureError>>`, `flush(signal: AbortSignal): Promise<Result<FlushReport,CaptureError>>`, `pause(): Promise<Result<void,CaptureError>>`. CaptureEvent mirrors shared Event plus exposed content before filtering; CapturePolicy mirrors server policy plus local exclusions. Local exclusions can only narrow server scope. Spool interface: appendFiltered/readBatch/acknowledge with stable event IDs; uploader gateway sends filtered events through F4.

- [ ] Write `redacts known secrets before any disk or network write`: assert neither spool bytes nor fake gateway payload contain sentinel secret; include nested arrays, multiline values, Unicode and cross-output-chunk patterns. Write `pause suppresses queued transmission`, `resume does not backfill pause`, `new exclusions purge queued payload`, `quota reports dropped event`, `missing policy blocks upload`.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```typescript
expect(spoolBytes).not.toContain(secret);
expect(sentPayloads).toHaveLength(0); // paused case
expect(report.gaps).toHaveLength(1); // overflow case
```

- [ ] Run `bun test tests/capture-policy.test.ts tests/capture-spool.test.ts tests/capture-upload.test.ts`; confirm expected assertions fail.
- [ ] Implement whole-event filtering before chunking, 8 MiB chunk cap compatible with F4, bounded parsing, restrictive permissions, atomic spool writes, local metadata-only losses, configurable 256 MiB spool default, and cancellation-aware exponential retry. On overflow refuse newest payload and record sequence gap; never block the harness or silently overwrite old data. Pause atomically halts dispatch and enqueues no new content; already acknowledged transmissions remain stored. Recheck policy before each send; expired/missing policy withholds content. OS credential failures surface status, not environment-secret dumps.
- [ ] Rerun tests including kill/restart and lost-ack replay; assert one server event via F4 idempotency. Inspect spool fixture bytes for seeded secrets. Keep unidentified secret detection explicitly imperfect.
- [ ] Commit as `feat: capture filtered events with offline-safe spooling`.

### Task C2: Normalize Codex and Claude events with honest capability reporting

**Files:** Create `src/adapters/{types.ts,codex.ts,claude.ts,generic.ts}`, `src/capture/{cursor.ts,watcher.ts}`, `tests/{capture-adapters,capture-resume}.test.ts`, `tests/fixtures/capture/{codex,claude,generic}.jsonl`, `docs/integrations/harness-matrix.md`; modify `hooks/hooks.json`, `hooks/run-hook.cmd`, `src/hooks/inject.ts` only for explicit remote opt-in dispatch.

**Interfaces:** `decodeEvents(line: string, source: SourceIdentity): DecodeResult` per adapter; DecodeResult has normalized events, ignored reason, malformed flag and capability gaps. `SourceIdentity` has client/session ID, source file identity, byte offset and adapter version. `CaptureWatcher.poll(signal: AbortSignal): Promise<Result<PollReport,CaptureError>>`; `restoreContext(scope: Scope, budgetBytes: number): Promise<Result<string,CaptureError>>`. Reuse C1 CaptureService and C3 client when available; before C3 use a gateway fake at its boundary.

- [ ] Write cases `retains tool results without accepting them as verified`, `deduplicates paired Codex records`, `does not consume incomplete trailing JSON`, `rotation records gap rather than inventing old events`, `owner role remains submitted`, and `local hooks preserve previous output without remote profile`. Assertions include `trust === 'submitted'`, stable event IDs, exact role ordering and explicit completeness flags.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```typescript
expect(decoded.events[0]?.origin.trust).toBe("submitted");
expect(new Set(eventIds).size).toBe(eventIds.length);
expect(report.completeness).toBe("partial"); // rotated source case
```

- [ ] Run `bun test tests/capture-adapters.test.ts tests/capture-resume.test.ts tests/transcript-portability.test.ts tests/inject.test.ts`; confirm new cases red and existing cases unchanged.
- [ ] Implement separate remote event decoders; do not change the legacy excerpt parser to suddenly include tools. Qualify pinned harness capabilities from official docs and local synthetic sessions. Never tail unselected directories or historical sessions. Native hooks are preferred where present; otherwise explicitly registered background watcher uses new records only. Record capture, restore, tool evidence, compaction notification and authenticity independently. Mark mutable transcript evidence submitted; collector credentials alone do not make it attested. Enable a trusted route only with independent demonstrated isolation and actual invocation observation.
- [ ] Run synthetic cross-harness resume/rotation/offline fixtures and existing local tests. Matrix cells say supported, unsupported, or unverified with evidence paths; unsupported automatic restoration is a visible limitation, not a fabricated hook. Live private qualification occurs in O3.
- [ ] Commit as `feat: adapt harness events and restore scoped context`.

### Task C3: Explicit remote CLI and MCP adapter

**Files:** Create `src/commands/remote.ts`, `src/services/remote.service.ts`, `src/gateways/memory.gateway.ts`, `src/lib/remote-envelope.ts`, `src/generated/memory-api.ts`, `tests/{remote-cli,remote-envelope,remote-service}.test.ts`, `server/internal/handler/{mcp.handler.go,mcp.handler_test.go}`, `docs/integrations/remote.md`; modify `src/cli.ts`, `package.json`, `skills/kb/references/query.md`, `README.md`.

**Interfaces:** `RemoteService.orient(input: RetrievalInput): Promise<Result<RemoteEnvelope,RemoteError>>`, `search(input: RetrievalInput): Promise<Result<RemoteEnvelope,RemoteError>>`, `fetch(id: string, locator: string, budgetBytes: number): Promise<Result<RemoteEnvelope,RemoteError>>`, `status(): Promise<Result<RemoteStatus,RemoteError>>`. RetrievalInput mirrors F5 request; generated OpenAPI types stay inside MemoryGateway. RemoteStatus contains connection state, authorized project IDs and capability gaps. RemoteEnvelope is a validated schema-v3 DTO separate from local Envelope. `NewMCPServer(retrieval RetrievalReader, memory CandidateWriter, checkpoints CheckpointWriter, status StatusReader) *MCPServer` and `(*MCPServer).ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error`; consumer interfaces use F4/F5 signatures and an authenticated status service, not DB clients. Create `server/cmd/kb-mcp/main.go` as the stdio composition root. Remote HTTP MCP remains unadvertised until independently authenticated and qualified. Tools: memory_orient/search/fetch/submit_candidate/checkpoint/status. No promotion, arbitrary query language, or owner-admin tool.

- [ ] Write `remote mode never falls back to local`, `local map output remains schema 2`, `remote envelope labels candidates`, `MCP framing obeys caller budget`, `MCP tool cannot request owner capability`; assert exact error status and no extra local reads. Test absent/expired credentials and revoked evidence cursor across HTTP, CLI, MCP.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```typescript
expect(localResult.schema_version).toBe("2");
expect(remoteResult.schema_version).toBe("3");
expect(localGatewayReads).toBe(0); // remote failure case
```

- [ ] Run `bun test tests/remote-*.test.ts` and `go test -race ./internal/handler -run TestMCP -v`; confirm red.
- [ ] Implement `kb remote {orient,search,fetch,status}` and explicit profile selection with OS-held tokens; preserve existing commands. Generate TS client from F1 OpenAPI. Implement stdio MCP first using per-client credential/profile; qualify remote MCP authentication separately before advertising it. Count final rendered tool framing and reduce content accordingly, returning an explicit tiny-budget failure when necessary. Retrieval data is never concatenated into instruction roles.
- [ ] Run remote contract tests, Go gate and TS gate; exercise actual API using synthetic projects. Run `/devex-review` on CLI help, credential failures, source citations and continuation. Update query guide with explicit local vs remote selection and no claim of universal native hooks.
- [ ] Commit as `feat: expose graph memory through remote CLI and MCP`.

### Task C4: Selected import and portable export

**Files:** Create `src/commands/transfer.ts`, `src/services/transfer.service.ts`, `src/repositories/vault.repository.ts`, `tests/transfer.test.ts`, `server/internal/service/{transfer.service.go,transfer.service_test.go}`, `server/internal/handler/transfer.handler.go`, `docs/runbooks/migration.md`; modify `src/cli.ts`, OpenAPI.

**Interfaces:** TS `TransferService.preview(selection: ImportSelection): Promise<Result<ImportManifest,TransferError>>`, `apply(manifest: ImportManifest, approvalDigest: string): Promise<Result<ImportReport,TransferError>>`. Selection includes explicit vault path, project binding and selected file IDs; raw/session inclusion is false unless explicitly selected. Manifest contains source hashes/locators, scope and policy digest; changes invalidate approval. Go `TransferService.Export(ctx,p Principal,scope Scope) (ExportManifest,error)` uses authorized claim/artifact streams; exports carry statuses and provenance. Import calls F4/F5, never a verified-state shortcut.

- [ ] Write `preview reads no unselected raw/session content`, `changed manifest requires new approval`, `curated import remains candidate`, `symlink escape is denied`, `retry creates no duplicate`, `export import preserves provenance without trust escalation`, `deleted source identity cannot resurrect`; assert zero unselected reads using repo-boundary fake, not filesystem primitive mocking.

Assertion anchors for the separate named cases above (fixture variables come from that case):

```typescript
expect(report.imported.every(item => item.status === "candidate")).toBe(true);
expect(unselectedSourceReads).toEqual([]);
expect(changedManifestResult.error.code).toBe("approval_stale");
```

- [ ] Run `bun test tests/transfer.test.ts` and `go test -race ./internal/service -run TestTransfer -v`; confirm red.
- [ ] Implement dry-run inventory, explicit manifest approval, content-hash idempotency, path safety and tombstone checks. Never rewrite vault originals. Selected import reports omissions/errors and partial completion; export is resumable and authorized per chunk. Document filtering and provenance loss where local evidence was already incomplete.
- [ ] Run round-trip against a synthetic vault with duplicate and conflicting records; run TS/Go gates. Existing user vault is not used for a test. Actual selected migration waits for onboarding selection.
- [ ] Commit as `feat: import and export scoped memory with provenance`.
