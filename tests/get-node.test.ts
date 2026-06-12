import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { parseEnvelope } from "../src/lib/envelope";
import { MAX_FILE_BYTES } from "../src/lib/wiki-read";

const vaults: string[] = [];
afterEach(() => {
  for (const v of vaults.splice(0)) {
    try {
      rmSync(v, { recursive: true, force: true });
    } catch {}
  }
});

function makeVault(): string {
  const dir = join(tmpdir(), `kb-getnode-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "wiki"), { recursive: true });
  mkdirSync(join(dir, ".kb"), { recursive: true });
  writeFileSync(
    join(dir, "wiki", "guide.md"),
    [
      "# Guide",
      "intro line",
      "## Install",
      "install body, see [[bar]] and [[baz]] and [[missing-page]]",
      "## Configure",
      "configure body",
      "### Advanced",
      "#### Deep Option",
      "deep body",
      "## Use",
      "use body",
    ].join("\n")
  );
  writeFileSync(join(dir, "wiki", "bar.md"), "# Bar\nbar body line\n");
  writeFileSync(join(dir, "wiki", "baz.md"), "# Baz\nbaz body line links back [[guide]]\n");
  vaults.push(dir);
  return dir;
}

async function run(
  vault: string,
  argv: string[]
): Promise<{ stdout: string; stderr: string; exitCode: number }> {
  const proc = Bun.spawn(["bun", "src/cli.ts", "get-node", ...argv], {
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env, KB_VAULT: vault },
  });
  const stdout = await new Response(proc.stdout).text();
  const stderr = await new Response(proc.stderr).text();
  const exitCode = await proc.exited;
  return { stdout, stderr, exitCode };
}

describe("get-node command", () => {
  it("page ID returns the full file as one curated chunk", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault, ["wiki/bar.md"]);
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.chunks.length).toBe(1);
    expect(env.chunks[0]!.node_kind).toBe("page");
    expect(env.chunks[0]!.curation).toBe("curated");
    expect(env.chunks[0]!.text).toContain("bar body line");
    expect(env.policy.nav_trace).toEqual(["wiki/bar.md"]);
  });

  it("section ID returns only that section with its file-absolute line range", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["wiki/guide.md#install"]);
    const env = parseEnvelope(stdout);
    expect(env.chunks.length).toBe(1);
    const c = env.chunks[0]!;
    expect(c.curation).toBe("heading-section");
    expect(c.node_kind).toBe("section");
    expect(c.line_range).toEqual([3, 4]);
    expect(c.text).toContain("install body");
    expect(c.text).not.toContain("configure body");
  });

  it("deep nested section resolves to its own range, not the parent's", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["wiki/guide.md#deep-option"]);
    const env = parseEnvelope(stdout);
    expect(env.chunks[0]!.text).toContain("deep body");
    expect(env.chunks[0]!.text).not.toContain("configure body");
  });

  it("--neighbors adds previous and next siblings at the same level", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["wiki/guide.md#configure", "--neighbors"]);
    const env = parseEnvelope(stdout);
    const ids = env.chunks.map((c) => c.node_id);
    expect(ids).toContain("wiki/guide.md#configure");
    expect(ids).toContain("wiki/guide.md#install");
    expect(ids).toContain("wiki/guide.md#use");
    expect(env.chunks.length).toBe(3);
  });

  it("--neighbors on a sole section returns only the main chunk", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["wiki/bar.md#bar", "--neighbors"]);
    const env = parseEnvelope(stdout);
    expect(env.chunks.length).toBe(1);
  });

  it("--follow-wikilinks fetches resolved targets only, in nav_trace order", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["wiki/guide.md#install", "--follow-wikilinks", "5"]);
    const env = parseEnvelope(stdout);
    const ids = env.chunks.map((c) => c.node_id);
    expect(ids).toContain("wiki/bar.md");
    expect(ids).toContain("wiki/baz.md");
    // unresolved [[missing-page]] silently skipped
    expect(JSON.stringify(ids)).not.toContain("missing-page");
    expect(env.policy.nav_trace).toEqual(["wiki/guide.md#install", "wiki/bar.md", "wiki/baz.md"]);
  });

  it("wikilink cycles cannot expand: follows are one hop and capped", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["wiki/baz.md#baz", "--follow-wikilinks", "5"]);
    const env = parseEnvelope(stdout);
    // baz links guide; guide's own links are NOT followed transitively.
    expect(env.chunks.length).toBe(2);
    expect(env.policy.nav_trace).toEqual(["wiki/baz.md#baz", "wiki/guide.md"]);
  });

  it("unknown node ID exits 1 with stderr and empty stdout", async () => {
    const vault = makeVault();
    const { exitCode, stdout, stderr } = await run(vault, ["wiki/nope.md"]);
    expect(exitCode).toBe(1);
    expect(stdout).toBe("");
    expect(stderr).toContain("unknown node");
  });

  it("traversal-shaped IDs are rejected before any read", async () => {
    const vault = makeVault();
    for (const bad of ["wiki/../etc/passwd", "/etc/passwd", "raw/x.md", "wiki/guide.md#Bad Slug"]) {
      const { exitCode, stdout, stderr } = await run(vault, [bad]);
      expect(exitCode).toBe(1);
      expect(stdout).toBe("");
      expect(stderr).toContain("invalid node id");
    }
  });

  it("oversize page in the tree gets a distinct unreadable-content error", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "huge.md"), `# Huge\n${"x".repeat(MAX_FILE_BYTES + 1)}\n`);
    const { exitCode, stdout, stderr } = await run(vault, ["wiki/huge.md"]);
    expect(exitCode).toBe(1);
    expect(stdout).toBe("");
    expect(stderr).toContain("node content unavailable");
    expect(stderr).not.toContain("unknown node");
  });

  it("cached ID whose file was deleted errors after one rebuild attempt", async () => {
    const vault = makeVault();
    await run(vault, ["wiki/bar.md"]); // warm the cache
    rmSync(join(vault, "wiki", "bar.md"));
    const { exitCode, stderr } = await run(vault, ["wiki/bar.md"]);
    expect(exitCode).toBe(1);
    expect(stderr).toContain("unknown node");
  });

  it("logs one hashed access-log line", async () => {
    const vault = makeVault();
    await run(vault, ["wiki/bar.md"]);
    const log = readFileSync(join(vault, ".kb", "access-log.jsonl"), "utf-8").trim().split("\n");
    expect(log.length).toBe(1);
    const entry = JSON.parse(log[0]!);
    expect(entry.command).toBe("get-node");
    expect(entry.query_hash).toMatch(/^[0-9a-f]{32}$/);
    expect(JSON.stringify(entry)).not.toContain("bar.md");
  });
});
