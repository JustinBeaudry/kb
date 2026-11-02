/** Schema-3 is intentionally separate from the local schema-2 envelope. */
export type RemoteScope = { project_id: string; task_id?: string; session_id?: string };
export type Applicability = { repository_revision?: string; dirty_digest?: string; environment?: string; versions?: Record<string, string>; valid_from?: string; valid_until?: string };
export type RemoteOrigin = { client_id: string; trust: "submitted" | "collector_attested" | "gateway_observed" | "owner_authenticated"; attestation_id?: string };
export type RemoteReference = { artifact_id: string; digest: string; locator: string; source_revision: string; origin: RemoteOrigin; applicability: Applicability };
export type RemoteChunk = { record_id: string; revision: string; text: string; claim_status: "candidate" | "verified" | "superseded" | "disputed" | "invalidated" | "rejected"; origin_trust: RemoteOrigin["trust"]; verification_outcome?: "supported" | "unsupported" | "ambiguous"; evidence: RemoteReference[]; warnings: string[]; applicability?: Applicability; passage?: { start_byte: number; end_byte: number; source_bytes: number } };
export type RemoteCheckpoint = { id: string; revision: string; scope: RemoteScope; objective: string; completed: string[]; pending: string[]; questions: string[]; evidence_revision_ids: string[]; updated_at: string; evidence_references_omitted?: boolean };
export type RemoteEnvelope = { schema_version: "3"; chunks: RemoteChunk[]; truncated: boolean; visited: number; no_results: boolean; continuation?: string; task_checkpoint?: { label: "task_state"; checkpoint: RemoteCheckpoint }; task_state_omitted?: boolean; truncation_reason?: "visited_limit" | "wire_budget" | "task_state_budget"; action?: string };
export type RemoteStatus = { schema_version: "3"; connection_state: "available" | "degraded" | "unavailable"; authorized_project_ids: string[]; authorized_capabilities: string[]; capability_gaps: string[]; max_revision_bytes?: number };

function obj(x: unknown): x is Record<string, unknown> { return x !== null && typeof x === "object" && !Array.isArray(x); }
function keys(x: Record<string, unknown>, allowed: string[]): boolean { return Object.keys(x).every((k) => allowed.includes(k)); }
function str(x: unknown): x is string { return typeof x === "string" && x.isWellFormed() && Buffer.byteLength(x) <= 16384; }
function id(x: unknown): x is string { return str(x) && x.trim().length > 0 && Buffer.byteLength(x) <= 4096; }
function wellFormed(x: string): boolean { return x.isWellFormed(); }
function opt(x: unknown, check: (x: unknown) => boolean): boolean { return x === undefined || check(x); }
function strings(x: unknown): x is string[] { return Array.isArray(x) && x.length <= 1000 && x.every(str); }
function ids(x: unknown): boolean { return Array.isArray(x) && x.length <= 1000 && x.every(id); }
function choice(x: unknown, a: readonly string[]): boolean { return typeof x === "string" && a.includes(x); }
const trusts = ["submitted", "collector_attested", "gateway_observed", "owner_authenticated"] as const;
const claims = ["candidate", "verified", "superseded", "disputed", "invalidated", "rejected"] as const;
function scope(x: unknown): boolean { return obj(x) && keys(x, ["project_id", "task_id", "session_id"]) && id(x.project_id) && opt(x.task_id, id) && opt(x.session_id, id); }
function applicability(x: unknown): boolean {
  if (!obj(x) || !keys(x, ["repository_revision", "dirty_digest", "environment", "versions", "valid_from", "valid_until"])) return false;
  return [x.repository_revision, x.dirty_digest, x.environment].every((v) => opt(v, str)) && [x.valid_from, x.valid_until].every((v) => opt(v, (s) => str(s) && !Number.isNaN(Date.parse(s)))) && opt(x.versions, (v) => obj(v) && Object.keys(v).length <= 1000 && Object.entries(v).every(([k, s]) => str(k) && str(s)));
}
function origin(x: unknown): boolean { return obj(x) && keys(x, ["client_id", "trust", "attestation_id"]) && id(x.client_id) && choice(x.trust, trusts) && opt(x.attestation_id, id); }
function reference(x: unknown): boolean { return obj(x) && keys(x, ["artifact_id", "digest", "locator", "source_revision", "origin", "applicability"]) && id(x.artifact_id) && id(x.digest) && str(x.locator) && id(x.source_revision) && origin(x.origin) && applicability(x.applicability); }
function int(x: unknown): x is number { return typeof x === "number" && Number.isSafeInteger(x) && x >= 0; }
function chunk(x: unknown): boolean {
  if (!obj(x) || !keys(x, ["record_id", "revision", "text", "claim_status", "origin_trust", "verification_outcome", "evidence", "warnings", "applicability", "passage"]) || !id(x.record_id) || !id(x.revision) || !str(x.text) || !choice(x.claim_status, claims) || !choice(x.origin_trust, trusts) || !opt(x.verification_outcome, (v) => choice(v, ["supported", "unsupported", "ambiguous"])) || !Array.isArray(x.evidence) || x.evidence.length > 1000 || !x.evidence.every(reference) || !strings(x.warnings) || !opt(x.applicability, applicability)) return false;
  // Candidate claims must never be presented as independently verified. Raw
  // source passages carry their own explicit non-claim warning instead.
  if (x.claim_status === "candidate" && (x.verification_outcome !== undefined || !(x.warnings.includes("unverified_task_candidate") || x.warnings.includes("source_evidence_is_not_a_verified_claim")))) return false;
  if (x.passage !== undefined) {
    const p = x.passage;
    if (!obj(p) || !keys(p, ["start_byte", "end_byte", "source_bytes"]) || !int(p.start_byte) || !int(p.end_byte) || !int(p.source_bytes) || p.start_byte > p.end_byte || p.end_byte > p.source_bytes || p.source_bytes > 8 * 1024 * 1024 || !wellFormed(x.text) || p.end_byte - p.start_byte !== Buffer.byteLength(x.text)) return false;
    if ((p.start_byte !== 0 || p.end_byte !== p.source_bytes) && !x.warnings.includes("partial_source_passage")) return false;
  }
  return true;
}
function checkpoint(x: unknown): boolean {
  if (!obj(x) || !keys(x, ["id", "revision", "scope", "objective", "completed", "pending", "questions", "evidence_revision_ids", "updated_at", "evidence_references_omitted"])) return false;
  return id(x.id) && id(x.revision) && scope(x.scope) && str(x.objective) && strings(x.completed) && strings(x.pending) && strings(x.questions) && ids(x.evidence_revision_ids) && str(x.updated_at) && !Number.isNaN(Date.parse(x.updated_at)) && opt(x.evidence_references_omitted, (v) => typeof v === "boolean");
}
export function parseRemoteEnvelope(x: unknown): RemoteEnvelope | null {
  if (!obj(x) || !keys(x, ["schema_version", "chunks", "truncated", "visited", "no_results", "continuation", "task_checkpoint", "task_state_omitted", "truncation_reason", "action"]) || x.schema_version !== "3" || !Array.isArray(x.chunks) || x.chunks.length > 50 || !x.chunks.every(chunk) || typeof x.truncated !== "boolean" || !int(x.visited) || x.visited > 50 || typeof x.no_results !== "boolean" || !opt(x.continuation, (s) => id(s) && Buffer.byteLength(s) <= 4096) || !opt(x.task_state_omitted, (v) => typeof v === "boolean") || !opt(x.truncation_reason, (v) => choice(v, ["visited_limit", "wire_budget", "task_state_budget"])) || !opt(x.action, str) || !opt(x.task_checkpoint, (v) => obj(v) && keys(v, ["label", "checkpoint"]) && v.label === "task_state" && checkpoint(v.checkpoint))) return null;
  if (!x.truncated && (x.continuation !== undefined || x.truncation_reason !== undefined || x.task_state_omitted === true)) return null;
  if (x.task_checkpoint !== undefined && x.task_state_omitted === true) return null;
  if (x.no_results !== (x.chunks.length === 0 && x.task_checkpoint === undefined && !x.task_state_omitted)) return null;
  // SAFETY: all required and optional properties and nested fields were validated above.
  return x as RemoteEnvelope;
}
export function parseRemoteStatus(x: unknown): RemoteStatus | null {
  if (!obj(x) || !keys(x, ["schema_version", "connection_state", "authorized_project_ids", "authorized_capabilities", "capability_gaps", "max_revision_bytes"]) || x.schema_version !== "3" || !choice(x.connection_state, ["available", "degraded", "unavailable"]) || !ids(x.authorized_project_ids) || !strings(x.authorized_capabilities) || !strings(x.capability_gaps) || !opt(x.max_revision_bytes, (n) => int(n) && n >= 1)) return null;
  // SAFETY: every status property was validated above.
  return x as RemoteStatus;
}
/** Exact frame: decimal UTF-8 body byte length, LF, then JSON with no trailing LF. */
export function renderRemoteFrame(value: RemoteEnvelope | RemoteStatus): string {
  const body = JSON.stringify(value);
  return `${Buffer.byteLength(body)}\n${body}`;
}
