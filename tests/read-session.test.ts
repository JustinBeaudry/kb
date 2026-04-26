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
  const dir = join(tmpdir(), `cairn-read-session-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "sessions"), { recursive: true });
  mkdirSync(join(dir, "raw"), { recursive: true });
  mkdirSync(join(dir, ".cairn"), { recursive: true });
  writeFileSync(
    join(dir, "sessions", "2026-04-14T09-00-00.md"),
    Array.from({ length: 50 }, (_, i) => `session-line-${i + 1}`).join("\n")
  );
  writeFileSync(join(dir, "raw", "other.md"), "raw content\n");
  vaults.push(dir);
  return dir;
}

async function run(vault: string, args: string[], env: Record<string, string> = {}): Promise<{ stdout: string; stderr: string; exitCode: number }> {
  const proc = Bun.spawn(["bun", "src/cli.ts", "read-session", ...args], {
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

describe("read-session", () => {
  it("returns a session-excerpt chunk when approved", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault, ["2026-04-14T09-00-00.md", "--approve"]);
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.chunks[0]!.curation).toBe("session-excerpt");
    expect(env.chunks[0]!.source).toBe("sessions/2026-04-14T09-00-00.md");
  });

  it("fails closed without approval", async () => {
    const vault = makeVault();
    const { exitCode, stderr } = await run(vault, ["2026-04-14T09-00-00.md"]);
    expect(exitCode).not.toBe(0);
    expect(stderr).toMatch(/approval|interactive/i);
  });

  it("rejects escape to raw/", async () => {
    const vault = makeVault();
    const { exitCode, stderr } = await run(vault, ["../raw/other.md", "--approve"]);
    expect(exitCode).not.toBe(0);
    expect(stderr).toMatch(/invalid|outside|not found/i);
  });
});
