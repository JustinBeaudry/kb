import { expect, test } from "bun:test";
import { canonicalOrigin, HTTPMemoryGateway } from "../src/gateways/memory.gateway";
import { RemoteProfiles, RemoteService, type MemoryGateway } from "../src/services/remote.service";
import type { CaptureError, CredentialStore, Result } from "../src/capture/types";
import { renderRemoteFrame } from "../src/lib/remote-envelope";

const token = "b".repeat(64);
const cursor = (offset: number) => `${Buffer.from(JSON.stringify({ k: "passage", o: offset })).toString("base64url")}.signature`;
const empty = { schema_version: "3", chunks: [], truncated: false, visited: 0, no_results: true };
class Store implements CredentialStore {
  values = new Map<string, string>(); reads = 0;
  async get(p: string): Promise<Result<string, CaptureError>> { this.reads++; const v = this.values.get(p); return v ? { ok: true, value: v } : { ok: false, error: { code: "credential_unavailable" } }; }
  async set(p: string, v: string): Promise<Result<void, CaptureError>> { this.values.set(p, v); return { ok: true, value: undefined }; }
}
function gateway(origin: string, bearer: string): MemoryGateway { return new HTTPMemoryGateway(origin, bearer); }

test("native-account registration binds profile plus canonical origin without plaintext fallback", async () => {
  const store = new Store(); const profiles = new RemoteProfiles(store);
  expect((await profiles.register("p", "https://EXAMPLE.com:443/", token)).ok).toBe(true);
  expect((await profiles.connect("p", "https://example.com", gateway)).ok).toBe(true);
  expect(await profiles.connect("p", "https://other.example", gateway)).toMatchObject({ error: { code: "credential_unavailable" } });
  expect(await profiles.connect("other", "https://example.com", gateway)).toMatchObject({ error: { code: "credential_unavailable" } });
  expect((await profiles.register("p", "https://x.example", "invalid")).ok).toBe(false);
  expect(store.values.size).toBe(1);
  for (const url of ["http://localhost:1", "http://192.168.1.2", "https://user:pass@example.com", "https://example.com/path", "https://example.com/?q=a", "file:///etc/passwd"]) expect(canonicalOrigin(url)).toBeNull();
  expect(canonicalOrigin("http://[::1]:1234")).toBe("http://[::1]:1234");
});

test("HTTP errors are closed, bounded, never forwarded and redirects never receive bearer", async () => {
  let redirected = 0;
  const other = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch() { redirected++; return Response.json(empty); } });
  const responses: Array<[number, string, string]> = [[401, "anything", "unauthorized"], [403, "anything", "forbidden"], [409, "conflict", "conflict"], [409, "stale", "stale"], [410, "deleted", "deleted"], [413, "budget", "budget"], [400, "invalid_input", "invalid_input"], [503, "incomplete", "incomplete"], [500, "forbidden", "unavailable"]];
  let index = 0;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch(req) {
    expect(req.headers.get("Authorization")).toBe(`Bearer ${token}`);
    if (index === responses.length) return Response.redirect(`http://127.0.0.1:${other.port}/target`);
    const [status, code] = responses[index++]!;
    return Response.json({ code, message: "private server text" }, { status });
  } });
  try {
    const api = new HTTPMemoryGateway(`http://127.0.0.1:${server.port}`, token);
    for (const [, , code] of responses) {
      const result = await api.orient({ scope: { project_id: "p" }, budget_bytes: 4096 });
      expect(result.ok ? "unexpected success" : result.error.code).toBe(code);
      expect(JSON.stringify(result)).not.toContain("private server text");
    }
    expect(await api.orient({ scope: { project_id: "p" }, budget_bytes: 4096 })).toEqual({ ok: false, error: { code: "unavailable" } });
    expect(redirected).toBe(0);
  } finally { server.stop(true); other.stop(true); }
});

test("malformed schema, oversized and malformed UTF-8 responses fail closed", async () => {
  const bodies = [JSON.stringify({ ...empty, chunks: [{}] }), JSON.stringify({ ...empty, schema_version: "2" }), JSON.stringify({ ...empty, extra: "data" }), JSON.stringify({ ...empty, action: "\ud800" }), "x".repeat(5000), new Uint8Array([0xc3, 0x28])];
  let i = 0;
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: () => new Response(bodies[i++]!) });
  try {
    const api = new HTTPMemoryGateway(`http://127.0.0.1:${server.port}`, token);
    for (const _body of bodies) expect(await api.orient({ scope: { project_id: "p" }, budget_bytes: 4096 })).toEqual({ ok: false, error: { code: "unavailable" } });
  } finally { server.stop(true); }
});

test("fetch passes explicit working scope and UTF-8 passage metadata, preserving continuation", async () => {
  const source = "a猫".repeat(9000);
  const bytes = Buffer.from(source);
  const seen: URL[] = [];
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch(req) {
    const url = new URL(req.url); seen.push(url);
    const q = url.searchParams;
    const start = q.has("continuation") ? 400 : 0;
    const text = bytes.subarray(start, start + 400).toString("utf8");
    return Response.json({ schema_version: "3", chunks: [{ record_id: "artifact", revision: "digest", text, claim_status: "candidate", origin_trust: "submitted", evidence: [{ artifact_id: "artifact", digest: "digest", locator: "base", source_revision: "generation", origin: { client_id: "p", trust: "submitted" }, applicability: {} }], warnings: ["source_evidence_is_not_a_verified_claim", "unverified_task_evidence", "partial_source_passage"], passage: { start_byte: start, end_byte: start + 400, source_bytes: bytes.length } }], truncated: start === 0, visited: 0, no_results: false, ...(start === 0 ? { continuation: cursor(400), truncation_reason: "wire_budget" } : {}) });
  } });
  try {
    const service = new RemoteService(new HTTPMemoryGateway(`http://127.0.0.1:${server.port}`, token));
    const opts = { mode: "working" as const, project_id: "project", task_id: "task" };
    const first = await service.fetch("artifact", "base", 2048, opts);
    expect(first.ok).toBe(true);
    if (!first.ok) return;
    expect(Buffer.byteLength(renderRemoteFrame(first.value))).toBeLessThanOrEqual(2048);
    expect(first.value.chunks[0]?.passage).toEqual({ start_byte: 0, end_byte: 400, source_bytes: bytes.length });
    expect(first.value.chunks[0]?.warnings).toContain("unverified_task_evidence");
    expect(seen[0]?.searchParams.get("mode")).toBe("working");
    expect(seen[0]?.searchParams.get("project_id")).toBe("project");
    expect(seen[0]?.searchParams.get("task_id")).toBe("task");
    const next = await service.fetch("artifact", "base", 2048, { ...opts, continuation: first.value.continuation });
    expect(next.ok).toBe(true);
    expect(seen.at(-1)?.searchParams.get("continuation")).toBe(cursor(400));
    const count = seen.length;
    expect(await service.fetch("artifact", "base", 2048, { mode: "working", project_id: "other" })).toMatchObject({ error: { code: "invalid_input" } });
    expect(seen.length).toBe(count);
  } finally { server.stop(true); }
});


test("fetch enforces UTF-8 start bounds, cursor continuity and submitted source trust", async () => {
  const passage = (start: number, overrides: Record<string, unknown> = {}) => ({
    schema_version: "3", chunks: [{ record_id: "artifact", revision: "digest", text: "x", claim_status: "candidate", origin_trust: "submitted", evidence: [{ artifact_id: "artifact", digest: "digest", locator: "base", source_revision: "generation", origin: { client_id: "p", trust: "submitted" }, applicability: {} }], warnings: ["source_evidence_is_not_a_verified_claim", "partial_source_passage"], passage: { start_byte: start, end_byte: start + 1, source_bytes: 100 }, ...overrides }], truncated: false, visited: 0, no_results: false,
  });
  let body: unknown = passage(0);
  const send: typeof fetch = Object.assign(async () => Response.json(body), { preconnect: fetch.preconnect });
  const api = new HTTPMemoryGateway("https://example.com", token, send);
  const input = { id: "artifact", locator: "base", budget_bytes: 4096 };
  for (const offset of [0, 1, 2, 3]) {
    body = passage(10 + offset);
    expect((await api.fetch({ ...input, start_byte: 10 })).ok).toBe(true);
  }
  body = passage(14);
  expect((await api.fetch({ ...input, start_byte: 10 })).ok).toBe(false);
  body = passage(1);
  expect((await api.fetch(input)).ok).toBe(true); // source can start with a split rune
  body = passage(41);
  expect((await api.fetch({ ...input, continuation: cursor(40) })).ok).toBe(false);
  body = passage(40);
  expect((await api.fetch({ ...input, continuation: cursor(40) })).ok).toBe(true);
  expect((await api.fetch({ ...input, continuation: "not-a-passage.cursor" })).ok).toBe(false);
  body = passage(0, { origin_trust: "owner_authenticated" });
  expect((await api.fetch(input)).ok).toBe(false);
  body = passage(0, { evidence: [{ artifact_id: "artifact", digest: "digest", locator: "base", source_revision: "generation", origin: { client_id: "p", trust: "owner_authenticated" }, applicability: {} }] });
  expect((await api.fetch(input)).ok).toBe(false);
  body = { ...passage(0), truncated: true, continuation: cursor(2) };
  expect((await api.fetch(input)).ok).toBe(false);
  body = { ...passage(0), truncated: true, continuation: cursor(1) };
  expect((await api.fetch(input)).ok).toBe(true);
});

const claim = { record_id: "claim", revision: "revision", text: "claim text", claim_status: "verified", origin_trust: "submitted", evidence: [], warnings: [] };
const taskCheckpoint = { label: "task_state", checkpoint: { id: "checkpoint", revision: "revision", scope: { project_id: "project", task_id: "task", session_id: "earlier" }, objective: "resume", completed: [], pending: [], questions: [], evidence_revision_ids: [], updated_at: "2026-10-30T00:00:00Z" } };
const resultWith = (chunk: Record<string, unknown> = claim) => ({ ...empty, chunks: [chunk], visited: 1, no_results: false });
const rejected = { ok: false, error: { code: "unavailable" } } as const;

test("ordinary search rejects an invalidated claim and foreign checkpoint with hidden IDs", async () => {
  const body = { ...resultWith({ ...claim, claim_status: "invalidated" }), task_checkpoint: { ...taskCheckpoint, checkpoint: { ...taskCheckpoint.checkpoint, scope: { project_id: "foreign", task_id: "foreign" }, evidence_revision_ids: ["hidden-revision"] } } };
  const send: typeof fetch = Object.assign(async () => Response.json(body), { preconnect: fetch.preconnect });
  const api = new HTTPMemoryGateway("https://example.com", token, send);
  const response = await api.search({ scope: { project_id: "project", task_id: "task" }, budget_bytes: 4096 });
  expect(response).toEqual(rejected);
  expect(JSON.stringify(response)).not.toContain("hidden-revision");
});

test("retrieval rejects results outside the requested mode and scope", async () => {
  let body: unknown;
  const send: typeof fetch = Object.assign(async () => Response.json(body), { preconnect: fetch.preconnect });
  const api = new HTTPMemoryGateway("https://example.com", token, send);
  const base = { scope: { project_id: "project", task_id: "task", session_id: "current" }, budget_bytes: 4096 };
  const candidate = { ...claim, claim_status: "candidate", warnings: ["unverified_task_candidate"] };
  const scopedCheckpoint = (scope: Record<string, string>, ids: string[] = []) => ({ ...taskCheckpoint, checkpoint: { ...taskCheckpoint.checkpoint, scope, evidence_revision_ids: ids } });
  const cases: Array<{ name: string; operation: "orient" | "search"; input: typeof base & { mode?: "orientation" | "evidence" | "working"; max_visited?: number; continuation?: string }; response: unknown }> = [
    ...["superseded", "invalidated", "rejected"].map((status) => ({ name: status, operation: "search" as const, input: base, response: resultWith({ ...claim, claim_status: status }) })),
    { name: "default evidence search candidate", operation: "search", input: base, response: resultWith(candidate) },
    { name: "default orientation candidate", operation: "orient", input: base, response: resultWith(candidate) },
    { name: "evidence mode candidate", operation: "orient", input: { ...base, mode: "evidence" }, response: resultWith(candidate) },
    { name: "candidate lacks task scope", operation: "search", input: { ...base, scope: { project_id: "project", task_id: "", session_id: "current" }, mode: "working" }, response: resultWith(candidate) },
    { name: "source warning is not a working candidate label", operation: "search", input: { ...base, mode: "working" }, response: resultWith({ ...candidate, warnings: ["source_evidence_is_not_a_verified_claim"] }) },
    { name: "retrieval passage", operation: "search", input: base, response: resultWith({ ...claim, text: "x", passage: { start_byte: 0, end_byte: 1, source_bytes: 1 } }) },
    { name: "visited beyond request", operation: "search", input: { ...base, max_visited: 1 }, response: { ...resultWith(), visited: 2 } },
    { name: "search checkpoint in evidence mode", operation: "search", input: base, response: { ...resultWith(), task_checkpoint: taskCheckpoint } },
    { name: "explicit evidence checkpoint", operation: "orient", input: { ...base, mode: "evidence" }, response: { ...resultWith(), task_checkpoint: taskCheckpoint } },
    { name: "checkpoint without requested task", operation: "orient", input: { ...base, scope: { project_id: "project", task_id: "", session_id: "current" } }, response: { ...resultWith(), task_checkpoint: taskCheckpoint } },
    { name: "foreign project", operation: "orient", input: base, response: { ...resultWith(), task_checkpoint: scopedCheckpoint({ project_id: "foreign", task_id: "task" }) } },
    { name: "foreign task", operation: "orient", input: base, response: { ...resultWith(), task_checkpoint: scopedCheckpoint({ project_id: "project", task_id: "foreign" }) } },
    { name: "missing task", operation: "orient", input: base, response: { ...resultWith(), task_checkpoint: scopedCheckpoint({ project_id: "project" }) } },
    { name: "hidden checkpoint evidence IDs", operation: "orient", input: base, response: { ...resultWith(), task_checkpoint: scopedCheckpoint({ project_id: "project", task_id: "task" }, ["hidden-revision"]) } },
    { name: "checkpoint on continuation", operation: "orient", input: { ...base, continuation: "a.b" }, response: { ...resultWith(), task_checkpoint: taskCheckpoint } },
    { name: "task state omitted on continuation", operation: "orient", input: { ...base, continuation: "a.b" }, response: { ...resultWith(), truncated: true, task_state_omitted: true } },
    { name: "task state omitted in evidence mode", operation: "search", input: base, response: { ...resultWith(), truncated: true, task_state_omitted: true } },
    { name: "task state omitted without task", operation: "orient", input: { ...base, scope: { project_id: "project", task_id: "", session_id: "current" } }, response: { ...resultWith(), truncated: true, task_state_omitted: true } },
  ];
  for (const c of cases) {
    body = c.response;
    expect(await api[c.operation](c.input), c.name).toEqual(rejected);
  }
});

test("valid request-specific retrieval results preserve F5 wire behavior", async () => {
  let body: unknown;
  const send: typeof fetch = Object.assign(async () => Response.json(body), { preconnect: fetch.preconnect });
  const api = new HTTPMemoryGateway("https://example.com", token, send);
  const base = { scope: { project_id: "project", task_id: "task", session_id: "current" }, budget_bytes: 4096 };
  for (const operation of ["orient", "search"] as const) {
    for (const status of ["verified", "disputed"]) {
      body = resultWith({ ...claim, claim_status: status });
      expect((await api[operation](base)).ok).toBe(true);
    }
    body = resultWith({ ...claim, claim_status: "candidate", warnings: ["unverified_task_candidate"] });
    expect((await api[operation]({ ...base, mode: "working" })).ok).toBe(true);
    body = { ...empty, no_results: false, task_checkpoint: taskCheckpoint };
    expect((await api[operation]({ ...base, mode: "working" })).ok).toBe(true);
    body = { ...empty, no_results: false, truncated: true, task_state_omitted: true, truncation_reason: "task_state_budget" };
    expect((await api[operation]({ ...base, mode: "working" })).ok).toBe(true);
  }
  body = { ...empty, no_results: false, task_checkpoint: taskCheckpoint };
  expect((await api.orient(base)).ok).toBe(true); // Same task, earlier session.
  expect((await api.search({ ...base, mode: "orientation" })).ok).toBe(true);
  body = { ...empty, visited: 1, truncated: true, truncation_reason: "visited_limit" };
  expect((await api.search({ ...base, max_visited: 1 })).ok).toBe(true);
});

test("fetch rejects unrelated retrieval state", async () => {
  const chunk = { record_id: "artifact", revision: "digest", text: "x", claim_status: "candidate", origin_trust: "submitted", evidence: [{ artifact_id: "artifact", digest: "digest", locator: "base", source_revision: "generation", origin: { client_id: "p", trust: "submitted" }, applicability: {} }], warnings: ["source_evidence_is_not_a_verified_claim"], passage: { start_byte: 0, end_byte: 1, source_bytes: 1 } };
  let body: unknown;
  const send: typeof fetch = Object.assign(async () => Response.json(body), { preconnect: fetch.preconnect });
  const api = new HTTPMemoryGateway("https://example.com", token, send);
  const input = { id: "artifact", locator: "base", budget_bytes: 4096 };
  for (const extra of [{ visited: 1 }, { task_checkpoint: taskCheckpoint }, { truncated: true, task_state_omitted: true }]) {
    body = { ...resultWith(chunk), visited: 0, ...extra };
    expect(await api.fetch(input)).toEqual(rejected);
  }
});
