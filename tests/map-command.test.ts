import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
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
  const dir = join(tmpdir(), `kb-mapcmd-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "wiki"), { recursive: true });
  mkdirSync(join(dir, ".kb"), { recursive: true });
  writeFileSync(
    join(dir, "wiki", "auth.md"),
    "---\ntitle: Auth\naliases: [Login]\n---\n# Auth\n\n## Overview\nOAuth2, see [[deploy]].\n\n## Setup\nsteps\n"
  );
  writeFileSync(join(dir, "wiki", "deploy.md"), "# Deploy\n\n## Steps\ncanary\n");
  vaults.push(dir);
  return dir;
}

async function run(
  vault: string,
  argv: string[],
  env: Record<string, string> = {}
): Promise<{ stdout: string; stderr: string; exitCode: number }> {
  const proc = Bun.spawn(["bun", "src/cli.ts", "map", ...argv], {
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env, KB_VAULT: vault, ...env },
  });
  const stdout = await new Response(proc.stdout).text();
  const stderr = await new Response(proc.stderr).text();
  const exitCode = await proc.exited;
  return { stdout, stderr, exitCode };
}

describe("map command", () => {
  it("no query: returns page and section node summaries with tree_root", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault, []);
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.policy.tree_root).toBe("wiki/");
    expect(env.policy.trust).toBe("curated");
    expect(env.chunks.length).toBeGreaterThan(2);
    for (const chunk of env.chunks) {
      expect(typeof chunk.node_id).toBe("string");
      expect(Array.isArray(chunk.heading_path)).toBe(true);
      expect(["page", "section"]).toContain(chunk.node_kind!);
    }
    const kinds = new Set(env.chunks.map((c) => c.node_kind));
    expect(kinds.has("page")).toBe(true);
    expect(kinds.has("section")).toBe(true);
  });

  it("query: surfaces the matching cluster first", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["Auth"]);
    const env = parseEnvelope(stdout);
    expect(env.chunks[0]!.node_id).toBe("wiki/auth.md");
    // neighborhood pulls in deploy via [[deploy]]
    expect(env.chunks.map((c) => c.node_id)).toContain("wiki/deploy.md");
  });

  it("exact alias query resolves to its page first", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["Login"]);
    const env = parseEnvelope(stdout);
    expect(env.chunks[0]!.node_id).toBe("wiki/auth.md");
  });

  it("zero-result query sets no_results with a recall fallback hint", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault, ["zzzNOMATCHzzz"]);
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.chunks).toEqual([]);
    expect(env.policy.no_results).toBe(true);
    expect(JSON.stringify(env.policy.suggestions)).toContain("kb recall");
  });

  it("empty wiki returns no_results", async () => {
    const vault = makeVault();
    rmSync(join(vault, "wiki", "auth.md"));
    rmSync(join(vault, "wiki", "deploy.md"));
    const { stdout, exitCode } = await run(vault, []);
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.policy.no_results).toBe(true);
  });

  it("degrades tier-by-tier under a tight budget and stays within it", async () => {
    const vault = makeVault();
    for (let i = 0; i < 20; i++) {
      writeFileSync(
        join(vault, "wiki", `bulk-${String(i).padStart(2, "0")}.md`),
        `# Bulk ${i}\n\n## One\ntext\n\n## Two\ntext\n`
      );
    }
    const { stdout, exitCode } = await run(vault, ["--budget", "1024"]);
    expect(exitCode).toBe(0);
    expect(new TextEncoder().encode(stdout).length).toBeLessThanOrEqual(1024);
    const env = parseEnvelope(stdout);
    expect(env.policy.map_tier).toBe(3);
    expect(JSON.stringify(env.policy.suggestions ?? [])).toContain("kb map");
  });

  it("section-only candidates under tight budget fall back to parent pages, never a silent empty envelope", async () => {
    const vault = makeVault();
    rmSync(join(vault, "wiki", "auth.md"));
    rmSync(join(vault, "wiki", "deploy.md"));
    for (let i = 0; i < 35; i++) {
      writeFileSync(
        join(vault, "wiki", `widget-${String(i).padStart(2, "0")}.md`),
        `# Page ${i}\n\n## Match zzz-heading ${i}\nbody text\n`
      );
    }
    const { stdout, exitCode } = await run(vault, ["zzz-heading", "--budget", "1024"]);
    expect(exitCode).toBe(0);
    expect(new TextEncoder().encode(stdout).length).toBeLessThanOrEqual(1024);
    const env = parseEnvelope(stdout);
    // The fix: parent pages stand in for section candidates under pressure.
    expect(env.chunks.length).toBeGreaterThan(0);
    for (const c of env.chunks) expect(c.node_kind).toBe("page");
    expect(env.policy.map_tier === 2 || env.policy.map_tier === 3).toBe(true);
  });

  it("when fitting drops every chunk the envelope still signals truncation and suggestions", async () => {
    const vault = makeVault();
    rmSync(join(vault, "wiki", "auth.md"));
    rmSync(join(vault, "wiki", "deploy.md"));
    for (let i = 0; i < 35; i++) {
      writeFileSync(
        join(vault, "wiki", `widget-${String(i).padStart(2, "0")}.md`),
        `# Page ${i}\n\n## Match zzz-heading ${i}\nbody text\n`
      );
    }
    const { stdout, exitCode } = await run(vault, ["zzz-heading", "--budget", "420"]);
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.policy.truncated).toBe(true);
    expect((env.policy.suggestions ?? []).length).toBeGreaterThan(0);
  });

  it("a 50-page/150-section vault fits tier 1-2 within the 16 KiB default", async () => {
    const vault = makeVault();
    rmSync(join(vault, "wiki", "auth.md"));
    rmSync(join(vault, "wiki", "deploy.md"));
    for (let i = 0; i < 50; i++) {
      writeFileSync(
        join(vault, "wiki", `page-${String(i).padStart(2, "0")}.md`),
        `# Page ${i}\n\n## Alpha\ntext\n\n## Beta\ntext\n\n## Gamma\ntext\n`
      );
    }
    const { stdout, exitCode } = await run(vault, []);
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.policy.map_tier === 1 || env.policy.map_tier === 2).toBe(true);
    expect(env.chunks.length).toBeGreaterThanOrEqual(50);
  });

  it("--budget flag overrides the KB_MAP_BUDGET env var", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["--budget", "100000"], { KB_MAP_BUDGET: "600" });
    const env = parseEnvelope(stdout);
    expect(env.policy.map_tier).toBe(1);
  });

  it("degenerate KB_MAP_BUDGET values fall back to the default", async () => {
    const vault = makeVault();
    for (const bad of ["0", "-5", "notanumber"]) {
      const { stdout, exitCode } = await run(vault, [], { KB_MAP_BUDGET: bad });
      expect(exitCode).toBe(0);
      const env = parseEnvelope(stdout);
      expect(env.policy.map_tier).toBe(1);
      expect(env.chunks.length).toBeGreaterThan(2);
    }
  });

  it("logs one hashed access-log line per invocation", async () => {
    const vault = makeVault();
    await run(vault, ["Auth"]);
    const log = readFileSync(join(vault, ".kb", "access-log.jsonl"), "utf-8").trim().split("\n");
    expect(log.length).toBe(1);
    const entry = JSON.parse(log[0]!);
    expect(entry.command).toBe("map");
    expect(entry.query_hash).toMatch(/^[0-9a-f]{32}$/);
    expect(JSON.stringify(entry)).not.toContain("Auth");
  });

  it("second call reuses the warm cache without rewriting tree.json", async () => {
    const vault = makeVault();
    await run(vault, []);
    const treePath = join(vault, ".kb", "index", "tree.json");
    expect(existsSync(treePath)).toBe(true);
    const before = statSync(treePath).mtimeMs;
    await run(vault, []);
    expect(statSync(treePath).mtimeMs).toBe(before);
  });

  it("missing vault exits non-zero with empty stdout", async () => {
    const { exitCode, stdout } = await run("/tmp/kb-map-missing-" + Date.now(), []);
    expect(exitCode).not.toBe(0);
    expect(stdout).toBe("");
  });
});
