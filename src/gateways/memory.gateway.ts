import { randomUUID } from "node:crypto";
import { validBearer } from "../capture/credentials";
import { operations, type OrientInput, type SearchInput, type FetchEvidenceQuery } from "../generated/memory-api";
import { parseRemoteEnvelope, parseRemoteStatus, type RemoteEnvelope, type RemoteStatus } from "../lib/remote-envelope";
import type { FetchInput, MemoryGateway, RemoteError, RemoteResult, RetrievalInput } from "../services/remote.service";

export function canonicalOrigin(value: string): string | null {
  try {
    const u = new URL(value);
    if (u.username || u.password || u.search || u.hash || u.pathname !== "/" || !(u.protocol === "https:" || (u.protocol === "http:" && ["127.0.0.1", "[::1]"].includes(u.hostname)))) return null;
    return u.origin;
  } catch { return null; }
}
const fail = (code: RemoteError["code"]): RemoteResult<never> => ({ ok: false, error: { code } });
async function readJSON(response: Response, limit: number): Promise<unknown> {
  if (!response.body) throw Error("missing response");
  const reader = response.body.getReader();
  const parts: Uint8Array[] = [];
  let total = 0;
  try {
    for (;;) {
      const part = await reader.read();
      if (part.done) break;
      total += part.value.byteLength;
      if (total > limit) throw Error("oversized response");
      parts.push(part.value);
    }
    return JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(Buffer.concat(parts)));
  } finally { await reader.cancel().catch(() => {}); }
}
function problem(value: unknown): string | undefined {
  if (value !== null && typeof value === "object" && "code" in value && typeof value.code === "string") return value.code;
  return undefined;
}
// The server authenticates this cursor. Decoding it here only checks that its
// publicly readable next offset agrees with the returned passage, not its MAC.
// Retrieval does not carry claim scope on the wire, so the server remains
// responsible for authorization. Reject the request contradictions we can see.
function validRetrievalForRequest(value: RemoteEnvelope, input: RetrievalInput, defaultMode: "orientation" | "evidence"): boolean {
  const mode = input.mode ?? defaultMode;
  if (input.max_visited !== undefined && value.visited > input.max_visited) return false;
  const canIncludeTaskState = mode !== "evidence" && !!input.scope.task_id && !input.continuation;
  if ((value.task_checkpoint || value.task_state_omitted) && !canIncludeTaskState) return false;
  const checkpoint = value.task_checkpoint?.checkpoint;
  // Task ownership is independent of session. A checkpoint from an earlier
  // session of the same project/task is legitimate, but its evidence IDs are not.
  if (checkpoint && (checkpoint.scope.project_id !== input.scope.project_id || checkpoint.scope.task_id !== input.scope.task_id || checkpoint.evidence_revision_ids.length !== 0)) return false;
  return value.chunks.every((chunk) => {
    if (chunk.passage) return false;
    if (chunk.claim_status === "verified" || chunk.claim_status === "disputed") return true;
    return chunk.claim_status === "candidate" && mode === "working" && !!input.scope.task_id && chunk.verification_outcome === undefined && chunk.warnings.includes("unverified_task_candidate");
  });
}
function passageOffset(cursor: string): number | null {
  try {
    const parts = cursor.split(".");
    if (parts.length !== 2 || !/^[A-Za-z0-9_-]+$/.test(parts[0]!) || !/^[A-Za-z0-9_-]+$/.test(parts[1]!)) return null;
    const payload: unknown = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(Buffer.from(parts[0]!, "base64url")));
    if (!payload || typeof payload !== "object" || Array.isArray(payload) || !("k" in payload) || payload.k !== "passage" || !("o" in payload) || typeof payload.o !== "number" || !Number.isSafeInteger(payload.o) || payload.o < 0 || payload.o > 8 * 1024 * 1024) return null;
    return payload.o;
  } catch { return null; }
}
export class HTTPMemoryGateway implements MemoryGateway {
  private readonly origin: string;
  constructor(origin: string, private readonly token: string, private readonly send: typeof fetch = fetch) {
    const normalized = canonicalOrigin(origin);
    if (!normalized || !validBearer(token)) throw Error("invalid configuration");
    this.origin = normalized;
  }
  private async request(path: string, body: OrientInput | SearchInput | undefined, budget: number): Promise<RemoteResult<unknown>> {
    try {
      const response = await this.send(this.origin + path, {
        method: body === undefined ? "GET" : "POST",
        headers: { Authorization: `Bearer ${this.token}`, "Content-Type": "application/json", "X-Request-Id": randomUUID() },
        body: body === undefined ? undefined : JSON.stringify(body),
        redirect: "error", signal: AbortSignal.timeout(20_000),
      });
      if (response.status >= 300 && response.status < 400) return fail("unavailable");
      if (response.status === 200) return { ok: true, value: await readJSON(response, budget) };
      let code: string | undefined;
      try { code = problem(await readJSON(response, 65536)); } catch { /* never forward remote text */ }
      if (response.status === 401) return fail("unauthorized");
      if (response.status === 403) return fail("forbidden");
      if (response.status === 409) return fail(code === "conflict" ? "conflict" : "stale");
      if (response.status === 410) return fail("deleted");
      if (response.status === 413 && code === "budget") return fail("budget");
      if (response.status === 400 || response.status === 413) return fail("invalid_input");
      if (response.status === 503 && code === "incomplete") return fail("incomplete");
      return fail("unavailable");
    } catch { return fail("unavailable"); }
  }
  private async retrieval(path: string, body: OrientInput | SearchInput | undefined, budget: number): Promise<RemoteResult<RemoteEnvelope>> {
    const result = await this.request(path, body, budget);
    if (!result.ok) return result;
    const value = parseRemoteEnvelope(result.value);
    if (!value || (value.continuation !== undefined && !/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(value.continuation))) return fail("unavailable");
    return { ok: true, value };
  }
  async orient(input: RetrievalInput & { budget_bytes: number }): Promise<RemoteResult<RemoteEnvelope>> {
    const wire: OrientInput = input;
    const result = await this.retrieval(operations.orientMemory.path, wire, input.budget_bytes);
    return result.ok && !validRetrievalForRequest(result.value, input, "orientation") ? fail("unavailable") : result;
  }
  async search(input: RetrievalInput & { budget_bytes: number }): Promise<RemoteResult<RemoteEnvelope>> {
    const wire: SearchInput = input;
    const result = await this.retrieval(operations.searchMemory.path, wire, input.budget_bytes);
    return result.ok && !validRetrievalForRequest(result.value, input, "evidence") ? fail("unavailable") : result;
  }
  async fetch(input: FetchInput & { budget_bytes: number }): Promise<RemoteResult<RemoteEnvelope>> {
    const { id, ...params } = input;
    const wire: FetchEvidenceQuery = params;
    const q = new URLSearchParams();
    for (const [key, value] of Object.entries(wire)) if (value !== undefined) q.set(key, String(value));
    const result = await this.retrieval(operations.fetchEvidence.path.replace("{id}", encodeURIComponent(id)) + "?" + q, undefined, input.budget_bytes);
    if (!result.ok) return result;
    const c = result.value.chunks[0];
    const ref = c?.evidence[0];
    const expectedStart = input.continuation ? passageOffset(input.continuation) : input.start_byte ?? 0;
    const nextOffset = result.value.continuation ? passageOffset(result.value.continuation) : undefined;
    if (result.value.no_results || result.value.visited !== 0 || result.value.task_checkpoint || result.value.task_state_omitted || result.value.chunks.length !== 1 || !c || !c.passage || c.record_id !== id || c.claim_status !== "candidate" || !c.warnings.includes("source_evidence_is_not_a_verified_claim") || (input.mode === "working" && !c.warnings.includes("unverified_task_evidence")) || c.evidence.length !== 1 || !ref || ref.artifact_id !== id || ref.locator !== input.locator || ref.digest !== c.revision || c.origin_trust !== "submitted" || ref.origin.trust !== "submitted" || expectedStart === null || c.passage.start_byte < expectedStart || c.passage.start_byte > expectedStart + (input.continuation ? 0 : 3) || (nextOffset !== undefined && (nextOffset === null || nextOffset !== c.passage.end_byte || nextOffset <= c.passage.start_byte)) || (input.end_byte !== undefined && c.passage.end_byte > input.end_byte)) return fail("unavailable");
    return result;
  }
  async status(): Promise<RemoteResult<RemoteStatus>> {
    const result = await this.request(operations.getStatus.path, undefined, 65536);
    if (!result.ok) return result;
    const value = parseRemoteStatus(result.value);
    return value ? { ok: true, value } : fail("unavailable");
  }
}
