import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, writeFileSync, rmSync } from "node:fs";
import { parseEnvelope } from "../src/lib/envelope";

const vaults: string[] = [];
afterEach(() => {
  for (const v of vaults.splice(0)) {
    try {
      rmSync(v, { recursive: true, force: true });
    } catch {}
  }
});

function makeVault(): string {
  const dir = join(tmpdir(), `cairn-recall-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "wiki"), { recursive: true });
  mkdirSync(join(dir, "sessions"), { recursive: true });
  mkdirSync(join(dir, "raw"), { recursive: true });
  mkdirSync(join(dir, ".cairn"), { recursive: true });
  writeFileSync(
    join(dir, "wiki", "auth.md"),
    "# Auth Flow\n\nOAuth2 PKCE flow documented here.\n"
  );
  writeFileSync(
    join(dir, "wiki", "deploy.md"),
    "# Deploy Runbook\n\nStaging first, then canary, then full.\n"
  );
  writeFileSync(
    join(dir, "sessions", "2026-04-01.md"),
    "OAuth2 debug notes — SESSION_MARKER_X.\n"
  );
  writeFileSync(
    join(dir, "raw", "notes.md"),
    "OAuth2 raw dump — RAW_MARKER_Y.\n"
  );
  vaults.push(dir);
  return dir;
}

async function run(vault: string, query: string): Promise<{ stdout: string; stderr: string; exitCode: number }> {
  const proc = Bun.spawn(["bun", "src/cli.ts", "recall", query], {
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env, CAIRN_VAULT: vault },
  });
  const stdout = await new Response(proc.stdout).text();
  const stderr = await new Response(proc.stderr).text();
  const exitCode = await proc.exited;
  return { stdout, stderr, exitCode };
}

describe("recall command", () => {
  it("returns chunks matching the query, each with provenance", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault, "OAuth2");
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.chunks.length).toBeGreaterThan(0);
    for (const chunk of env.chunks) {
      expect(chunk.source).toMatch(/^wiki\//);
      expect(chunk.curation).toBe("curated");
      expect(Array.isArray(chunk.line_range)).toBe(true);
    }
  });

  it("does not return results from sessions/ or raw/", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, "OAuth2");
    const env = parseEnvelope(stdout);
    for (const chunk of env.chunks) {
      expect(chunk.source.startsWith("sessions/")).toBe(false);
      expect(chunk.source.startsWith("raw/")).toBe(false);
      expect(chunk.text).not.toContain("SESSION_MARKER_X");
      expect(chunk.text).not.toContain("RAW_MARKER_Y");
    }
  });

  it("empty result returns envelope with no_results policy flag and a hint", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault, "zzzUNMATCHEDqueryTokenZZZ");
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.chunks).toEqual([]);
    expect(env.policy.no_results).toBe(true);
    expect(Array.isArray(env.policy.suggestions)).toBe(true);
  });

  it("missing vault exits non-zero without leaking content", async () => {
    const { exitCode, stdout, stderr } = await run("/tmp/does-not-exist-cairn-" + Date.now(), "anything");
    expect(exitCode).not.toBe(0);
    expect(stdout).toBe("");
    expect(stderr).toMatch(/vault/i);
  });

  it("missing wiki directory returns structured error", async () => {
    const vault = makeVault();
    rmSync(join(vault, "wiki"), { recursive: true });
    const { exitCode, stderr } = await run(vault, "anything");
    expect(exitCode).not.toBe(0);
    expect(stderr).toMatch(/wiki/i);
  });
});
