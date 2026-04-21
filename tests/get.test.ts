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
  const dir = join(tmpdir(), `cairn-get-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "wiki"), { recursive: true });
  mkdirSync(join(dir, "raw"), { recursive: true });
  mkdirSync(join(dir, ".cairn"), { recursive: true });
  writeFileSync(
    join(dir, "wiki", "auth.md"),
    "# Auth Flow\n\nOAuth2 with PKCE.\n\nRefresh tokens rotate.\n"
  );
  writeFileSync(join(dir, "raw", "secret.md"), "SENSITIVE_DATA\n");
  vaults.push(dir);
  return dir;
}

async function run(vault: string, page: string): Promise<{ stdout: string; stderr: string; exitCode: number }> {
  const proc = Bun.spawn(["bun", "src/cli.ts", "get", page], {
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env, CAIRN_VAULT: vault },
  });
  const stdout = await new Response(proc.stdout).text();
  const stderr = await new Response(proc.stderr).text();
  const exitCode = await proc.exited;
  return { stdout, stderr, exitCode };
}

describe("get command", () => {
  it("returns one chunk for an existing wiki page", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault, "auth");
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.chunks.length).toBe(1);
    const chunk = env.chunks[0]!;
    expect(chunk.source).toBe("wiki/auth.md");
    expect(chunk.curation).toBe("curated");
    expect(chunk.line_range[0]).toBe(1);
    expect(chunk.line_range[1]).toBeGreaterThanOrEqual(4);
    expect(chunk.text).toContain("OAuth2");
  });

  it("accepts .md suffix", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault, "auth.md");
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.chunks[0]!.source).toBe("wiki/auth.md");
  });

  it("refuses to read raw/ pages via get", async () => {
    const vault = makeVault();
    const { exitCode, stderr } = await run(vault, "../raw/secret");
    expect(exitCode).not.toBe(0);
    expect(stderr).toMatch(/not found|denied|invalid/i);
  });

  it("refuses absolute paths", async () => {
    const vault = makeVault();
    const { exitCode } = await run(vault, "/etc/passwd");
    expect(exitCode).not.toBe(0);
  });

  it("missing page returns structured error (non-zero exit)", async () => {
    const vault = makeVault();
    const { exitCode, stdout, stderr } = await run(vault, "nonexistent");
    expect(exitCode).not.toBe(0);
    expect(stdout).toBe("");
    expect(stderr).toMatch(/not found/i);
  });
});
