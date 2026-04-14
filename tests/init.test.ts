import { describe, it, expect } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { existsSync, rmSync, readFileSync, mkdirSync, writeFileSync } from "node:fs";

describe("cairn init (integration)", () => {
  it("should scaffold vault at custom path", async () => {
    const vaultDir = join(tmpdir(), `cairn-init-${Date.now()}`);
    const settingsDir = join(tmpdir(), `cairn-settings-${Date.now()}`);
    mkdirSync(settingsDir, { recursive: true });
    const settingsPath = join(settingsDir, "settings.json");
    writeFileSync(settingsPath, "{}");

    const proc = Bun.spawn(["bun", "run", "src/cli.ts", "init", "--vault-path", vaultDir, "--settings-path", settingsPath], { stdout: "pipe", stderr: "pipe" });
    const exitCode = await proc.exited;

    expect(exitCode).toBe(0);
    expect(existsSync(join(vaultDir, "wiki"))).toBe(true);
    expect(existsSync(join(vaultDir, "CAIRN.md"))).toBe(true);
    expect(existsSync(join(vaultDir, ".cairn", "state.json"))).toBe(true);
    const settings = JSON.parse(readFileSync(settingsPath, "utf-8"));
    expect(settings.hooks?.SessionStart).toBeDefined();

    rmSync(vaultDir, { recursive: true });
    rmSync(settingsDir, { recursive: true });
  });

  it("should refuse occupied directory", async () => {
    const vaultDir = join(tmpdir(), `cairn-init-${Date.now()}`);
    mkdirSync(vaultDir, { recursive: true });
    writeFileSync(join(vaultDir, "existing.txt"), "content");

    const proc = Bun.spawn(["bun", "run", "src/cli.ts", "init", "--vault-path", vaultDir], { stdout: "pipe", stderr: "pipe" });
    const exitCode = await proc.exited;
    expect(exitCode).toBe(1);

    rmSync(vaultDir, { recursive: true });
  });
});
