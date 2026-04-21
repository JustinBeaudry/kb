import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir, homedir } from "node:os";
import { mkdirSync, writeFileSync, rmSync } from "node:fs";

const vaults: string[] = [];
afterEach(() => {
  for (const v of vaults.splice(0)) {
    try {
      rmSync(v, { recursive: true, force: true });
    } catch {}
  }
});

function makeCairnVault(parent: string): string {
  const dir = join(parent, `v-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, ".cairn"), { recursive: true });
  mkdirSync(join(dir, "wiki"), { recursive: true });
  mkdirSync(join(dir, "raw"), { recursive: true });
  mkdirSync(join(dir, "sessions"), { recursive: true });
  writeFileSync(
    join(dir, ".cairn", "state.json"),
    JSON.stringify({ version: "0.6.0", vaultPath: dir, createdAt: new Date().toISOString() })
  );
  writeFileSync(join(dir, "CAIRN.md"), "");
  writeFileSync(join(dir, "index.md"), "");
  writeFileSync(join(dir, "log.md"), "");
  writeFileSync(join(dir, "context.md"), "");
  vaults.push(dir);
  return dir;
}

async function runDoctor(vault: string): Promise<{ stdout: string; exitCode: number }> {
  const proc = Bun.spawn(["bun", "src/cli.ts", "doctor", "-p", vault], {
    stdout: "pipe",
    stderr: "pipe",
  });
  const stdout = await new Response(proc.stdout).text();
  const exitCode = await proc.exited;
  return { stdout, exitCode };
}

describe("doctor — trust boundary", () => {
  it("warns when the vault path does not match the default deny globs", async () => {
    const nonDefault = join(tmpdir(), `notes-outside-cairn-${Date.now()}`);
    const vault = makeCairnVault(nonDefault);
    mkdirSync(nonDefault, { recursive: true });
    const { stdout } = await runDoctor(vault);
    expect(stdout).toMatch(/vault path outside default deny globs|deny rules will not fire/);
    vaults.push(nonDefault);
  });

  it("does not warn when the vault path contains a 'cairn' segment", async () => {
    const parent = join(tmpdir(), `doctor-${Date.now()}`, "cairn");
    mkdirSync(parent, { recursive: true });
    const vault = makeCairnVault(parent);
    const { stdout } = await runDoctor(vault);
    expect(stdout).not.toMatch(/vault path outside default deny globs/);
    vaults.push(parent);
  });
});
