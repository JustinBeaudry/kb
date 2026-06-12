import { describe, it, expect, afterEach } from "bun:test";
import { createHash } from "node:crypto";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";

const vaults: string[] = [];
afterEach(() => {
  for (const v of vaults.splice(0)) {
    try {
      rmSync(v, { recursive: true, force: true });
    } catch {}
  }
});

function makeVault(): string {
  const dir = join(
    tmpdir(),
    `kb-mark-extracted-${Date.now()}-${Math.random().toString(36).slice(2)}`
  );
  mkdirSync(join(dir, "sessions", "summaries"), { recursive: true });
  mkdirSync(join(dir, ".kb"), { recursive: true });
  vaults.push(dir);
  return dir;
}

function writeManifest(vault: string, name: string, frontmatter: string, body = ""): string {
  const path = join(vault, "sessions", name);
  writeFileSync(path, `---\n${frontmatter}\n---\n${body}`);
  return path;
}

async function run(
  vault: string,
  args: string[]
): Promise<{ stdout: string; stderr: string; exitCode: number }> {
  const proc = Bun.spawn(["bun", "src/cli.ts", "mark-extracted", ...args], {
    stdout: "pipe",
    stderr: "pipe",
    stdin: "ignore",
    env: { ...process.env, KB_VAULT: vault },
  });
  const stdout = await new Response(proc.stdout).text();
  const stderr = await new Response(proc.stderr).text();
  const exitCode = await proc.exited;
  return { stdout, stderr, exitCode };
}

describe("mark-extracted", () => {
  it("flips extracted to true and preserves other fields and body", async () => {
    const vault = makeVault();
    const path = writeManifest(
      vault,
      "2026-06-10T10-00-00.md",
      'session_id: abc123\nextracted: false\ntags:\n  - foo',
      "body line\n"
    );
    const { exitCode } = await run(vault, ["2026-06-10T10-00-00.md"]);
    expect(exitCode).toBe(0);
    const content = readFileSync(path, "utf-8");
    expect(content).toMatch(/extracted: true/);
    expect(content).toMatch(/session_id: abc123/);
    expect(content).toMatch(/- foo/);
    expect(content).toMatch(/body line/);
  });

  it("is idempotent on already-extracted manifests", async () => {
    const vault = makeVault();
    const path = writeManifest(vault, "done.md", "session_id: x1\nextracted: true");
    const { exitCode } = await run(vault, ["done.md"]);
    expect(exitCode).toBe(0);
    const content = readFileSync(path, "utf-8");
    expect(content).toMatch(/extracted: true/);
    expect(content).toMatch(/session_id: x1/);
  });

  it("fails with a clear message on a missing manifest and audits the failure", async () => {
    const vault = makeVault();
    const { exitCode, stderr } = await run(vault, ["nope.md"]);
    expect(exitCode).not.toBe(0);
    expect(stderr).toContain("nope.md");
    expect(existsSync(join(vault, "sessions", "nope.md"))).toBe(false);
    const log = readFileSync(join(vault, ".kb", "access-log.jsonl"), "utf-8").trim();
    const entry = JSON.parse(log.split("\n").pop()!) as Record<string, unknown>;
    expect(entry.command).toBe("mark-extracted");
    expect(entry.exit_code).toBe(1);
  });

  it("rejects non-markdown filenames", async () => {
    const vault = makeVault();
    const { exitCode } = await run(vault, ["manifest.txt"]);
    expect(exitCode).not.toBe(0);
  });

  it("refuses a symlinked manifest file", async () => {
    const vault = makeVault();
    const outside = join(tmpdir(), `kb-me-out-${Date.now()}.md`);
    writeFileSync(outside, "---\nextracted: false\n---\n");
    const { symlinkSync } = await import("node:fs");
    symlinkSync(outside, join(vault, "sessions", "linked.md"));
    const { exitCode } = await run(vault, ["linked.md"]);
    expect(exitCode).not.toBe(0);
    expect(readFileSync(outside, "utf-8")).toContain("extracted: false");
    rmSync(outside, { force: true });
  });

  it("refuses files without parseable frontmatter instead of corrupting them", async () => {
    const vault = makeVault();
    const unclosed = join(vault, "sessions", "unclosed.md");
    writeFileSync(unclosed, "---\nsession_id: abc\nno closing delimiter\n");
    const none = join(vault, "sessions", "plain.md");
    writeFileSync(none, "just some text, no frontmatter\n");
    for (const [name, path] of [
      ["unclosed.md", unclosed],
      ["plain.md", none],
    ] as const) {
      const before = readFileSync(path, "utf-8");
      const { exitCode, stderr } = await run(vault, [name]);
      expect(exitCode).not.toBe(0);
      expect(stderr).toMatch(/not a manifest/);
      expect(readFileSync(path, "utf-8")).toBe(before);
    }
  });

  it("rejects traversal and subpath targets", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "sessions", "summaries", "s.md"), "---\na: 1\n---\n");
    const escape = await run(vault, ["../escape.md"]);
    expect(escape.exitCode).not.toBe(0);
    const subpath = await run(vault, ["summaries/s.md"]);
    expect(subpath.exitCode).not.toBe(0);
  });

  it("exits nonzero on malformed YAML without modifying the file", async () => {
    const vault = makeVault();
    const path = writeManifest(vault, "bad.md", "session_id: [unclosed");
    const before = readFileSync(path, "utf-8");
    const { exitCode } = await run(vault, ["bad.md"]);
    expect(exitCode).not.toBe(0);
    expect(readFileSync(path, "utf-8")).toBe(before);
  });

  it("appends a write-audit access-log entry with hashed target", async () => {
    const vault = makeVault();
    writeManifest(vault, "logme.md", "extracted: false");
    const { exitCode } = await run(vault, ["logme.md"]);
    expect(exitCode).toBe(0);
    const log = readFileSync(join(vault, ".kb", "access-log.jsonl"), "utf-8").trim();
    const lines = log.split("\n");
    const entry = JSON.parse(lines[lines.length - 1]!) as Record<string, unknown>;
    expect(entry.command).toBe("mark-extracted");
    expect(entry.target_hash).toBe(
      createHash("sha256").update("logme.md").digest("hex").slice(0, 32)
    );
    expect(entry.exit_code).toBe(0);
    expect(log).not.toContain("logme.md");
  });
});
