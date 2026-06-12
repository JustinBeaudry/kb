import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, rmSync, symlinkSync, writeFileSync } from "node:fs";

const dirs: string[] = [];
afterEach(() => {
  for (const d of dirs.splice(0)) {
    try {
      rmSync(d, { recursive: true, force: true });
    } catch {}
  }
});

function makeVault(): string {
  const dir = join(
    tmpdir(),
    `kb-sessions-list-${Date.now()}-${Math.random().toString(36).slice(2)}`
  );
  mkdirSync(join(dir, "sessions", "summaries"), { recursive: true });
  mkdirSync(join(dir, "sessions", ".trash"), { recursive: true });
  mkdirSync(join(dir, ".kb"), { recursive: true });
  dirs.push(dir);
  return dir;
}

function writeManifest(vault: string, name: string, frontmatter: string): void {
  writeFileSync(join(vault, "sessions", name), `---\n${frontmatter}\n---\n`);
}

async function run(
  vault: string,
  args: string[]
): Promise<{ stdout: string; stderr: string; exitCode: number }> {
  const proc = Bun.spawn(["bun", "src/cli.ts", "sessions", ...args], {
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

describe("sessions --unprocessed", () => {
  it("lists only unprocessed manifest names, nothing from subdirectories", async () => {
    const vault = makeVault();
    writeManifest(vault, "a.md", "extracted: false");
    writeManifest(vault, "b.md", "session_id: x"); // no extracted field counts as unprocessed
    writeManifest(vault, "c.md", "extracted: true");
    writeFileSync(join(vault, "sessions", "summaries", "a.md"), "---\nx: 1\n---\n");
    writeFileSync(join(vault, "sessions", ".trash", "t.md"), "---\nx: 1\n---\n");
    const { stdout, exitCode } = await run(vault, ["--unprocessed"]);
    expect(exitCode).toBe(0);
    expect(stdout.trim().split("\n").filter(Boolean).sort()).toEqual(["a.md", "b.md"]);
  });

  it("lists all manifests without the flag", async () => {
    const vault = makeVault();
    writeManifest(vault, "a.md", "extracted: false");
    writeManifest(vault, "c.md", "extracted: true");
    const { stdout, exitCode } = await run(vault, []);
    expect(exitCode).toBe(0);
    expect(stdout.trim().split("\n").filter(Boolean).sort()).toEqual(["a.md", "c.md"]);
  });

  it("returns nothing and exits 0 for an empty sessions dir", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault, ["--unprocessed"]);
    expect(exitCode).toBe(0);
    expect(stdout.trim()).toBe("");
  });

  it("skips manifests with malformed frontmatter without crashing", async () => {
    const vault = makeVault();
    writeManifest(vault, "good.md", "extracted: false");
    writeManifest(vault, "bad.md", "broken: [unclosed");
    const { stdout, exitCode } = await run(vault, ["--unprocessed"]);
    expect(exitCode).toBe(0);
    expect(stdout).toContain("good.md");
    expect(stdout).not.toContain("bad.md");
  });

  it("refuses a symlinked sessions directory", async () => {
    const dir = join(
      tmpdir(),
      `kb-sessions-sym-${Date.now()}-${Math.random().toString(36).slice(2)}`
    );
    const outside = join(
      tmpdir(),
      `kb-sessions-out-${Date.now()}-${Math.random().toString(36).slice(2)}`
    );
    mkdirSync(dir, { recursive: true });
    mkdirSync(outside, { recursive: true });
    mkdirSync(join(dir, ".kb"), { recursive: true });
    symlinkSync(outside, join(dir, "sessions"));
    dirs.push(dir, outside);
    const { exitCode, stderr } = await run(dir, ["--unprocessed"]);
    expect(exitCode).not.toBe(0);
    expect(stderr).toMatch(/symlink/i);
  });
});
