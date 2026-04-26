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
  const dir = join(tmpdir(), `cairn-list-topics-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "wiki"), { recursive: true });
  mkdirSync(join(dir, ".cairn"), { recursive: true });
  writeFileSync(
    join(dir, "index.md"),
    [
      "# Vault Index",
      "",
      "## Architecture",
      "- [[Auth]]",
      "",
      "## People",
      "- [[Team]]",
      "",
    ].join("\n")
  );
  vaults.push(dir);
  return dir;
}

async function run(vault: string, ...extraArgs: string[]): Promise<{ stdout: string; stderr: string; exitCode: number }> {
  const proc = Bun.spawn(["bun", "src/cli.ts", "list-topics", ...extraArgs], {
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env, CAIRN_VAULT: vault },
  });
  const stdout = await new Response(proc.stdout).text();
  const stderr = await new Response(proc.stderr).text();
  const exitCode = await proc.exited;
  return { stdout, stderr, exitCode };
}

describe("list-topics command", () => {
  it("returns one envelope chunk per heading with accurate line numbers", async () => {
    const vault = makeVault();
    const { stdout, exitCode } = await run(vault);
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.schema_version).toBe("1");
    expect(env.policy).toMatchObject({ source_scope: "wiki" });
    const texts = env.chunks.map((c) => c.text);
    expect(texts).toContain("Architecture");
    expect(texts).toContain("People");
    for (const chunk of env.chunks) {
      expect(chunk.source).toBe("index.md");
      expect(chunk.curation).toBe("curated");
      expect(chunk.line_range[0]).toBe(chunk.line_range[1]);
      expect(chunk.line_range[0]).toBeGreaterThan(0);
    }
  });

  it("missing index.md returns envelope with empty chunks, not an error", async () => {
    const vault = makeVault();
    rmSync(join(vault, "index.md"));
    const { stdout, exitCode } = await run(vault);
    expect(exitCode).toBe(0);
    const env = parseEnvelope(stdout);
    expect(env.chunks).toEqual([]);
  });

  it("missing vault exits non-zero without leaking content", async () => {
    const { exitCode, stderr, stdout } = await run("/tmp/does-not-exist-cairn-" + Date.now());
    expect(exitCode).not.toBe(0);
    expect(stdout).toBe("");
    expect(stderr).toMatch(/vault/i);
  });
});
