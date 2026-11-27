import { afterEach, describe, expect, test } from "bun:test";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { runCommand } from "citty";
import { parse as yamlParse } from "yaml";
import type { CaptureError, CredentialStore, Result } from "../src/capture/types";
import { createTransferCommand } from "../src/commands/transfer";
import { profileAccount } from "../src/services/remote.service";
import { FileSyncFolder, SYNC_STATE_FILE } from "../src/repositories/vault-sync.repository";
import { claimTitle, inertMarkdown, renderVaultPages } from "../src/services/obsidian.service";
import type { ExportPage, ExportedClaim, GatewayError, TransferGateway } from "../src/services/transfer.service";
import { VaultSyncService } from "../src/services/vault-sync.service";

const scope = { project_id: "demo" };
const dirs: string[] = [];
afterEach(() => { for (const d of dirs.splice(0)) rmSync(d, { recursive: true, force: true }); });
function temp(): string {
  const d = mkdtempSync(join(tmpdir(), "kb-vault-sync-"));
  dirs.push(d);
  return d;
}

function claim(id: string, text: string, status: ExportedClaim["claim_status"] = "verified", kind = "decision"): ExportedClaim {
  return { claim_id: id, revision: `${id}-r1`, text, kind, scope: { project_id: "demo", task_id: "t" }, claim_status: status, origin_trust: status === "verified" ? "owner_authenticated" : "submitted", evidence: [{ artifact_id: "art", digest: "d".repeat(64), locator: "notes.md#L1-3", source_revision: "s1", origin: { client_id: "agent", trust: "submitted" } }] };
}

class PagedExports implements TransferGateway {
  calls = 0;
  constructor(public pages: ExportPage[], private readonly failAt = -1) {}
  async policy(): Promise<never> { throw new Error("unused"); }
  async stageEvidence(): Promise<never> { throw new Error("unused"); }
  async submitCandidate(): Promise<never> { throw new Error("unused"); }
  async exportPage(input: { continuation?: string }): Promise<Result<ExportPage, GatewayError>> {
    const index = input.continuation ? Number(input.continuation.slice(1)) : 0;
    this.calls++;
    if (index === this.failAt) return { ok: false, error: { code: "unavailable" } };
    return { ok: true, value: this.pages[index]! };
  }
}
function pages(...groups: ExportedClaim[][]): ExportPage[] {
  return groups.map((claims, i) => ({ claims, omitted: 0, completeness: "complete", ...(i < groups.length - 1 ? { continuation: `p${i + 1}` } : {}) }));
}
const sync = (gateway: TransferGateway) => new VaultSyncService(gateway, (d) => new FileSyncFolder(d));
const files = (dir: string) => readdirSync(join(dir, "claims")).sort();

describe("rendering", () => {
  test("claim text cannot execute or load anything in Obsidian", () => {
    const hostile = "```dataviewjs\napp.vault.delete()\n```\n~~~dataviewjs\nx\n~~~\n`$= dv.el('b','x')`\n<iframe src=\"https://evil.example\"></iframe>\n![x](https://evil.example/p.png) ![[secret.png]]\n\\`still escaped`";
    const out = inertMarkdown(hostile);
    expect(out).not.toMatch(/(^|[^\\])(\\\\)*`/);
    expect(out).not.toMatch(/(^|[^\\])(\\\\)*~/);
    expect(out).not.toContain("<");
    expect(out).not.toMatch(/(^|[^\\])!\[/);
    expect(out).toContain("app.vault.delete()");
  });

  test("titles are readable and safe as file names and wikilinks", () => {
    expect(claimTitle("# Deploys need migration 007\nmore")).toBe("Deploys need migration 007");
    expect(claimTitle("a/b\\c:d*e?f\"g<h>i|j#k^l[m]n")).toBe("a b c d e f g h i j k l m n");
    expect(claimTitle("   \n\n")).toBe("Claim");
    expect([...claimTitle("x".repeat(200))].length).toBe(60);
    expect(claimTitle("ends with dots...")).toBe("ends with dots");
    expect(claimTitle(".obsidian/plugins")).toBe("obsidian plugins");
  });

  test("pages are deterministic, carry export provenance and mark unverified claims", () => {
    const claims = [claim("b", "Use Neo4j 2026.10", "candidate", "finding"), claim("a", "Backups run nightly")];
    const first = renderVaultPages(scope, claims, "complete");
    expect(renderVaultPages(scope, [...claims].reverse(), "complete")).toEqual(first);
    const index = first.find((p) => p.path === "index.md")!.content;
    expect(index.indexOf("## Verified")).toBeLessThan(index.indexOf("## Candidates (unverified)"));
    expect(index).toContain("[[claims/Backups run nightly (");
    const candidate = first.find((p) => p.path.startsWith("claims/Use Neo4j"))!.content;
    expect(candidate).toContain("> [!warning] Candidate");
    const front = yamlParse(/^---\n([\s\S]*?)\n---/.exec(candidate)![1]!);
    expect(front).toMatchObject({ kb_export: 1, claim_id: "b", claim_status: "candidate", origin_trust: "submitted", tags: ["kb/candidate", "kb/kind/finding"] });
    const verified = first.find((p) => p.path.startsWith("claims/Backups"))!.content;
    expect(verified).not.toContain("[!warning]");
    expect(verified).toContain("## Evidence");
  });

  test("an incomplete export says so on the index", () => {
    expect(renderVaultPages(scope, [], "partial")[0]!.content).toContain("[!warning] Incomplete");
  });
});

describe("vault-sync", () => {
  test("writes an index and one page per claim, then only changes what changed", async () => {
    const out = join(temp(), "kb");
    const gateway = new PagedExports(pages([claim("a", "Backups run nightly")], [claim("b", "Use Neo4j", "candidate")]));
    const first = await sync(gateway).sync(scope, out);
    expect(first.ok && first.value).toMatchObject({ claims: 2, written: 3, unchanged: 0, removed: 0, completeness: "complete" });
    expect(existsSync(join(out, "index.md"))).toBe(true);
    expect(files(out)).toHaveLength(2);

    const second = await sync(gateway).sync(scope, out);
    expect(second.ok && second.value).toMatchObject({ written: 0, unchanged: 3, removed: 0 });
  });

  test("removes pages for claims that are gone, but only after a complete export", async () => {
    const out = join(temp(), "kb");
    await sync(new PagedExports(pages([claim("a", "Keep me"), claim("b", "Delete me")]))).sync(scope, out);
    expect(files(out)).toHaveLength(2);

    const partial = pages([claim("a", "Keep me")]);
    partial[0]!.completeness = "partial";
    const kept = await sync(new PagedExports(partial)).sync(scope, out);
    expect(kept.ok && kept.value.removed).toBe(0);
    expect(files(out)).toHaveLength(2);

    const complete = await sync(new PagedExports(pages([claim("a", "Keep me")]))).sync(scope, out);
    expect(complete.ok && complete.value.removed).toBe(1);
    expect(files(out)).toEqual([expect.stringMatching(/^Keep me \(/)]);
  });

  test("a failed export page writes nothing", async () => {
    const out = join(temp(), "kb");
    const result = await sync(new PagedExports(pages([claim("a", "One")], [claim("b", "Two")]), 1)).sync(scope, out);
    expect(result.ok).toBe(false);
    expect(existsSync(out)).toBe(false);

    const retried = await sync(new PagedExports(pages([claim("a", "One")], [claim("b", "Two")]))).sync(scope, out);
    expect(retried.ok && retried.value.written).toBe(3);
  });

  test("never overwrites a user's note that has a page's name", async () => {
    const out = join(temp(), "kb");
    await sync(new PagedExports(pages([claim("a", "One")]))).sync(scope, out);
    const [page] = renderVaultPages(scope, [claim("b", "Two")], "complete").filter((p) => p.path.startsWith("claims/"));
    writeFileSync(join(out, page!.path), "mine");
    const refused = await sync(new PagedExports(pages([claim("a", "One"), claim("b", "Two")]))).sync(scope, out);
    expect(!refused.ok && refused.error.code).toBe("output_exists");
    expect(readFileSync(join(out, page!.path), "utf8")).toBe("mine");
  });

  test("never writes into a folder of the user's notes or one synced for another scope", async () => {
    const root = temp();
    writeFileSync(join(root, "my-note.md"), "mine");
    const refused = await sync(new PagedExports(pages([claim("a", "One")]))).sync(scope, root);
    expect(!refused.ok && refused.error.code).toBe("output_exists");
    expect(readdirSync(root)).toEqual(["my-note.md"]);

    const out = join(temp(), "kb");
    await sync(new PagedExports(pages([claim("a", "One")]))).sync(scope, out);
    const other = await sync(new PagedExports(pages([]))).sync({ project_id: "other" }, out);
    expect(!other.ok && other.error.code).toBe("invalid_input");
  });

  test("leaves files it did not write and refuses symlinks", async () => {
    const out = join(temp(), "kb");
    await sync(new PagedExports(pages([claim("a", "One")]))).sync(scope, out);
    writeFileSync(join(out, "claims", "My own note.md"), "mine");
    await sync(new PagedExports(pages([]))).sync(scope, out);
    expect(files(out)).toEqual(["My own note.md"]);

    const elsewhere = temp();
    const linked = join(temp(), "kb");
    await sync(new PagedExports(pages([]))).sync(scope, linked);
    rmSync(join(linked, "claims"), { recursive: true });
    symlinkSync(elsewhere, join(linked, "claims"));
    const result = await sync(new PagedExports(pages([claim("a", "One")]))).sync(scope, linked);
    expect(!result.ok && result.error.code).toBe("invalid_input");
    expect(readdirSync(elsewhere)).toEqual([]);
  });

  test("a tampered state file cannot make the sync delete outside its pages", async () => {
    const out = join(temp(), "kb");
    await sync(new PagedExports(pages([claim("a", "One")]))).sync(scope, out);
    mkdirSync(join(out, "notes"));
    writeFileSync(join(out, "notes", "keep.md"), "mine");
    const state = JSON.parse(readFileSync(join(out, SYNC_STATE_FILE), "utf8"));
    state.files.push("notes/keep.md");
    writeFileSync(join(out, SYNC_STATE_FILE), JSON.stringify(state));
    const result = await sync(new PagedExports(pages([]))).sync(scope, out);
    expect(!result.ok && result.error.code).toBe("invalid_input");
    expect(readFileSync(join(out, "notes", "keep.md"), "utf8")).toBe("mine");
  });

  test("the CLI writes the view and exits 2 when the export was partial", async () => {
    const endpoint = "http://127.0.0.1:9";
    const credentials: CredentialStore = {
      async get(account: string): Promise<Result<string, CaptureError>> {
        return account === profileAccount("agent", endpoint) ? { ok: true, value: "a".repeat(64) } : { ok: false, error: { code: "credential_unavailable" } };
      },
      async set(): Promise<Result<void, CaptureError>> { return { ok: true, value: undefined }; },
    };
    const partial = pages([claim("a", "One")]);
    partial[0]!.completeness = "partial";
    partial[0]!.omitted = 1;
    const command = createTransferCommand({ credentials, gateway: () => new PagedExports(partial) });
    const out = join(temp(), "kb");
    let stdout = "", stderr = "";
    const originalOut = process.stdout.write, originalErr = process.stderr.write, previousExit = process.exitCode;
    // SAFETY: test-only capture of CLI output streams.
    process.stdout.write = ((c: string) => { stdout += c; return true; }) as typeof process.stdout.write;
    // SAFETY: test-only capture of CLI output streams.
    process.stderr.write = ((c: string) => { stderr += c; return true; }) as typeof process.stderr.write;
    try {
      process.exitCode = 0;
      await runCommand(command, { rawArgs: ["vault-sync", "--project", "demo", "--profile", "agent", "--endpoint", endpoint] });
      expect(JSON.parse(stderr).error.code).toBe("invalid_input");
      stderr = "";
      process.exitCode = 0;
      await runCommand(command, { rawArgs: ["vault-sync", "--project", "demo", "--out", out, "--profile", "agent", "--endpoint", endpoint] });
      expect(JSON.parse(stdout)).toMatchObject({ claims: 1, omitted: 1, completeness: "partial" });
      expect(process.exitCode).toBe(2);
      expect(readFileSync(join(out, "index.md"), "utf8")).toContain("Incomplete");
    } finally {
      process.stdout.write = originalOut; process.stderr.write = originalErr; process.exitCode = previousExit ?? 0;
    }
  });
});
