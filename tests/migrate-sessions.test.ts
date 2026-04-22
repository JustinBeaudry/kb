import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import {
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  computeManifestHash,
  readManifest,
  readSummaryFrontmatter,
  writeManifest,
  type SessionManifest,
} from "../src/lib/manifest";

const CLI = join(import.meta.dir, "..", "src", "cli.ts");

interface Env {
  root: string;
  vault: string;
}

function makeVault(): Env {
  const root = join(tmpdir(), `cairn-migrate-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  const vault = join(root, "vault");
  mkdirSync(join(vault, "sessions"), { recursive: true });
  mkdirSync(join(vault, ".cairn"), { recursive: true });
  writeFileSync(
    join(vault, "log.md"),
    "# Vault Log\n\n## [2026-04-01] session | old branch | 1 files | deadbeef\n## [2026-04-01] session | other branch | 2 files | cafebabe\n"
  );
  return { root, vault };
}

function writeLegacyFixtures(vault: string): void {
  writeFileSync(join(vault, "sessions", "2026-04-01T00-00-00-deadbeef.md"), "Prompt is too long\n");
  writeFileSync(
    join(vault, "sessions", "2026-04-02T00-00-00.md"),
    `---
session_id: 123e4567-e89b-12d3-a456-426614174000
status: completed
extracted: false
files_changed:
  - path: src/app.ts
    action: modified
decisions:
  - choice: Keep manifests
    reason: Safer capture
open_threads:
  - Follow up
tags:
  - sessions
entire_checkpoint: abc123
---

## Summary

Legacy summary body.
`
  );
  writeFileSync(
    join(vault, "sessions", "2026-04-03T00-00-00-unknown.md"),
    `---
title: Unknown
---

This mentions "Prompt is too long" but is not exactly the known error.
`
  );

  const migrated: SessionManifest = {
    session_id: "abcdef01-2345-6789-abcd-ef0123456789",
    timestamp: "2026-04-04T00:00:00.000Z",
    transcript_path: null,
    transcript_hash: null,
    transcript_size: null,
    git_head: null,
    branch: null,
    files_changed: [],
    excerpt: { head: "", tail: "" },
    manifest_hash: null,
  };
  migrated.manifest_hash = computeManifestHash(migrated);
  writeManifest(join(vault, "sessions", "2026-04-04T00-00-00-abcdef01.md"), migrated);
}

async function runMigrate(env: Env, args: string[]): Promise<{ exitCode: number; stdout: string; stderr: string }> {
  const proc = Bun.spawn(["bun", CLI, "migrate-sessions", ...args], {
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

describe("cairn migrate-sessions", () => {
  let env: Env;

  beforeEach(() => {
    env = makeVault();
    writeLegacyFixtures(env.vault);
  });

  afterEach(() => {
    rmSync(env.root, { recursive: true, force: true });
  });

  it("dry-runs by default without mutating the vault", async () => {
    const before = readdirSync(join(env.vault, "sessions")).sort();
    const result = await runMigrate(env, []);
    const after = readdirSync(join(env.vault, "sessions")).sort();

    expect(result.exitCode).toBe(0);
    expect(result.stdout).toContain("legacy-error");
    expect(result.stdout).toContain("legacy-well-formed");
    expect(result.stdout).toContain("already-migrated");
    expect(result.stdout).toContain("unknown");
    expect(after).toEqual(before);
    expect(existsSync(join(env.vault, "sessions", ".trash"))).toBe(false);
  });

  it("applies migration by moving errors and converting well-formed summaries", async () => {
    const result = await runMigrate(env, ["--apply", "--yes"]);

    expect(result.exitCode).toBe(0);
    expect(existsSync(join(env.vault, ".cairn", "migration-journal.json"))).toBe(false);
    expect(existsSync(join(env.vault, "sessions", ".trash", "2026-04-01T00-00-00-deadbeef.md"))).toBe(true);
    expect(existsSync(join(env.vault, "sessions", "2026-04-02T00-00-00.md"))).toBe(false);

    const convertedPath = join(env.vault, "sessions", "2026-04-02T00-00-00-123e4567.md");
    const summaryPath = join(env.vault, "sessions", "summaries", "2026-04-02T00-00-00-123e4567.md");
    const manifest = readManifest(convertedPath);
    const summary = readSummaryFrontmatter(summaryPath);

    expect(manifest.transcript_path).toBeNull();
    expect(manifest.transcript_hash).toBeNull();
    expect(manifest.files_changed).toEqual([{ path: "src/app.ts", action: "modified" }]);
    expect(manifest.entire_checkpoint).toBe("abc123");
    expect(summary.data.user_edited).toBe(true);
    expect(summary.body).toContain("Legacy summary body.");
    const log = readFileSync(join(env.vault, "log.md"), "utf-8");
    expect(log).not.toContain("deadbeef");
    expect(log).toContain("cafebabe");
  });

  it("is idempotent after a successful migration", async () => {
    await runMigrate(env, ["--apply", "--yes"]);
    const second = await runMigrate(env, ["--apply", "--yes"]);

    expect(second.exitCode).toBe(0);
    expect(second.stdout).toContain("already-migrated");
  });
});
