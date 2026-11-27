import type { Result } from "../capture/types";
import { validSyncPath, type SyncFolder } from "../repositories/vault-sync.repository";
import { renderVaultPages } from "./obsidian.service";
import { canonicalJSON, mergeCompleteness, opaque, validExportedClaim, validScope, type ExportedClaim, type ExportPage, type ExportScope, type TransferError, type TransferGateway } from "./transfer.service";

export type VaultSyncSummary = {
  out: string;
  claims: number;
  omitted: number;
  completeness: ExportPage["completeness"];
  written: number;
  unchanged: number;
  removed: number;
};

/** Bounds memory: every page is held until the whole export is read. */
export const MAX_SYNC_CLAIMS = 20_000;
const MAX_SYNC_PAGES = MAX_SYNC_CLAIMS / 10;

const fail = (code: TransferError["code"]): Result<never, TransferError> => ({ ok: false, error: { code } });

/**
 * Mirrors exported remote memory into a folder people read in Obsidian. Every
 * page of the export is authorized by the server as in `kb transfer export`.
 * Pages are written only after the whole export has been read, and pages for
 * claims that are gone (deleted, revoked or out of scope) are removed only
 * when the export was complete, so a partial read never hides knowledge.
 */
export class VaultSyncService {
  constructor(private readonly gateway: TransferGateway, private readonly folders: (dir: string) => SyncFolder) {}

  async sync(scope: ExportScope, outDir: string): Promise<Result<VaultSyncSummary, TransferError>> {
    if (!validScope(scope, false) || typeof outDir !== "string" || outDir.length === 0) return fail("invalid_input");
    const folder = this.folders(outDir);
    const opened = await folder.open();
    if (!opened.ok) return fail(opened.error.code === "output_exists" ? "output_exists" : "invalid_input");
    const prior = opened.value;
    if (prior && (canonicalJSON(prior.scope) !== canonicalJSON(scope) || !Array.isArray(prior.files) || !prior.files.every(validSyncPath))) return fail("invalid_input");

    const claims: ExportedClaim[] = [];
    let omitted = 0;
    let completeness: ExportPage["completeness"] = "complete";
    let continuation: string | undefined;
    for (let page = 0; ; page++) {
      if (page >= MAX_SYNC_PAGES) return fail("budget");
      const result = await this.gateway.exportPage({ scope, ...(continuation ? { continuation } : {}) });
      if (!result.ok) return result;
      const value = result.value;
      if (!value || typeof value !== "object" || !Array.isArray(value.claims) || !Number.isSafeInteger(value.omitted) || value.omitted < 0 || !["complete", "partial", "withheld"].includes(value.completeness) || (value.continuation !== undefined && !opaque(value.continuation)) || !value.claims.every((c) => validExportedClaim(c, scope))) return fail("unavailable");
      claims.push(...value.claims);
      if (claims.length > MAX_SYNC_CLAIMS) return fail("budget");
      omitted += value.omitted;
      completeness = mergeCompleteness(completeness, value.completeness);
      continuation = value.continuation;
      if (continuation === undefined) break;
    }
    // A claim ID appears once; a later page's revision wins.
    const latest = [...new Map(claims.map((c) => [c.claim_id, c])).values()];

    const pages = renderVaultPages(scope, latest, completeness);
    const files = new Set(prior?.files ?? []);
    for (const page of pages) files.add(page.path);
    // Claim ownership of every path before writing, so an interrupted sync
    // still knows which files are its own on the next run.
    const claimed = await folder.save({ version: "kb-vault-sync-1", scope, files: [...files].sort() });
    if (!claimed.ok) return fail("unavailable");
    let written = 0;
    let unchanged = 0;
    for (const page of pages) {
      const result = await folder.write(page.path, page.content);
      if (!result.ok) return fail(result.error.code === "path_unsafe" ? "invalid_input" : "unavailable");
      if (result.value === "written") written++;
      else unchanged++;
    }
    let removed = 0;
    if (completeness === "complete") {
      const current = new Set(pages.map((p) => p.path));
      for (const path of [...files]) {
        if (current.has(path)) continue;
        const result = await folder.remove(path);
        if (!result.ok) return fail(result.error.code === "path_unsafe" ? "invalid_input" : "unavailable");
        files.delete(path);
        removed++;
      }
    }
    const saved = await folder.save({ version: "kb-vault-sync-1", scope, files: [...files].sort() });
    if (!saved.ok) return fail("unavailable");
    return { ok: true, value: { out: outDir, claims: latest.length, omitted, completeness, written, unchanged, removed } };
  }
}
