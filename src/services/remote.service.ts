import { createHash } from "node:crypto";
import { validBearer, validProfile } from "../capture/credentials";
import type { CredentialStore } from "../capture/types";
import { canonicalOrigin } from "../gateways/memory.gateway";
import { renderRemoteFrame, type Applicability, type RemoteEnvelope, type RemoteScope, type RemoteStatus } from "../lib/remote-envelope";

export type RemoteError = { code: "invalid_input" | "credential_unavailable" | "unauthorized" | "forbidden" | "stale" | "conflict" | "deleted" | "incomplete" | "budget" | "unavailable" };
export type RemoteResult<T> = { ok: true; value: T } | { ok: false; error: RemoteError };
export type RetrievalInput = { scope: RemoteScope; query?: string; entity_ids?: string[]; applicability?: Applicability; budget_bytes?: number; max_depth?: number; max_visited?: number; continuation?: string; mode?: "orientation" | "evidence" | "working" };
export type FetchInput = { id: string; locator: string; budget_bytes?: number; mode?: "publication" | "working"; project_id?: string; task_id?: string; session_id?: string; start_byte?: number; end_byte?: number; continuation?: string };
export interface MemoryGateway {
  orient(input: RetrievalInput & { budget_bytes: number }): Promise<RemoteResult<RemoteEnvelope>>;
  search(input: RetrievalInput & { budget_bytes: number }): Promise<RemoteResult<RemoteEnvelope>>;
  fetch(input: FetchInput & { budget_bytes: number }): Promise<RemoteResult<RemoteEnvelope>>;
  status(): Promise<RemoteResult<RemoteStatus>>;
}
const fail = (code: RemoteError["code"]): RemoteResult<never> => ({ ok: false, error: { code } });
const opaque = (v: unknown): v is string => typeof v === "string" && v.isWellFormed() && v.trim().length > 0 && Buffer.byteLength(v) <= 4096;
const optionalID = (v: unknown) => v === undefined || opaque(v);
const number = (v: unknown, min: number, max: number) => typeof v === "number" && Number.isSafeInteger(v) && v >= min && v <= max;
const optionalNumber = (v: unknown, min: number, max: number) => v === undefined || number(v, min, max);

/** A remote credential has a distinct, derived native-store account for each
 * profile AND canonical origin. No editable file can rebind a credential, and
 * an existing C1 capture credential cannot silently authorize a new origin. */
export function profileAccount(profile: string, origin: string): string | null {
  const canonical = canonicalOrigin(origin);
  if (!validProfile(profile) || !canonical) return null;
  return "remote-" + createHash("sha256").update(JSON.stringify([profile, canonical])).digest("hex");
}
export class RemoteProfiles {
  constructor(private readonly credentials: CredentialStore) {}
  async register(profile: string, origin: string, token: string): Promise<RemoteResult<void>> {
    const account = profileAccount(profile, origin);
    if (!account || !validBearer(token)) return fail("invalid_input");
    const result = await this.credentials.set(account, token);
    return result.ok ? { ok: true, value: undefined } : fail("credential_unavailable");
  }
  async connect(profile: string, origin: string, factory: (origin: string, token: string) => MemoryGateway): Promise<RemoteResult<RemoteService>> {
    const account = profileAccount(profile, origin), normalized = canonicalOrigin(origin);
    if (!account || !normalized) return fail("invalid_input");
    const token = await this.credentials.get(account);
    if (!token.ok || !validBearer(token.value)) return fail("credential_unavailable");
    try { return { ok: true, value: new RemoteService(factory(normalized, token.value)) }; } catch { return fail("unavailable"); }
  }
}
function validRetrieval(i: RetrievalInput): boolean {
  if (!i || !i.scope || Object.keys(i).some((k) => !["scope", "query", "entity_ids", "applicability", "budget_bytes", "max_depth", "max_visited", "continuation", "mode"].includes(k)) || Object.keys(i.scope).some((k) => !["project_id", "task_id", "session_id"].includes(k)) || !opaque(i.scope.project_id) || !optionalID(i.scope.task_id) || !optionalID(i.scope.session_id) || (i.query !== undefined && (typeof i.query !== "string" || Buffer.byteLength(i.query) > 4096)) || (i.entity_ids !== undefined && (!Array.isArray(i.entity_ids) || i.entity_ids.length > 50 || !i.entity_ids.every(opaque))) || !optionalNumber(i.max_depth, 0, 2) || !optionalNumber(i.max_visited, 1, 50) || (i.mode !== undefined && !["orientation", "evidence", "working"].includes(i.mode)) || (i.mode === "working" && !opaque(i.scope.task_id))) return false;
  if (i.applicability !== undefined) {
    const a = i.applicability;
    if (!a || typeof a !== "object" || Array.isArray(a) || Object.keys(a).some((k) => !["repository_revision", "dirty_digest", "environment", "versions", "valid_from", "valid_until"].includes(k)) || [a.repository_revision, a.dirty_digest, a.environment, a.valid_from, a.valid_until].some((v) => v !== undefined && (typeof v !== "string" || Buffer.byteLength(v) > 4096)) || (a.versions !== undefined && (!a.versions || typeof a.versions !== "object" || Array.isArray(a.versions) || Object.entries(a.versions).length > 50 || Object.entries(a.versions).some(([k, v]) => !opaque(k) || typeof v !== "string" || Buffer.byteLength(v) > 4096)))) return false;
  }
  return true;
}
function validFetch(i: FetchInput): boolean {
  if (!i || !opaque(i.id) || !opaque(i.locator) || (i.mode !== undefined && !["publication", "working"].includes(i.mode)) || !optionalNumber(i.start_byte, 0, 8 * 1024 * 1024) || !optionalNumber(i.end_byte, 0, 8 * 1024 * 1024) || (i.end_byte !== undefined && i.end_byte < (i.start_byte ?? 0)) || (i.continuation !== undefined && (i.start_byte ?? 0) !== 0)) return false;
  return i.mode === "working" ? opaque(i.project_id) && opaque(i.task_id) && optionalID(i.session_id) : i.project_id === undefined && i.task_id === undefined && i.session_id === undefined;
}
/** Wrapper pins service budget AND caller frame budget; server authenticates the
 * inner cursor, including operation parameters, grants, epochs and generation.
 * Wrapper tampering never bypasses that server verification. */
function wrap(frameBudget: number, serviceBudget: number, inner: string): string { return `cli1.${frameBudget}.${serviceBudget}.${inner}`; }
function unwrap(cursor: string, budget: number): { service: number; inner: string } | null {
  const match = /^cli1\.([1-9]\d*)\.([1-9]\d*)\.([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)$/.exec(cursor);
  if (!match || Number(match[1]) !== budget) return null;
  const service = Number(match[2]);
  return number(service, 1, budget) ? { service, inner: match[3]! } : null;
}
export class RemoteService {
  constructor(private readonly gateway: MemoryGateway) {}
  private async bounded(budget: number, continuation: string | undefined, call: (budget: number, cursor?: string) => Promise<RemoteResult<RemoteEnvelope>>): Promise<RemoteResult<RemoteEnvelope>> {
    let serviceBudget = budget, cursor: string | undefined;
    if (continuation !== undefined) {
      if (typeof continuation !== "string" || Buffer.byteLength(continuation) > 4096) return fail("stale");
      const parsed = unwrap(continuation, budget);
      if (!parsed) return fail("stale");
      serviceBudget = parsed.service; cursor = parsed.inner;
    }
    for (let attempt = 0; attempt < 12; attempt++) {
      const result = await call(serviceBudget, cursor);
      if (!result.ok) return result;
      if (result.value.continuation !== undefined && (Buffer.byteLength(result.value.continuation) > 2048 || !/^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(result.value.continuation))) return fail("unavailable");
      const value = result.value.continuation ? { ...result.value, continuation: wrap(budget, serviceBudget, result.value.continuation) } : result.value;
      const bytes = Buffer.byteLength(renderRemoteFrame(value));
      if (!cursor && result.value.continuation) {
        // A later page can consume its entire server JSON budget. Reserve the
        // complete decimal-length prefix and worst-case wrapper before exposing
        // any budget-bound server cursor; never resize an existing cursor.
        const safe = budget - (String(budget).length + 1) - `cli1.${budget}.${budget}.`.length;
        if (serviceBudget > safe) {
          serviceBudget = safe;
          if (serviceBudget < 1) break;
          continue;
        }
      }
      if (bytes <= budget) return { ok: true, value };
      if (cursor) return fail("budget"); // never change a signed cursor's service budget
      serviceBudget = Math.min(serviceBudget - (bytes - budget), Math.floor(serviceBudget * budget / bytes)) - 1;
      if (serviceBudget < 1) break;
    }
    return fail("budget");
  }
  orient(input: RetrievalInput): Promise<RemoteResult<RemoteEnvelope>> {
    const budget = input?.budget_bytes ?? 4096;
    if (!number(budget, 1, 4096) || !validRetrieval(input)) return Promise.resolve(fail("invalid_input"));
    return this.bounded(budget, input.continuation, (b, c) => this.gateway.orient({ ...input, budget_bytes: b, continuation: c }));
  }
  search(input: RetrievalInput): Promise<RemoteResult<RemoteEnvelope>> {
    const budget = input?.budget_bytes ?? 16384;
    if (!number(budget, 1, 16384) || !validRetrieval(input) || (input.mode === "orientation" && budget > 4096)) return Promise.resolve(fail("invalid_input"));
    return this.bounded(budget, input.continuation, (b, c) => this.gateway.search({ ...input, budget_bytes: b, continuation: c }));
  }
  fetch(id: string, locator: string, budgetBytes: number, options: Omit<FetchInput, "id" | "locator" | "budget_bytes"> = {}): Promise<RemoteResult<RemoteEnvelope>> {
    const input: FetchInput = { ...options, id, locator, budget_bytes: budgetBytes };
    if (!number(budgetBytes, 1, 16384) || Object.keys(input).some((k) => !["id", "locator", "budget_bytes", "mode", "project_id", "task_id", "session_id", "start_byte", "end_byte", "continuation"].includes(k)) || !validFetch(input)) return Promise.resolve(fail("invalid_input"));
    return this.bounded(budgetBytes, input.continuation, (b, c) => this.gateway.fetch({ ...input, budget_bytes: b, continuation: c }));
  }
  async status(budget = 4096): Promise<RemoteResult<RemoteStatus>> {
    if (!number(budget, 1, 4096)) return fail("invalid_input");
    const result = await this.gateway.status();
    return result.ok && Buffer.byteLength(renderRemoteFrame(result.value)) > budget ? fail("budget") : result;
  }
}
