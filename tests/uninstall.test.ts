import { describe, it, expect } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { existsSync, rmSync, readFileSync, mkdirSync, writeFileSync } from "node:fs";
import { CAIRN_HOOK_MARKER } from "../src/lib/settings";

describe("cairn uninstall (integration)", () => {
  it("should remove hooks and preserve vault", async () => {
    const vaultDir = join(tmpdir(), `cairn-uninst-${Date.now()}`);
    const settingsDir = join(tmpdir(), `cairn-settings-${Date.now()}`);
    mkdirSync(settingsDir, { recursive: true });
    const settingsPath = join(settingsDir, "settings.json");
    writeFileSync(settingsPath, "{}");

    await Bun.spawn(["bun", "run", "src/cli.ts", "init", "--vault-path", vaultDir, "--settings-path", settingsPath], { stdout: "pipe", stderr: "pipe" }).exited;

    const proc = Bun.spawn(["bun", "run", "src/cli.ts", "uninstall", "--force", "--settings-path", settingsPath], { stdout: "pipe", stderr: "pipe" });
    const exitCode = await proc.exited;
    expect(exitCode).toBe(0);

    const settings = JSON.parse(readFileSync(settingsPath, "utf-8"));
    expect(JSON.stringify(settings)).not.toContain(CAIRN_HOOK_MARKER);
    expect(existsSync(join(vaultDir, "CAIRN.md"))).toBe(true);

    rmSync(vaultDir, { recursive: true });
    rmSync(settingsDir, { recursive: true });
  });
});
