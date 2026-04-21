import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, writeFileSync, rmSync, symlinkSync } from "node:fs";
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
  const dir = join(tmpdir(), `cairn-read-raw-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "raw"), { recursive: true });
  mkdirSync(join(dir, "wiki"), { recursive: true });
  mkdirSync(join(dir, ".cairn"), { recursive: true });
  const body = Array.from({ length: 500 }, (_, i) => `raw-line-${i + 1}`).join("\n");
  writeFileSync(join(dir, "raw", "notes.md"), body);
  writeFileSync(join(dir, "wiki", "auth.md"), "wiki content\n");
  vaults.push(dir);
  return dir;
}

async function run(vault: string, args: string[], env: Record<string, string> = {}): Promise<{ stdout: string; stderr: string; exitCode: number }> {
  const proc = Bun.spawn(["bun", "src/cli.ts", "read-raw", ...args], {
    stdout: "pipe",
    stderr: "pipe",
    stdin: "ignore",
    env: { ...process.env, CAIRN_VAULT: vault, ...env },
  });
  const stdout = await new Response(proc.stdout).text();
  const stderr = await new Response(proc.stderr).text();
  const exitCode = await proc.exited;
  return { stdout, stderr, exitCode };
}

describe("read-raw — approval gate", () => {
  it("--approve flag allows non-interactive read", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault, ["notes.md", "--approve"]);
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.chunks.length).toBe(1);
    expect(env.chunks[0]!.curation).toBe("raw-excerpt");
    expect(env.chunks[0]!.source).toBe("raw/notes.md");
  });

  it("headless without --approve fails closed", async () => {
    const vault = makeVault();
    const { exitCode, stderr, stdout } = await run(vault, ["notes.md"]);
    expect(exitCode).not.toBe(0);
    expect(stdout).toBe("");
    expect(stderr).toMatch(/approval|interactive/i);
  });

  it("CAIRN_APPROVE=1 is treated as non-interactive override", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault, ["notes.md"], { CAIRN_APPROVE: "1" });
    expect(exitCode).toBe(0);
    expect(parseEnvelope(stdout).chunks.length).toBe(1);
  });
});

describe("read-raw — bounds", () => {
  it("defaults produce a bounded excerpt, not whole file", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["notes.md", "--approve"]);
    const env = parseEnvelope(stdout);
    const chunk = env.chunks[0]!;
    const lines = chunk.text.split("\n").length;
    expect(lines).toBeLessThan(500);
    expect(chunk.line_range[0]).toBe(1);
  });

  it("--lines N clamps to hard max", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["notes.md", "--approve", "--lines", "10000"]);
    const env = parseEnvelope(stdout);
    const chunk = env.chunks[0]!;
    const lines = chunk.text.split("\n").length;
    expect(lines).toBeLessThanOrEqual(500);
    expect(env.policy.clamped).toBe(true);
  });

  it("--lines smaller than default honored", async () => {
    const vault = makeVault();
    const { stdout } = await run(vault, ["notes.md", "--approve", "--lines", "5"]);
    const env = parseEnvelope(stdout);
    const chunk = env.chunks[0]!;
    expect(chunk.text.split("\n").length).toBe(5);
    expect(chunk.line_range).toEqual([1, 5]);
  });
});

describe("read-raw — path safety", () => {
  it("rejects ../ traversal", async () => {
    const vault = makeVault();
    const { exitCode, stderr } = await run(vault, ["../wiki/auth.md", "--approve"]);
    expect(exitCode).not.toBe(0);
    expect(stderr).toMatch(/invalid|outside|not found/i);
  });

  it("rejects absolute paths", async () => {
    const vault = makeVault();
    const { exitCode } = await run(vault, ["/etc/passwd", "--approve"]);
    expect(exitCode).not.toBe(0);
  });

  it("rejects symlinks that escape raw/", async () => {
    const vault = makeVault();
    const outside = join(vault, "wiki", "auth.md");
    const link = join(vault, "raw", "escape.md");
    symlinkSync(outside, link);
    const { exitCode, stderr } = await run(vault, ["escape.md", "--approve"]);
    expect(exitCode).not.toBe(0);
    expect(stderr).toMatch(/symlink|outside|invalid/i);
  });

  it("rejects directories", async () => {
    const vault = makeVault();
    mkdirSync(join(vault, "raw", "subdir"));
    const { exitCode, stderr } = await run(vault, ["subdir", "--approve"]);
    expect(exitCode).not.toBe(0);
    expect(stderr).toMatch(/regular file|not a file|invalid/i);
  });

  it("missing file returns not-found without stdout content", async () => {
    const vault = makeVault();
    const { exitCode, stderr, stdout } = await run(vault, ["missing.md", "--approve"]);
    expect(exitCode).not.toBe(0);
    expect(stdout).toBe("");
    expect(stderr).toMatch(/not found/i);
  });
});
