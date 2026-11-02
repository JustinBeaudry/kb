import { expect, test } from "bun:test";
import { parseRemoteEnvelope, parseRemoteStatus, renderRemoteFrame } from "../src/lib/remote-envelope";

const candidate = {
  schema_version: "3", chunks: [{ record_id: "claim", revision: "r", text: "猫", claim_status: "candidate", origin_trust: "submitted", evidence: [], warnings: ["unverified_task_candidate"] }], truncated: false, visited: 1, no_results: false,
};
test("schema 3 candidate labels and complete UTF-8 framing", () => {
  const result = parseRemoteEnvelope(candidate);
  expect(result?.chunks[0]?.claim_status).toBe("candidate");
  const frame = renderRemoteFrame(result!);
  const [length, body] = frame.split("\n");
  expect(Number(length)).toBe(Buffer.byteLength(body!));
  expect(Buffer.byteLength(frame)).toBe(Buffer.byteLength(body!) + length!.length + 1);
});
test("schema 3 validates every nested object and rejects ambiguous candidates", () => {
  expect(parseRemoteEnvelope({ ...candidate, schema_version: "2" })).toBeNull();
  expect(parseRemoteEnvelope({ ...candidate, chunks: [{ ...candidate.chunks[0], evidence: [{}] }] })).toBeNull();
  expect(parseRemoteEnvelope({ ...candidate, chunks: [{ ...candidate.chunks[0], warnings: [] }] })).toBeNull();
  expect(parseRemoteEnvelope({ ...candidate, extra: "untrusted" })).toBeNull();
});

test("no_results agrees with chunks, checkpoint and omitted task state", () => {
  expect(parseRemoteEnvelope({ ...candidate, no_results: true })).toBeNull();
  const empty = { ...candidate, chunks: [], no_results: true };
  expect(parseRemoteEnvelope(empty)).not.toBeNull();
  expect(parseRemoteEnvelope({ ...empty, no_results: false })).toBeNull();
  expect(parseRemoteEnvelope({ ...empty, task_state_omitted: true })).toBeNull();
  expect(parseRemoteEnvelope({ ...empty, truncated: true, task_state_omitted: true, no_results: false })).not.toBeNull();
  const checkpoint = { label: "task_state", checkpoint: { id: "id", revision: "r", scope: { project_id: "p" }, objective: "", completed: [], pending: [], questions: [], evidence_revision_ids: [], updated_at: "2026-01-01T00:00:00Z" } };
  expect(parseRemoteEnvelope({ ...empty, task_checkpoint: checkpoint })).toBeNull();
  expect(parseRemoteEnvelope({ ...empty, task_checkpoint: checkpoint, no_results: false })).not.toBeNull();
});

test("truncation metadata and task-state flags cannot contradict the envelope", () => {
  const checkpoint = { label: "task_state", checkpoint: { id: "id", revision: "r", scope: { project_id: "p", task_id: "t" }, objective: "", completed: [], pending: [], questions: [], evidence_revision_ids: [], updated_at: "2026-01-01T00:00:00Z" } };
  expect(parseRemoteEnvelope({ ...candidate, truncation_reason: "visited_limit" })).toBeNull();
  expect(parseRemoteEnvelope({ ...candidate, task_state_omitted: true })).toBeNull();
  expect(parseRemoteEnvelope({ ...candidate, truncated: true, task_state_omitted: true, task_checkpoint: checkpoint })).toBeNull();
  expect(parseRemoteEnvelope({ ...candidate, truncated: true, task_state_omitted: false, task_checkpoint: checkpoint })).not.toBeNull();
});

test("status accepts the server's advertised revision bound and rejects invalid bounds", () => {
  const status = { schema_version: "3", connection_state: "available", authorized_project_ids: ["p"], authorized_capabilities: ["status:read"], capability_gaps: [] };
  expect(parseRemoteStatus(status)?.max_revision_bytes).toBeUndefined();
  expect(parseRemoteStatus({ ...status, max_revision_bytes: 64 })?.max_revision_bytes).toBe(64);
  // A newer server's larger bound keeps status readable; writes enforce the limit.
  expect(parseRemoteStatus({ ...status, max_revision_bytes: 128 })?.max_revision_bytes).toBe(128);
  for (const bound of [0, -1, 1.5, "64", null]) expect(parseRemoteStatus({ ...status, max_revision_bytes: bound })).toBeNull();
});
