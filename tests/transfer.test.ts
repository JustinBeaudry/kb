import { afterEach, describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { runCommand } from "citty";
import type { CaptureError, CredentialStore, Result } from "../src/capture/types";
import { createTransferCommand } from "../src/commands/transfer";
import { profileAccount } from "../src/services/remote.service";
import { HTTPTransferGateway } from "../src/gateways/memory.gateway";
import { FileBundleWriter, FileVaultRepository, type VaultEntry, type VaultError, type VaultRepository } from "../src/repositories/vault.repository";
import {
  TransferService,
  type CandidateSubmission,
  type EvidenceStage,
  type ExportPage,
  type ExportedClaim,
  type GatewayError,
  type ImportManifest,
  type TransferGateway,
  type TransferPolicy,
} from "../src/services/transfer.service";

const scope = { project_id: "demo", task_id: "import" };
const sha = (s: string | Uint8Array) => createHash("sha256").update(s).digest("hex");
const dirs: string[] = [];
afterEach(() => { for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true }); });
function temp(): string {
  const d = mkdtempSync(join(tmpdir(), "kb-transfer-"));
  dirs.push(d);
  return d;
}

/** Repository-boundary fake. Records every content read; never touches disk. */
class FakeVault implements VaultRepository {
  readonly reads: string[] = [];
  constructor(readonly root: string, readonly files: Map<string, string>, readonly unsafe: string[] = []) {}
  async inventory(): Promise<Result<VaultEntry[], VaultError>> {
    const entries: VaultEntry[] = [...this.files.keys()].map((id) => ({ id, kind: id.startsWith("raw/") ? "raw" : id.startsWith("sessions/") ? "session" : "curated", bytes: Buffer.byteLength(this.files.get(id)!) }));
    for (const id of this.unsafe) entries.push({ id, kind: "curated", bytes: 0, unsafe: true });
    return { ok: true, value: entries };
  }
  async read(id: string): Promise<Result<Uint8Array, VaultError>> {
    this.reads.push(id);
    const v = this.files.get(id);
    return v === undefined ? { ok: false, error: { code: "not_found" } } : { ok: true, value: Buffer.from(v) };
  }
}

/** Gateway-boundary fake emulating the server contract: deterministic
 * artifact identity, conflicts on an existing claim ID, source tombstones by
 * digest, and candidate-only submissions. */
class FakeServer implements TransferGateway {
  policyValue: TransferPolicy = { revision: "policy-1", paused: false, excluded_paths: [], excluded_fields: [] };
  readonly artifacts = new Map<string, EvidenceStage>();
  readonly claims = new Map<string, CandidateSubmission>();
  readonly tombstonedDigests = new Set<string>();
  readonly submissions: unknown[] = [];
  pages: ExportPage[] = [];
  exportCalls: (string | undefined)[] = [];
  failExportAt = -1;
  async policy(): Promise<Result<TransferPolicy, GatewayError>> { return { ok: true, value: this.policyValue }; }
  async stageEvidence(input: EvidenceStage): Promise<Result<{ artifact_id: string }, GatewayError>> {
    if (this.tombstonedDigests.has(input.digest)) return { ok: false, error: { code: "deleted" } };
    if (input.policy_revision !== this.policyValue.revision) return { ok: false, error: { code: "stale" } };
    const id = sha(JSON.stringify([input.scope, input.digest, input.source_locator, input.source_path]));
    this.artifacts.set(id, input);
    return { ok: true, value: { artifact_id: id } };
  }
  async submitCandidate(input: CandidateSubmission): Promise<Result<{ claim_id: string; revision: string; claim_status: string }, GatewayError>> {
    this.submissions.push(structuredClone(input));
    if (input.evidence.some((e) => this.tombstonedDigests.has(e.digest))) return { ok: false, error: { code: "forbidden" } };
    if (this.claims.has(input.claim_id)) return { ok: false, error: { code: "conflict" } };
    this.claims.set(input.claim_id, input);
    return { ok: true, value: { claim_id: input.claim_id, revision: "rev-" + this.claims.size, claim_status: "candidate" } };
  }
  async exportPage(input: { continuation?: string }): Promise<Result<ExportPage, GatewayError>> {
    this.exportCalls.push(input.continuation);
    const index = input.continuation === undefined ? 0 : Number(input.continuation.slice(5));
    if (index === this.failExportAt) { this.failExportAt = -1; return { ok: false, error: { code: "unavailable" } }; }
    return { ok: true, value: this.pages[index]! };
  }
}

function vaultFixture(): FakeVault {
  return new FakeVault("/synthetic/vault", new Map([
    ["wiki/alpha.md", "# Alpha\nThe synthetic build uses bun.\n"],
    ["wiki/beta.md", "# Beta\nDuplicate content.\n"],
    ["wiki/beta-copy.md", "# Beta\nDuplicate content.\n"],
    ["raw/secret-transcript.md", "RAW PRIVATE CONTENT"],
    ["sessions/2026-01-01.md", "SESSION PRIVATE CONTENT"],
  ]));
}
function service(vault: VaultRepository, server = new FakeServer()): { svc: TransferService; server: FakeServer } {
  return { svc: new TransferService(server, () => ({ ok: true, value: vault }), (dir) => new FileBundleWriter(dir)), server };
}
async function previewed(svc: TransferService, fileIds: string[], extra: { includeRaw?: boolean; includeSessions?: boolean } = {}): Promise<ImportManifest> {
  const result = await svc.preview({ vaultPath: "/synthetic/vault", scope, fileIds, ...extra });
  if (!result.ok) throw Error("preview failed: " + result.error.code);
  return result.value;
}

describe("selected import", () => {
  test("preview reads no unselected raw/session content", async () => {
    const vault = vaultFixture();
    const { svc } = service(vault);

    const manifest = await previewed(svc, ["wiki/alpha.md", "raw/secret-transcript.md", "sessions/2026-01-01.md"]);

    const unselectedSourceReads = vault.reads.filter((id) => id.startsWith("raw/") || id.startsWith("sessions/"));
    expect(unselectedSourceReads).toEqual([]);
    expect(vault.reads).toEqual(["wiki/alpha.md"]);
    expect(manifest.items.map((i) => i.file_id)).toEqual(["wiki/alpha.md"]);
    expect(manifest.omissions).toEqual([
      { file_id: "raw/secret-transcript.md", reason: "not_selected_kind" },
      { file_id: "sessions/2026-01-01.md", reason: "not_selected_kind" },
    ]);
    expect(JSON.stringify(manifest)).not.toContain("PRIVATE CONTENT");
  });

  test("explicit raw inclusion reads only the selected raw file", async () => {
    const vault = vaultFixture();
    const { svc } = service(vault);

    const manifest = await previewed(svc, ["raw/secret-transcript.md"], { includeRaw: true });

    expect(vault.reads).toEqual(["raw/secret-transcript.md"]);
    expect(manifest.items[0]?.kind).toBe("raw");
  });

  test("manifest carries source hashes, locators, scope and policy digest", async () => {
    const { svc } = service(vaultFixture());

    const manifest = await previewed(svc, ["wiki/alpha.md"]);

    const item = manifest.items[0]!;
    expect(item.digest).toBe(sha("# Alpha\nThe synthetic build uses bun.\n"));
    expect(JSON.parse(item.locator)).toMatchObject({ v: "kb-import-1", vault_file: "wiki/alpha.md", sha256: item.digest });
    expect(manifest.scope).toEqual(scope);
    expect(manifest.policy_revision).toBe("policy-1");
    expect(manifest.policy_digest).toMatch(/^[a-f0-9]{64}$/);
    expect(manifest.digest).toMatch(/^[a-f0-9]{64}$/);
  });

  test("duplicate content within a selection is imported once", async () => {
    const { svc } = service(vaultFixture());

    const manifest = await previewed(svc, ["wiki/beta.md", "wiki/beta-copy.md"]);

    expect(manifest.items.map((i) => i.file_id)).toEqual(["wiki/beta-copy.md"]);
    expect(manifest.omissions).toEqual([{ file_id: "wiki/beta.md", reason: "duplicate_content" }]);
  });

  test("policy exclusions are omitted before any read", async () => {
    const vault = vaultFixture();
    const server = new FakeServer();
    server.policyValue = { ...server.policyValue, excluded_paths: ["wiki/"] };
    const { svc } = service(vault, server);

    const manifest = await previewed(svc, ["wiki/alpha.md"]);

    expect(vault.reads).toEqual([]);
    expect(manifest.omissions).toEqual([{ file_id: "wiki/alpha.md", reason: "policy_excluded" }]);
  });

  test("paused policy refuses preview", async () => {
    const server = new FakeServer();
    server.policyValue = { ...server.policyValue, paused: true };
    const { svc } = service(vaultFixture(), server);

    const result = await svc.preview({ vaultPath: "/synthetic/vault", scope, fileIds: ["wiki/alpha.md"] });

    expect(result.ok ? undefined : result.error.code).toBe("policy_paused");
  });

  test("invalid selections are rejected", async () => {
    const { svc } = service(vaultFixture());
    for (const bad of [
      { vaultPath: "/synthetic/vault", scope: { project_id: "demo" }, fileIds: ["wiki/alpha.md"] },
      { vaultPath: "/synthetic/vault", scope, fileIds: [] },
      { vaultPath: "", scope, fileIds: ["wiki/alpha.md"] },
      { vaultPath: "/synthetic/vault", scope, fileIds: ["wiki/alpha.md", "wiki/alpha.md"] },
    ]) {
      // SAFETY: deliberately malformed input at the service boundary.
      const result = await svc.preview(bad as Parameters<TransferService["preview"]>[0]);
      expect(result.ok ? undefined : result.error.code).toBe("invalid_input");
    }
  });

  test("changed manifest requires new approval", async () => {
    const { svc, server } = service(vaultFixture());
    const manifest = await previewed(svc, ["wiki/alpha.md"]);
    const changed: ImportManifest = { ...manifest, items: manifest.items.map((i) => ({ ...i, file_id: "wiki/beta.md" })) };

    const changedManifestResult = await svc.apply(changed, manifest.digest);

    expect(changedManifestResult.ok).toBe(false);
    if (changedManifestResult.ok) return;
    expect(changedManifestResult.error.code).toBe("approval_stale");
    expect(server.artifacts.size).toBe(0);
  });

  test("changed source after approval requires new approval", async () => {
    const vault = vaultFixture();
    const { svc, server } = service(vault);
    const manifest = await previewed(svc, ["wiki/alpha.md"]);
    vault.files.set("wiki/alpha.md", "# Alpha\nEdited after approval.\n");

    const result = await svc.apply(manifest, manifest.digest);

    expect(result.ok ? undefined : result.error.code).toBe("approval_stale");
    expect(server.artifacts.size).toBe(0);
  });

  test("changed policy after approval requires new approval", async () => {
    const { svc, server } = service(vaultFixture());
    const manifest = await previewed(svc, ["wiki/alpha.md"]);
    server.policyValue = { ...server.policyValue, revision: "policy-2" };

    const result = await svc.apply(manifest, manifest.digest);

    expect(result.ok ? undefined : result.error.code).toBe("approval_stale");
  });

  test("wrong approval digest is stale", async () => {
    const { svc } = service(vaultFixture());
    const manifest = await previewed(svc, ["wiki/alpha.md"]);

    const result = await svc.apply(manifest, "0".repeat(64));

    expect(result.ok ? undefined : result.error.code).toBe("approval_stale");
  });

  test("curated import remains candidate", async () => {
    const { svc, server } = service(vaultFixture());
    const manifest = await previewed(svc, ["wiki/alpha.md", "wiki/beta.md"]);

    const result = await svc.apply(manifest, manifest.digest);

    expect(result.ok).toBe(true);
    if (!result.ok) return;
    const report = result.value;
    expect(report.imported).toHaveLength(2);
    expect(report.imported.every((item) => item.status === "candidate")).toBe(true);
    expect(report.complete).toBe(true);
    for (const s of server.submissions) {
      expect(Object.keys(s as object).sort()).toEqual(["applicability", "claim_id", "depends_on", "evidence", "kind", "scope", "text"]);
      expect(JSON.stringify(s)).not.toMatch(/verified|claim_status|origin/);
    }
    const submitted = [...server.claims.values()][0]!;
    expect(submitted.scope).toEqual(scope);
    expect(submitted.evidence[0]?.digest).toBe(manifest.items[0]?.digest);
  });

  test("retry creates no duplicate", async () => {
    const { svc, server } = service(vaultFixture());
    const manifest = await previewed(svc, ["wiki/alpha.md", "wiki/beta.md"]);
    const first = await svc.apply(manifest, manifest.digest);
    expect(first.ok).toBe(true);

    const retry = await svc.apply(manifest, manifest.digest);

    expect(retry.ok).toBe(true);
    if (!retry.ok) return;
    expect(retry.value.imported).toEqual([]);
    expect(retry.value.duplicates.sort()).toEqual(["wiki/alpha.md", "wiki/beta.md"]);
    expect(server.claims.size).toBe(2);
    expect(server.artifacts.size).toBe(2);
  });

  test("deleted source identity cannot resurrect", async () => {
    const vault = vaultFixture();
    const { svc, server } = service(vault);
    const manifest = await previewed(svc, ["wiki/alpha.md"]);
    server.tombstonedDigests.add(manifest.items[0]!.digest);
    // The same deleted bytes under a new file name are the same source identity.
    vault.files.set("wiki/renamed.md", vault.files.get("wiki/alpha.md")!);
    const renamed = await previewed(svc, ["wiki/renamed.md"]);

    const original = await svc.apply(manifest, manifest.digest);
    const moved = await svc.apply(renamed, renamed.digest);

    for (const result of [original, moved]) {
      expect(result.ok).toBe(true);
      if (!result.ok) return;
      expect(result.value.imported).toEqual([]);
      expect(result.value.refused.map((r) => r.reason)).toEqual(["deleted"]);
      expect(result.value.complete).toBe(false);
    }
    expect(server.claims.size).toBe(0);
  });

  test("partial failures are reported without hiding successes", async () => {
    const { svc, server } = service(vaultFixture());
    const manifest = await previewed(svc, ["wiki/alpha.md", "wiki/beta.md"]);
    const original = server.submitCandidate.bind(server);
    server.submitCandidate = async (input) => input.text.includes("Alpha") ? { ok: false, error: { code: "unavailable" } } : original(input);

    const result = await svc.apply(manifest, manifest.digest);

    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.value.imported.map((i) => i.file_id)).toEqual(["wiki/beta.md"]);
    expect(result.value.failed).toEqual([{ file_id: "wiki/alpha.md", reason: "unavailable" }]);
    expect(result.value.complete).toBe(false);
  });

  test("server status other than candidate is rejected as escalation", async () => {
    const { svc, server } = service(vaultFixture());
    const manifest = await previewed(svc, ["wiki/alpha.md"]);
    server.submitCandidate = async (input) => ({ ok: true, value: { claim_id: input.claim_id, revision: "r", claim_status: "verified" } });

    const result = await svc.apply(manifest, manifest.digest);

    expect(result.ok && result.value.imported).toEqual([]);
    expect(result.ok && result.value.failed).toEqual([{ file_id: "wiki/alpha.md", reason: "unexpected_status" }]);
  });
});

describe("file vault repository", () => {
  function syntheticVault(): { root: string; outside: string } {
    const root = temp(), outside = temp();
    mkdirSync(join(root, "wiki"));
    mkdirSync(join(root, "raw"));
    mkdirSync(join(root, ".kb"));
    writeFileSync(join(root, "wiki", "note.md"), "# Note\nsynthetic\n");
    writeFileSync(join(root, "raw", "r.md"), "raw\n");
    writeFileSync(join(root, ".kb", "state.json"), "{}");
    writeFileSync(join(outside, "secret.md"), "OUTSIDE SECRET");
    symlinkSync(join(outside, "secret.md"), join(root, "wiki", "escape.md"));
    symlinkSync(outside, join(root, "linked"));
    return { root, outside };
  }

  test("inventory lists metadata only and flags symlinks", async () => {
    const { root } = syntheticVault();
    const repo = FileVaultRepository.open(root);
    expect(repo.ok).toBe(true);
    if (!repo.ok) return;

    const inventory = await repo.value.inventory();

    expect(inventory.ok).toBe(true);
    if (!inventory.ok) return;
    const byId = Object.fromEntries(inventory.value.map((e) => [e.id, e]));
    expect(byId["wiki/note.md"]).toEqual({ id: "wiki/note.md", kind: "curated", bytes: 17 });
    expect(byId["raw/r.md"]?.kind).toBe("raw");
    expect(byId["wiki/escape.md"]?.unsafe).toBe(true);
    expect(byId["linked"]?.unsafe).toBe(true);
    expect(Object.keys(byId).some((id) => id.startsWith(".kb/") || id.startsWith("linked/"))).toBe(false);
  });

  test("symlink escape is denied", async () => {
    const { root } = syntheticVault();
    const repo = FileVaultRepository.open(root);
    if (!repo.ok) throw Error("open");

    const direct = await repo.value.read("wiki/escape.md");
    const viaDir = await repo.value.read("linked/secret.md");
    const traversal = await repo.value.read("../outside.md");

    for (const r of [direct, viaDir, traversal]) expect(r.ok ? "read" : r.error.code).toBe("path_unsafe");
  });

  test("symlink escape is denied through the service", async () => {
    const { root } = syntheticVault();
    const server = new FakeServer();
    const svc = new TransferService(server, (p) => FileVaultRepository.open(p), (d) => new FileBundleWriter(d));

    const result = await svc.preview({ vaultPath: root, scope, fileIds: ["wiki/escape.md", "linked/secret.md", "wiki/note.md"] });

    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.value.items.map((i) => i.file_id)).toEqual(["wiki/note.md"]);
    expect(result.value.omissions).toEqual([
      { file_id: "linked/secret.md", reason: "not_found" },
      { file_id: "wiki/escape.md", reason: "path_unsafe" },
    ]);
    expect(JSON.stringify(result.value)).not.toContain("OUTSIDE SECRET");
  });

  test("never rewrites vault originals", async () => {
    const { root } = syntheticVault();
    const before = readFileSync(join(root, "wiki", "note.md"), "utf8");
    const svc = new TransferService(new FakeServer(), (p) => FileVaultRepository.open(p), (d) => new FileBundleWriter(d));
    const manifest = await svc.preview({ vaultPath: root, scope, fileIds: ["wiki/note.md"] });
    if (!manifest.ok) throw Error("preview");

    const applied = await svc.apply(manifest.value, manifest.value.digest);

    expect(applied.ok).toBe(true);
    expect(readFileSync(join(root, "wiki", "note.md"), "utf8")).toBe(before);
  });

  test("missing vault is unavailable", async () => {
    expect(FileVaultRepository.open(join(temp(), "absent")).ok).toBe(false);
  });
});

describe("portable export", () => {
  const verified: ExportedClaim = {
    claim_id: "claim-verified", revision: "rev-v", text: "Builds use bun.", kind: "fact", scope: { project_id: "demo" },
    claim_status: "verified", origin_trust: "collector_attested",
    evidence: [{ artifact_id: "artifact-1", digest: "a".repeat(64), locator: "wiki/alpha.md#L1", source_revision: "gen-1", origin: { client_id: "collector", trust: "collector_attested" } }],
  };
  const candidate: ExportedClaim = { ...verified, claim_id: "claim-candidate", revision: "rev-c", text: "Maybe flaky.", kind: "finding", claim_status: "candidate", origin_trust: "submitted", evidence: [] };

  function exportServer(): FakeServer {
    const server = new FakeServer();
    server.pages = [
      { claims: [verified], omitted: 0, completeness: "complete", continuation: "page-1" },
      { claims: [candidate], omitted: 1, completeness: "partial" },
    ];
    return server;
  }

  test("export import preserves provenance without trust escalation", async () => {
    const out = join(temp(), "bundle");
    const server = exportServer();
    const svc = new TransferService(server, (p) => FileVaultRepository.open(p), (d) => new FileBundleWriter(d));

    const exported = await svc.export({ project_id: "demo" }, out);
    expect(exported.ok).toBe(true);
    if (!exported.ok) return;
    expect(exported.value).toMatchObject({ claims: 2, omitted: 1, completeness: "partial" });
    const files = readdirSync(join(out, "claims")).map((f) => "claims/" + f).sort();
    const manifest = await svc.preview({ vaultPath: out, scope, fileIds: files });
    if (!manifest.ok) throw Error("preview");
    const report = await svc.apply(manifest.value, manifest.value.digest);

    expect(report.ok).toBe(true);
    if (!report.ok) return;
    expect(report.value.imported).toHaveLength(2);
    expect(report.value.imported.every((item) => item.status === "candidate")).toBe(true);
    const reimported = [...server.claims.values()];
    const fact = reimported.find((c) => c.kind === "fact")!;
    expect(fact.text).toBe("Builds use bun.");
    const provenance = JSON.parse(fact.evidence[0]!.locator).provenance;
    expect(provenance).toMatchObject({ exported_claim_id: "claim-verified", exported_revision: "rev-v", exported_status: "verified", exported_origin_trust: "collector_attested" });
    expect(provenance.exported_evidence[0]).toMatchObject({ artifact_id: "artifact-1", digest: "a".repeat(64) });
    for (const s of server.submissions) expect(Object.keys(s as object)).not.toContain("claim_status");
  });

  test("export resumes from the last completed chunk", async () => {
    const out = join(temp(), "bundle");
    const server = exportServer();
    server.failExportAt = 1;
    const svc = new TransferService(server, (p) => FileVaultRepository.open(p), (d) => new FileBundleWriter(d));
    const first = await svc.export({ project_id: "demo" }, out);
    expect(first.ok ? "ok" : first.error.code).toBe("unavailable");

    const resumed = await svc.export({ project_id: "demo" }, out, { resume: true });

    expect(resumed.ok && resumed.value.claims).toBe(2);
    expect(server.exportCalls).toEqual([undefined, "page-1", "page-1"]);
    expect(readdirSync(join(out, "claims"))).toHaveLength(2);
  });

  test("export refuses an existing output without resume", async () => {
    const out = temp();
    writeFileSync(join(out, "keep.md"), "keep");
    const svc = new TransferService(exportServer(), (p) => FileVaultRepository.open(p), (d) => new FileBundleWriter(d));

    const result = await svc.export({ project_id: "demo" }, out);

    expect(result.ok ? "ok" : result.error.code).toBe("output_exists");
    expect(readFileSync(join(out, "keep.md"), "utf8")).toBe("keep");
  });

  test("export resume refuses a symlinked claims directory", async () => {
    const out = join(temp(), "bundle"), outside = temp();
    const server = exportServer();
    server.failExportAt = 1;
    const svc = new TransferService(server, (p) => FileVaultRepository.open(p), (d) => new FileBundleWriter(d));
    await svc.export({ project_id: "demo" }, out);
    rmSync(join(out, "claims"), { recursive: true, force: true });
    symlinkSync(outside, join(out, "claims"));

    const resumed = await svc.export({ project_id: "demo" }, out, { resume: true });

    expect(resumed.ok).toBe(false);
    expect(readdirSync(outside)).toEqual([]);
  });

  test("export rejects claims outside the requested session", async () => {
    const server = exportServer();
    server.pages = [{ claims: [{ ...verified, scope: { project_id: "demo", session_id: "other" } }], omitted: 0, completeness: "complete" }];
    const svc = new TransferService(server, (p) => FileVaultRepository.open(p), (d) => new FileBundleWriter(d));

    const other = await svc.export({ project_id: "demo", session_id: "s-1" }, join(temp(), "bundle"));
    server.pages = [{ claims: [verified], omitted: 0, completeness: "complete" }];
    const missing = await svc.export({ project_id: "demo", session_id: "s-1" }, join(temp(), "bundle"));

    expect([other, missing].map((r) => (r.ok ? "ok" : r.error.code))).toEqual(["unavailable", "unavailable"]);
  });

  test("export preserves withheld completeness", async () => {
    const cases: [ExportPage["completeness"][], string][] = [
      [["withheld"], "withheld"],
      [["complete", "withheld"], "withheld"],
      [["withheld", "partial"], "partial"],
      [["complete", "complete"], "complete"],
    ];
    const results: string[] = [];
    for (const [pages] of cases) {
      const server = new FakeServer();
      server.pages = pages.map((completeness, i) => ({ claims: [], omitted: 0, completeness, ...(i < pages.length - 1 ? { continuation: `page-${i + 1}` } : {}) }));
      const svc = new TransferService(server, (p) => FileVaultRepository.open(p), (d) => new FileBundleWriter(d));
      const r = await svc.export({ project_id: "demo" }, join(temp(), "bundle"));
      results.push(r.ok ? r.value.completeness : r.error.code);
    }

    expect(results).toEqual(cases.map(([, want]) => want));
  });

  test("export rejects an invalid exported status", async () => {
    const server = exportServer();
    // SAFETY: deliberately forged wire value at the gateway boundary.
    server.pages = [{ claims: [{ ...verified, claim_status: "trusted" as ExportedClaim["claim_status"] }], omitted: 0, completeness: "complete" }];
    const svc = new TransferService(server, (p) => FileVaultRepository.open(p), (d) => new FileBundleWriter(d));

    const result = await svc.export({ project_id: "demo" }, join(temp(), "bundle"));

    expect(result.ok ? "ok" : result.error.code).toBe("unavailable");
  });
});

describe("HTTP transfer gateway", () => {
  const agent = "a".repeat(64), collector = "c".repeat(64);
  function recorder(responses: Record<string, { status: number; body: unknown }>) {
    const calls: { url: string; auth: string; body: unknown }[] = [];
    const send = async (url: string | URL | Request, init?: RequestInit) => {
      const path = new URL(String(url)).pathname;
      const headers = new Headers(init?.headers);
      calls.push({ url: path, auth: headers.get("Authorization") ?? "", body: init?.body ? JSON.parse(String(init.body)) : undefined });
      const r = responses[path] ?? { status: 503, body: { code: "unavailable" } };
      return new Response(JSON.stringify(r.body), { status: r.status });
    };
    // SAFETY: test double implements the subset of fetch the gateway uses.
    return { calls, send: send as typeof fetch };
  }

  test("evidence uses the collector credential and candidates the agent credential", async () => {
    const { calls, send } = recorder({
      "/v1/artifacts": { status: 201, body: { schema_version: "3", artifact: { id: "art", digest: sha("x"), state: "staged" } } },
      "/v1/artifacts/art:complete": { status: 200, body: { schema_version: "3", receipt: { id: "art", state: "ready", duplicate: false } } },
      "/v1/candidates": { status: 201, body: { schema_version: "3", claim: { claim_id: "c", revision: "r", claim_status: "candidate" } } },
    });
    const gw = new HTTPTransferGateway("http://127.0.0.1:1", agent, collector, send);

    const staged = await gw.stageEvidence({ scope, digest: sha("x"), bytes: 1, policy_revision: "p", source_locator: "l", source_path: "wiki/x.md", content: Buffer.from("x") });
    const submitted = await gw.submitCandidate({ claim_id: "c", text: "x", kind: "imported_note", scope, evidence: [], depends_on: [], applicability: {} });

    expect(staged).toEqual({ ok: true, value: { artifact_id: "art" } });
    expect(submitted).toEqual({ ok: true, value: { claim_id: "c", revision: "r", claim_status: "candidate" } });
    expect(calls.map((c) => [c.url, c.auth])).toEqual([
      ["/v1/artifacts", "Bearer " + collector],
      ["/v1/artifacts/art:complete", "Bearer " + collector],
      ["/v1/candidates", "Bearer " + agent],
    ]);
    expect(calls[0]?.body).toMatchObject({ content_base64: Buffer.from("x").toString("base64"), source_path: "wiki/x.md" });
  });

  test("maps problems to closed codes", async () => {
    for (const [status, code, want] of [[410, "deleted", "deleted"], [409, "conflict", "conflict"], [409, "stale", "stale"], [403, "forbidden", "forbidden"], [401, "unauthorized", "unauthorized"], [503, "unavailable", "unavailable"]] as const) {
      const { send } = recorder({ "/v1/candidates": { status, body: { code } } });
      const gw = new HTTPTransferGateway("http://127.0.0.1:1", agent, collector, send);

      const result = await gw.submitCandidate({ claim_id: "c", text: "x", kind: "k", scope, evidence: [], depends_on: [], applicability: {} });

      expect(result.ok ? "ok" : result.error.code).toBe(want);
    }
  });

  test("policy and export pages are parsed from the wire", async () => {
    const { send } = recorder({
      "/v1/policies/demo": { status: 200, body: { schema_version: "3", policy: { revision: "p1", paused: false, excluded_paths: ["x/"], excluded_fields: [], allowed_model_routes: [], spool_limit_bytes: 1 } } },
      "/v1/exports": { status: 200, body: { schema_version: "3", scope: { project_id: "demo" }, claims: [{ claim_id: "c", revision: "r", text: "t", kind: "fact", scope: { project_id: "demo" }, evidence: [], depends_on: [], applicability: {}, claim_status: "verified", origin_trust: "submitted" }], omitted: 0, completeness: "complete" } },
    });
    const gw = new HTTPTransferGateway("http://127.0.0.1:1", agent, undefined, send);

    const policy = await gw.policy("demo");
    const page = await gw.exportPage({ scope: { project_id: "demo" } });

    expect(policy).toEqual({ ok: true, value: { revision: "p1", paused: false, excluded_paths: ["x/"], excluded_fields: [] } });
    expect(page.ok && page.value.claims[0]?.claim_status).toBe("verified");
    expect(await gw.stageEvidence({ scope, digest: "d", bytes: 0, policy_revision: "p", source_locator: "l", source_path: "s", content: new Uint8Array() })).toEqual({ ok: false, error: { code: "credential_unavailable" } });
  });
});

describe("transfer CLI", () => {
  class Credentials implements CredentialStore {
    entries = new Map<string, string>();
    async get(account: string): Promise<Result<string, CaptureError>> {
      const t = this.entries.get(account);
      return t ? { ok: true, value: t } : { ok: false, error: { code: "credential_unavailable" } };
    }
    async set(account: string, t: string): Promise<Result<void, CaptureError>> { this.entries.set(account, t); return { ok: true, value: undefined }; }
  }

  test("inventory, preview, approve and apply against a synthetic vault", async () => {
    const root = temp();
    mkdirSync(join(root, "wiki"));
    mkdirSync(join(root, "raw"));
    writeFileSync(join(root, "wiki", "a.md"), "# A\nsynthetic\n");
    writeFileSync(join(root, "raw", "r.md"), "RAW PRIVATE CONTENT");
    const endpoint = "http://127.0.0.1:9";
    const credentials = new Credentials();
    credentials.entries.set(profileAccount("agent", endpoint)!, "a".repeat(64));
    credentials.entries.set(profileAccount("collector", endpoint)!, "c".repeat(64));
    const server = new FakeServer();
    const tokens: (string | undefined)[][] = [];
    const command = createTransferCommand({ credentials, gateway: (_o, a, e) => { tokens.push([a, e]); return server; } });
    let out = "", err = "";
    const originalOut = process.stdout.write, originalErr = process.stderr.write, previousExit = process.exitCode;
    // SAFETY: test-only capture of CLI output streams.
    process.stdout.write = ((c: string) => { out += c; return true; }) as typeof process.stdout.write;
    // SAFETY: test-only capture of CLI output streams.
    process.stderr.write = ((c: string) => { err += c; return true; }) as typeof process.stderr.write;
    const run = async (args: string[]) => { out = ""; err = ""; process.exitCode = 0; await runCommand(command, { rawArgs: args }); };
    try {
      await run(["inventory", "--vault", root]);
      expect(JSON.parse(out).entries.map((e: VaultEntry) => e.id)).toEqual(["raw/r.md", "wiki/a.md"]);
      const manifestPath = join(temp(), "manifest.json");
      await run(["preview", "--vault", root, "--project", "demo", "--task", "import", "--select", "wiki/a.md,raw/r.md", "--manifest", manifestPath, "--profile", "agent", "--endpoint", endpoint]);
      const preview = JSON.parse(out);
      expect(preview.omissions).toEqual([{ file_id: "raw/r.md", reason: "not_selected_kind" }]);
      expect(readFileSync(manifestPath, "utf8")).not.toContain("RAW PRIVATE");
      await run(["apply", "--manifest", manifestPath, "--approve", "f".repeat(64), "--profile", "agent", "--evidence-profile", "collector", "--endpoint", endpoint]);
      expect(JSON.parse(err).error.code).toBe("approval_stale");
      await run(["apply", "--manifest", manifestPath, "--approve", preview.approval_digest, "--profile", "agent", "--evidence-profile", "collector", "--endpoint", endpoint]);
      const report = JSON.parse(out);
      expect(report.imported.map((i: { status: string }) => i.status)).toEqual(["candidate"]);
      expect(process.exitCode).toBe(2); // the raw omission makes the import partial
      await run(["apply", "--manifest", manifestPath, "--approve", preview.approval_digest, "--profile", "agent", "--endpoint", endpoint]);
      expect(JSON.parse(err).error.code).toBe("credential_unavailable");
      expect(tokens[1]).toEqual(["a".repeat(64), "c".repeat(64)]);
      await run(["export", "--project", "demo", "--out", join(temp(), "bundle"), "--profile", "agent", "--endpoint", endpoint]);
      expect(JSON.parse(err).error.code).toBe("unavailable"); // fake has no export pages
    } finally {
      process.stdout.write = originalOut; process.stderr.write = originalErr; process.exitCode = previousExit ?? 0;
    }
  });
});
