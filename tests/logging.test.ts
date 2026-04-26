import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, writeFileSync, rmSync, readFileSync, existsSync, statSync } from "node:fs";

const vaults: string[] = [];
afterEach(() => {
  for (const v of vaults.splice(0)) {
    try {
      rmSync(v, { recursive: true, force: true });
    } catch {}
  }
});

function makeVault(): string {
  const dir = join(tmpdir(), `cairn-log-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "wiki"), { recursive: true });
  mkdirSync(join(dir, ".cairn"), { recursive: true });
  writeFileSync(join(dir, "wiki", "auth.md"), "# Auth\n\nOAuth2 content\n");
  writeFileSync(join(dir, "index.md"), "# Index\n\n## Arch\n- [[Auth]]\n");
  vaults.push(dir);
  return dir;
}

async function run(vault: string, cmd: string[]): Promise<{ stdout: string; exitCode: number }> {
  const proc = Bun.spawn(["bun", "src/cli.ts", ...cmd], {
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env, CAIRN_VAULT: vault },
  });
  const stdout = await new Response(proc.stdout).text();
  const exitCode = await proc.exited;
  return { stdout, exitCode };
}

describe("access log — minimized entries", () => {
  it("recall appends a single JSONL entry with hash + len, never plaintext query", async () => {
    const vault = makeVault();
    await run(vault, ["recall", "SUPER_SECRET_QUERY"]);
    const logPath = join(vault, ".cairn", "access-log.jsonl");
    expect(existsSync(logPath)).toBe(true);
    const raw = readFileSync(logPath, "utf-8");
    expect(raw).not.toContain("SUPER_SECRET_QUERY");
    const lines = raw.trim().split("\n").filter(Boolean);
    expect(lines.length).toBe(1);
    const entry = JSON.parse(lines[0]!);
    expect(entry.command).toBe("recall");
    expect(entry.query_hash).toMatch(/^[0-9a-f]{16,64}$/);
    expect(entry.query_len).toBe("SUPER_SECRET_QUERY".length);
    expect(typeof entry.bytes_returned).toBe("number");
    expect(entry.exit_code).toBe(0);
  });

  it("get appends an access log entry", async () => {
    const vault = makeVault();
    await run(vault, ["get", "auth"]);
    const raw = readFileSync(join(vault, ".cairn", "access-log.jsonl"), "utf-8");
    const entry = JSON.parse(raw.trim().split("\n").filter(Boolean)[0]!);
    expect(entry.command).toBe("get");
    expect(entry.pages_returned).toBe(1);
  });

  it("list-topics appends an access log entry", async () => {
    const vault = makeVault();
    await run(vault, ["list-topics"]);
    const raw = readFileSync(join(vault, ".cairn", "access-log.jsonl"), "utf-8");
    const entry = JSON.parse(raw.trim().split("\n").filter(Boolean)[0]!);
    expect(entry.command).toBe("list-topics");
  });
});

describe("access log — concurrency", () => {
  it("parallel recalls produce one valid JSONL line each", async () => {
    const vault = makeVault();
    await Promise.all(Array.from({ length: 5 }).map(() => run(vault, ["recall", "OAuth2"])));
    const raw = readFileSync(join(vault, ".cairn", "access-log.jsonl"), "utf-8");
    const lines = raw.trim().split("\n").filter(Boolean);
    expect(lines.length).toBe(5);
    for (const line of lines) {
      expect(() => JSON.parse(line)).not.toThrow();
    }
  });
});

describe("log rotation", () => {
  it("rotates access-log.jsonl when size cap exceeded", async () => {
    const vault = makeVault();
    const logPath = join(vault, ".cairn", "access-log.jsonl");
    mkdirSync(join(vault, ".cairn"), { recursive: true });
    const bigLine = "x".repeat(3 * 1024 * 1024);
    writeFileSync(logPath, bigLine + "\n");
    expect(statSync(logPath).size).toBeGreaterThan(2 * 1024 * 1024);

    await run(vault, ["list-topics"]);

    const rotated = logPath + ".1";
    expect(existsSync(rotated)).toBe(true);
    expect(statSync(logPath).size).toBeLessThan(2 * 1024 * 1024);
    const current = readFileSync(logPath, "utf-8").trim();
    expect(current.split("\n").length).toBe(1);
  });

  it("rotation preserves an older archive (overwrites existing .1)", async () => {
    const vault = makeVault();
    const logPath = join(vault, ".cairn", "access-log.jsonl");
    mkdirSync(join(vault, ".cairn"), { recursive: true });
    writeFileSync(logPath + ".1", "stale-archive\n");
    const bigLine = "y".repeat(3 * 1024 * 1024);
    writeFileSync(logPath, bigLine + "\n");

    await run(vault, ["list-topics"]);

    const archived = readFileSync(logPath + ".1", "utf-8");
    expect(archived).toContain("y");
    expect(archived).not.toContain("stale-archive");
  });
});
