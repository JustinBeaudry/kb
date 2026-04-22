import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { mkdirSync, rmSync, writeFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  computeManifestHash,
  writeManifest,
  writeSummaryFrontmatter,
  type SessionManifest,
} from "../src/lib/manifest";

const CLI = join(import.meta.dir, "..", "src", "cli.ts");

interface Env {
  root: string;
  vault: string;
}

function makeVault(): Env {
  const root = join(tmpdir(), `cairn-doctor-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  const vault = join(root, "vault");
  mkdirSync(join(vault, ".cairn", "sessions"), { recursive: true });
  mkdirSync(join(vault, "wiki"), { recursive: true });
  mkdirSync(join(vault, "sessions", "summaries"), { recursive: true });
  mkdirSync(join(vault, "sessions", ".trash"), { recursive: true });
  writeFileSync(join(vault, ".cairn", "state.json"), JSON.stringify({ createdAt: "2026-04-21T00:00:00Z" }));
  writeFileSync(join(vault, "log.md"), "# Vault Log\n");
  return { root, vault };
}

function writeGoodManifest(vault: string): void {
  const manifest: SessionManifest = {
    session_id: "123e4567-e89b-12d3-a456-426614174000",
    timestamp: "2026-04-21T00:00:00.000Z",
    transcript_path: null,
    transcript_hash: null,
    transcript_size: null,
    git_head: null,
    branch: null,
    files_changed: [],
    excerpt: { head: "", tail: "" },
  };
  manifest.manifest_hash = computeManifestHash(manifest);
  writeManifest(join(vault, "sessions", "2026-04-21T00-00-00-123e4567.md"), manifest);
  writeSummaryFrontmatter(
    join(vault, "sessions", "summaries", "2026-04-21T00-00-00-123e4567.md"),
    { manifest_hash: manifest.manifest_hash, transcript_hash: null, generated_at: "2026-04-21T00:00:01Z" },
    "## Summary\n\nCached.\n"
  );
}

async function runDoctor(env: Env): Promise<{ exitCode: number; stdout: string; stderr: string }> {
  const proc = Bun.spawn(["bun", CLI, "doctor"], {
    cwd: env.root,
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env, CAIRN_VAULT: env.vault },
  });
  const [stdout, stderr] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
  ]);
  const exitCode = await proc.exited;
  return { exitCode, stdout, stderr };
}

describe("cairn doctor session health", () => {
  let env: Env;

  beforeEach(() => {
    env = makeVault();
    writeGoodManifest(env.vault);
  });

  afterEach(() => {
    rmSync(env.root, { recursive: true, force: true });
  });

  it("reports manifest, summary, and trash counts", async () => {
    const result = await runDoctor(env);

    expect(result.exitCode).toBe(0);
    expect(result.stdout).toContain("session manifests — 1");
    expect(result.stdout).toContain("cached summaries — 1");
    expect(result.stdout).toContain("trashed sessions — 0");
  });

  it("warns on recent capture errors and ignores old ones", async () => {
    writeFileSync(
      join(env.vault, ".cairn", "capture-errors.log"),
      [
        JSON.stringify({ ts: new Date().toISOString(), error: "recent" }),
        JSON.stringify({ ts: "2020-01-01T00:00:00.000Z", error: "old" }),
      ].join("\n")
    );

    const result = await runDoctor(env);

    expect(result.stdout).toContain("1 capture errors in last 7 days");
  });

  it("warns when a migration journal is present", async () => {
    writeFileSync(join(env.vault, ".cairn", "migration-journal.json"), "{}");

    const result = await runDoctor(env);

    expect(result.stdout).toContain("migration in progress");
  });

  it("warns on legacy session files and malformed manifests", async () => {
    writeFileSync(join(env.vault, "sessions", "legacy.md"), "---\ntitle: old\n---\n\n## Summary\nOld.\n");
    writeFileSync(
      join(env.vault, "sessions", "broken-manifest.md"),
      "---\nmanifest_hash: abc\nsession_id: broken\n---\n"
    );

    const result = await runDoctor(env);

    expect(result.stdout).toContain("1 legacy session files detected");
    expect(result.stdout).toContain("manifest missing required fields");
  });

  it("deletes stale session lockfiles", async () => {
    const lockPath = join(env.vault, ".cairn", "sessions", "stale.lock");
    writeFileSync(lockPath, JSON.stringify({ pid: 123, createdAt: "2020-01-01T00:00:00.000Z" }));

    const result = await runDoctor(env);

    expect(result.stdout).toContain("removed stale session lockfiles — 1");
    expect(existsSync(lockPath)).toBe(false);
  });
});
