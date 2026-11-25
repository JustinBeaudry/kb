import { createHash } from "node:crypto";
import { parse as yamlParse, stringify as yamlStringify } from "yaml";
import type { Result } from "../capture/types";
import type { BundleState, BundleWriter, VaultEntry, VaultEntryKind, VaultError, VaultRepository } from "../repositories/vault.repository";
import type { RemoteError } from "./remote.service";

/** Imported candidates are task-scoped: the server accepts candidates only
 * inside an explicit task, and they never appear as verified shared memory. */
export type TransferScope = { project_id: string; task_id: string; session_id?: string };
export type ExportScope = { project_id: string; task_id?: string; session_id?: string };

export type ImportSelection = {
  vaultPath: string;
  scope: TransferScope;
  fileIds: string[];
  /** Raw and session sources are never read unless explicitly included. */
  includeRaw?: boolean;
  includeSessions?: boolean;
};

export type OmissionReason = "not_found" | "path_unsafe" | "not_selected_kind" | "policy_excluded" | "too_large" | "too_large_for_candidate" | "unreadable" | "not_text" | "empty" | "duplicate_content";
export type Omission = { file_id: string; reason: OmissionReason };

export type ExportProvenance = {
  exported_claim_id: string;
  exported_revision: string;
  exported_status: string;
  exported_origin_trust: string;
  exported_evidence: { artifact_id: string; digest: string; locator: string; origin_trust: string }[];
};
export type ManifestItem = {
  file_id: string;
  kind: VaultEntryKind;
  digest: string;
  bytes: number;
  locator: string;
  claim_id: string;
  candidate_kind: string;
};
export type ImportManifest = {
  version: "kb-import-1";
  vault: string;
  scope: TransferScope;
  policy_revision: string;
  policy_digest: string;
  items: ManifestItem[];
  omissions: Omission[];
  /** Approval digest over every other field. Any change invalidates approval. */
  digest: string;
};
export type ImportedItem = { file_id: string; claim_id: string; revision: string; status: "candidate" };
export type ImportReport = {
  imported: ImportedItem[];
  duplicates: string[];
  refused: { file_id: string; reason: "deleted" | "forbidden" }[];
  failed: { file_id: string; reason: string }[];
  omissions: Omission[];
  complete: boolean;
};
export type ExportSummary = { out: string; claims: number; omitted: number; completeness: "complete" | "partial" | "withheld" };

export type TransferError = { code: "invalid_input" | "approval_stale" | "policy_paused" | "vault_unavailable" | "output_exists" | RemoteError["code"] };
export type GatewayError = { code: RemoteError["code"] };

export type TransferPolicy = { revision: string; paused: boolean; excluded_paths: string[]; excluded_fields: string[] };
export type EvidenceStage = { scope: TransferScope; digest: string; bytes: number; policy_revision: string; source_locator: string; source_path: string; content: Uint8Array };
export type CandidateSubmission = {
  claim_id: string;
  text: string;
  kind: string;
  scope: TransferScope;
  evidence: { artifact_id: string; digest: string; locator: string; source_revision: string; applicability: Record<string, never> }[];
  depends_on: string[];
  applicability: Record<string, never>;
};
export type ExportedClaim = {
  claim_id: string;
  revision: string;
  text: string;
  kind: string;
  scope: ExportScope;
  claim_status: "candidate" | "verified" | "superseded" | "disputed" | "invalidated" | "rejected";
  origin_trust: "submitted" | "collector_attested" | "gateway_observed" | "owner_authenticated";
  evidence: { artifact_id: string; digest: string; locator: string; source_revision: string; origin: { client_id: string; trust: string } }[];
};
export type ExportPage = { claims: ExportedClaim[]; omitted: number; completeness: "complete" | "partial" | "withheld"; continuation?: string };

/** F4/F5 boundary. Implementations never accept or forward status or trust. */
export interface TransferGateway {
  policy(projectId: string): Promise<Result<TransferPolicy, GatewayError>>;
  stageEvidence(input: EvidenceStage): Promise<Result<{ artifact_id: string }, GatewayError>>;
  submitCandidate(input: CandidateSubmission): Promise<Result<{ claim_id: string; revision: string; claim_status: string }, GatewayError>>;
  exportPage(input: { scope: ExportScope; continuation?: string }): Promise<Result<ExportPage, GatewayError>>;
}

const MAX_SELECTION = 1000;
const MAX_CANDIDATE_TEXT_BYTES = 16384;
const MAX_LOCATOR_BYTES = 4096;
const MAX_EXPORT_PAGES = 100_000;
const CLAIM_STATUSES = new Set(["candidate", "verified", "superseded", "disputed", "invalidated", "rejected"]);
const TRUSTS = new Set(["submitted", "collector_attested", "gateway_observed", "owner_authenticated"]);

const fail = (code: TransferError["code"]): Result<never, TransferError> => ({ ok: false, error: { code } });
const sha256 = (v: string | Uint8Array) => createHash("sha256").update(v).digest("hex");
const opaque = (v: unknown): v is string => typeof v === "string" && v.isWellFormed() && v.trim().length > 0 && Buffer.byteLength(v) <= 4096;

/** Deterministic JSON with sorted object keys: the approval digest input. */
export function canonicalJSON(value: unknown): string {
  if (Array.isArray(value)) return "[" + value.map(canonicalJSON).join(",") + "]";
  if (value && typeof value === "object") {
    return "{" + Object.entries(value).filter(([, v]) => v !== undefined).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)).map(([k, v]) => JSON.stringify(k) + ":" + canonicalJSON(v)).join(",") + "}";
  }
  return JSON.stringify(value);
}
export function manifestDigest(m: Omit<ImportManifest, "digest">): string {
  const { version, vault, scope, policy_revision, policy_digest, items, omissions } = m;
  return sha256(canonicalJSON({ version, vault, scope, policy_revision, policy_digest, items, omissions }));
}
function policyDigest(p: TransferPolicy): string {
  return sha256(canonicalJSON({ revision: p.revision, paused: p.paused, excluded_paths: [...p.excluded_paths].sort(), excluded_fields: [...p.excluded_fields].sort() }));
}
function excluded(policy: TransferPolicy, id: string): boolean {
  return policy.excluded_paths.some((e) => {
    const prefix = e.replace(/\/+$/, "");
    return prefix === "" || id === prefix || id.startsWith(prefix + "/");
  });
}
function validScope(s: unknown, task: boolean): boolean {
  if (!s || typeof s !== "object" || Array.isArray(s)) return false;
  const v: Record<string, unknown> = { ...s };
  if (Object.keys(v).some((k) => !["project_id", "task_id", "session_id"].includes(k))) return false;
  return opaque(v.project_id) && (task ? opaque(v.task_id) : v.task_id === undefined || opaque(v.task_id)) && (v.session_id === undefined || opaque(v.session_id));
}
function importClaimID(scope: TransferScope, fileID: string, digest: string): string {
  return "import-" + sha256(canonicalJSON(["kb-import-1", scope.project_id, scope.task_id, scope.session_id ?? "", fileID, digest])).slice(0, 48);
}
function bundleFileID(claimID: string): string {
  return "claims/" + sha256(claimID).slice(0, 32) + ".md";
}

/** Reads provenance written by a kb export. It is retained as metadata only;
 * the exported status never becomes the imported status. */
function exportProvenance(text: string): { body: string; kind: string; provenance: ExportProvenance } | undefined {
  const match = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?/.exec(text);
  if (!match) return undefined;
  let data: unknown;
  try { data = yamlParse(match[1]!); } catch { return undefined; }
  if (!data || typeof data !== "object" || Array.isArray(data)) return undefined;
  const d: Record<string, unknown> = { ...data };
  if (d.kb_export !== 1 || !opaque(d.claim_id) || !opaque(d.revision) || !opaque(d.kind) || typeof d.claim_status !== "string" || !CLAIM_STATUSES.has(d.claim_status) || typeof d.origin_trust !== "string" || !TRUSTS.has(d.origin_trust) || !Array.isArray(d.evidence)) return undefined;
  const evidence: ExportProvenance["exported_evidence"] = [];
  for (const e of d.evidence) {
    if (!e || typeof e !== "object") return undefined;
    const r: Record<string, unknown> = { ...e };
    if (!opaque(r.artifact_id) || !opaque(r.digest) || typeof r.locator !== "string" || typeof r.origin_trust !== "string" || !TRUSTS.has(r.origin_trust)) return undefined;
    evidence.push({ artifact_id: r.artifact_id, digest: r.digest, locator: r.locator, origin_trust: r.origin_trust });
  }
  return { body: text.slice(match[0].length), kind: d.kind, provenance: { exported_claim_id: d.claim_id, exported_revision: d.revision, exported_status: d.claim_status, exported_origin_trust: d.origin_trust, exported_evidence: evidence } };
}
/** Bounded locator; provenance evidence is truncated rather than dropped. */
function locatorFor(fileID: string, digest: string, bytes: number, provenance?: ExportProvenance): string | undefined {
  const base = { v: "kb-import-1", vault_file: fileID, sha256: digest, bytes };
  if (!provenance) return JSON.stringify(base);
  const p = { ...provenance, exported_evidence: [...provenance.exported_evidence] };
  for (;;) {
    const locator = JSON.stringify({ ...base, provenance: { ...p, exported_evidence_total: provenance.exported_evidence.length } });
    if (Buffer.byteLength(locator) <= MAX_LOCATOR_BYTES) return locator;
    if (p.exported_evidence.length === 0) return undefined;
    p.exported_evidence.pop();
  }
}
function validExportedClaim(c: ExportedClaim, scope: ExportScope): boolean {
  return !!c && opaque(c.claim_id) && opaque(c.revision) && typeof c.text === "string" && opaque(c.kind) && CLAIM_STATUSES.has(c.claim_status) && TRUSTS.has(c.origin_trust) && validScope(c.scope, false) && c.scope.project_id === scope.project_id && (scope.task_id === undefined || c.scope.task_id === scope.task_id) && (scope.session_id === undefined || c.scope.session_id === scope.session_id) && Array.isArray(c.evidence) && c.evidence.length <= 50 && c.evidence.every((e) => opaque(e.artifact_id) && opaque(e.digest) && typeof e.locator === "string" && TRUSTS.has(e.origin?.trust));
}

/** Partial dominates withheld, which dominates complete: a bundle is only as
 * complete as its least complete page. */
function mergeCompleteness(a: ExportPage["completeness"], b: ExportPage["completeness"]): ExportPage["completeness"] {
  if (a === "partial" || b === "partial") return "partial";
  return a === "withheld" || b === "withheld" ? "withheld" : "complete";
}

export class TransferService {
  constructor(
    private readonly gateway: TransferGateway,
    private readonly vaults: (path: string) => Result<VaultRepository, VaultError>,
    private readonly bundles: (dir: string) => BundleWriter,
  ) {}

  private async currentPolicy(project: string): Promise<Result<TransferPolicy, TransferError>> {
    const policy = await this.gateway.policy(project);
    if (!policy.ok) return policy;
    if (policy.value.paused) return fail("policy_paused");
    return policy;
  }

  /** Dry-run inventory of the explicit selection. Reads only selected,
   * eligible files; raw/session content requires explicit inclusion. */
  async preview(selection: ImportSelection): Promise<Result<ImportManifest, TransferError>> {
    const s = selection;
    if (!s || typeof s.vaultPath !== "string" || s.vaultPath.length === 0 || !validScope(s.scope, true) || !Array.isArray(s.fileIds) || s.fileIds.length === 0 || s.fileIds.length > MAX_SELECTION || !s.fileIds.every((id) => typeof id === "string") || new Set(s.fileIds).size !== s.fileIds.length) return fail("invalid_input");
    const vault = this.vaults(s.vaultPath);
    if (!vault.ok) return fail("vault_unavailable");
    const policy = await this.currentPolicy(s.scope.project_id);
    if (!policy.ok) return policy;
    const inventory = await vault.value.inventory();
    if (!inventory.ok) return fail("vault_unavailable");
    const entries = new Map<string, VaultEntry>(inventory.value.map((e) => [e.id, e]));
    const items: ManifestItem[] = [], omissions: Omission[] = [];
    const seen = new Set<string>();
    const scope: TransferScope = { project_id: s.scope.project_id, task_id: s.scope.task_id, ...(s.scope.session_id ? { session_id: s.scope.session_id } : {}) };
    for (const id of [...s.fileIds].sort()) {
      const entry = entries.get(id);
      const omit = (reason: OmissionReason) => omissions.push({ file_id: id, reason });
      if (!entry) { omit("not_found"); continue; }
      if (entry.unsafe) { omit("path_unsafe"); continue; }
      if ((entry.kind === "raw" && !s.includeRaw) || (entry.kind === "session" && !s.includeSessions)) { omit("not_selected_kind"); continue; }
      if (excluded(policy.value, id)) { omit("policy_excluded"); continue; }
      const read = await vault.value.read(id);
      if (!read.ok) { omit(read.error.code === "path_unsafe" || read.error.code === "not_found" || read.error.code === "too_large" ? read.error.code : "unreadable"); continue; }
      let text: string;
      try { text = new TextDecoder("utf-8", { fatal: true }).decode(read.value); } catch { omit("not_text"); continue; }
      const digest = sha256(read.value);
      if (seen.has(digest)) { omit("duplicate_content"); continue; }
      const exported = entry.kind === "curated" ? exportProvenance(text) : undefined;
      const body = exported ? exported.body : text;
      if (body.trim().length === 0) { omit("empty"); continue; }
      if (Buffer.byteLength(body) > MAX_CANDIDATE_TEXT_BYTES) { omit("too_large_for_candidate"); continue; }
      const locator = locatorFor(id, digest, read.value.byteLength, exported?.provenance);
      if (!locator) { omit("too_large_for_candidate"); continue; }
      seen.add(digest);
      items.push({ file_id: id, kind: entry.kind, digest, bytes: read.value.byteLength, locator, claim_id: importClaimID(scope, id, digest), candidate_kind: exported ? exported.kind : "imported_note" });
    }
    const manifest = { version: "kb-import-1" as const, vault: vault.value.root, scope, policy_revision: policy.value.revision, policy_digest: policyDigest(policy.value), items, omissions };
    return { ok: true, value: { ...manifest, digest: manifestDigest(manifest) } };
  }

  /** Applies exactly the approved manifest. Any change to the manifest,
   * selected sources, or policy since approval is approval_stale and nothing
   * is uploaded. Submissions are always candidates; retries are idempotent by
   * content-derived claim identity. */
  async apply(manifest: ImportManifest, approvalDigest: string): Promise<Result<ImportReport, TransferError>> {
    if (!manifest || typeof manifest !== "object" || manifest.version !== "kb-import-1" || !Array.isArray(manifest.items) || !Array.isArray(manifest.omissions) || !validScope(manifest.scope, true) || typeof manifest.vault !== "string" || typeof approvalDigest !== "string") return fail("invalid_input");
    if (manifestDigest(manifest) !== approvalDigest || manifest.digest !== approvalDigest) return fail("approval_stale");
    const vault = this.vaults(manifest.vault);
    if (!vault.ok) return fail("vault_unavailable");
    const policy = await this.currentPolicy(manifest.scope.project_id);
    if (!policy.ok) return policy;
    if (policy.value.revision !== manifest.policy_revision || policyDigest(policy.value) !== manifest.policy_digest) return fail("approval_stale");
    const contents: Uint8Array[] = [];
    for (const item of manifest.items) {
      const read = await vault.value.read(item.file_id);
      if (!read.ok || sha256(read.value) !== item.digest || read.value.byteLength !== item.bytes) return fail("approval_stale");
      contents.push(read.value);
    }
    const report: ImportReport = { imported: [], duplicates: [], refused: [], failed: [], omissions: manifest.omissions, complete: false };
    for (const [i, item] of manifest.items.entries()) {
      const content = contents[i]!;
      const staged = await this.gateway.stageEvidence({ scope: manifest.scope, digest: item.digest, bytes: item.bytes, policy_revision: manifest.policy_revision, source_locator: item.locator, source_path: item.file_id, content });
      if (!staged.ok) {
        if (staged.error.code === "deleted" || staged.error.code === "forbidden") report.refused.push({ file_id: item.file_id, reason: staged.error.code });
        else report.failed.push({ file_id: item.file_id, reason: staged.error.code });
        continue;
      }
      const text = new TextDecoder().decode(content);
      const body = item.kind === "curated" ? exportProvenance(text)?.body ?? text : text;
      const submitted = await this.gateway.submitCandidate({
        claim_id: item.claim_id, text: body, kind: item.candidate_kind, scope: manifest.scope,
        evidence: [{ artifact_id: staged.value.artifact_id, digest: item.digest, locator: item.locator, source_revision: item.digest, applicability: {} }],
        depends_on: [], applicability: {},
      });
      if (!submitted.ok) {
        if (submitted.error.code === "conflict") report.duplicates.push(item.file_id);
        else if (submitted.error.code === "deleted" || submitted.error.code === "forbidden") report.refused.push({ file_id: item.file_id, reason: submitted.error.code });
        else report.failed.push({ file_id: item.file_id, reason: submitted.error.code });
        continue;
      }
      if (submitted.value.claim_status !== "candidate" || submitted.value.claim_id !== item.claim_id) {
        report.failed.push({ file_id: item.file_id, reason: "unexpected_status" });
        continue;
      }
      report.imported.push({ file_id: item.file_id, claim_id: item.claim_id, revision: submitted.value.revision, status: "candidate" });
    }
    report.complete = report.refused.length === 0 && report.failed.length === 0 && report.omissions.length === 0;
    return { ok: true, value: report };
  }

  /** Writes a portable, vault-compatible bundle. Each chunk is authorized by
   * the server; progress is saved after each chunk so a failed export resumes
   * from its last authenticated continuation. */
  async export(scope: ExportScope, outDir: string, options: { resume?: boolean } = {}): Promise<Result<ExportSummary, TransferError>> {
    if (!validScope(scope, false) || typeof outDir !== "string" || outDir.length === 0) return fail("invalid_input");
    const writer = this.bundles(outDir);
    const begun = await writer.begin(!!options.resume);
    if (!begun.ok) return fail(begun.error.code === "output_exists" ? "output_exists" : "invalid_input");
    let state: BundleState = { version: "kb-export-1", scope, claims: 0, omitted: 0, completeness: "complete", done: false };
    if (begun.value) {
      const prior = begun.value;
      if (canonicalJSON(prior.scope) !== canonicalJSON(scope) || !Number.isSafeInteger(prior.claims) || !Number.isSafeInteger(prior.omitted) || (prior.continuation !== undefined && !opaque(prior.continuation))) return fail("invalid_input");
      state = prior;
    } else {
      const saved = await writer.save(state);
      if (!saved.ok) return fail("unavailable");
    }
    for (let page = 0; !state.done && page < MAX_EXPORT_PAGES; page++) {
      const result = await this.gateway.exportPage({ scope, ...(state.continuation ? { continuation: state.continuation } : {}) });
      if (!result.ok) return result;
      const value = result.value;
      if (!value || typeof value !== "object" || !Array.isArray(value.claims) || !Number.isSafeInteger(value.omitted) || value.omitted < 0 || !["complete", "partial", "withheld"].includes(value.completeness) || (value.continuation !== undefined && !opaque(value.continuation)) || !value.claims.every((c) => validExportedClaim(c, scope))) return fail("unavailable");
      for (const c of value.claims) {
        const front = { kb_export: 1, claim_id: c.claim_id, revision: c.revision, kind: c.kind, claim_status: c.claim_status, origin_trust: c.origin_trust, scope: c.scope, evidence: c.evidence.map((e) => ({ artifact_id: e.artifact_id, digest: e.digest, locator: e.locator, source_revision: e.source_revision, origin_trust: e.origin.trust })) };
        const written = await writer.write(bundleFileID(c.claim_id), `---\n${yamlStringify(front).trimEnd()}\n---\n${c.text}`);
        if (!written.ok) return fail("unavailable");
      }
      state = { ...state, claims: state.claims + value.claims.length, omitted: state.omitted + value.omitted, completeness: mergeCompleteness(state.completeness, value.completeness), continuation: value.continuation, done: value.continuation === undefined };
      const saved = await writer.save(state);
      if (!saved.ok) return fail("unavailable");
    }
    if (!state.done) return fail("incomplete");
    return { ok: true, value: { out: outDir, claims: state.claims, omitted: state.omitted, completeness: state.completeness } };
  }
}
