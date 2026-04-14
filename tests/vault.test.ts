import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { resolveVaultPath, checkVaultState } from "../src/lib/vault";
import { DEFAULT_VAULT_PATH } from "../src/lib/constants";
import { mkdirSync, writeFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";

describe("resolveVaultPath", () => {
  const originalEnv = process.env.CAIRN_VAULT;

  afterEach(() => {
    if (originalEnv === undefined) {
      delete process.env.CAIRN_VAULT;
    } else {
      process.env.CAIRN_VAULT = originalEnv;
    }
  });

  it("should use CAIRN_VAULT env var when set", () => {
    process.env.CAIRN_VAULT = "/tmp/test-cairn-vault";
    expect(resolveVaultPath()).toBe("/tmp/test-cairn-vault");
  });

  it("should use .cairn file in project root when present", () => {
    const testDir = join(tmpdir(), `cairn-test-${Date.now()}`);
    mkdirSync(testDir, { recursive: true });
    writeFileSync(join(testDir, ".cairn"), "/tmp/project-vault\n");
    delete process.env.CAIRN_VAULT;
    expect(resolveVaultPath(testDir)).toBe("/tmp/project-vault");
    rmSync(testDir, { recursive: true });
  });

  it("should fall back to ~/cairn", () => {
    delete process.env.CAIRN_VAULT;
    expect(resolveVaultPath("/nonexistent")).toBe(DEFAULT_VAULT_PATH);
  });
});

describe("checkVaultState", () => {
  it("should return 'empty' for non-existent path", () => {
    expect(checkVaultState("/tmp/does-not-exist-cairn")).toBe("empty");
  });

  it("should return 'cairn' when .cairn/state.json exists", () => {
    const testDir = join(tmpdir(), `cairn-test-${Date.now()}`);
    mkdirSync(join(testDir, ".cairn"), { recursive: true });
    writeFileSync(
      join(testDir, ".cairn", "state.json"),
      JSON.stringify({ version: "0.1.0" })
    );
    expect(checkVaultState(testDir)).toBe("cairn");
    rmSync(testDir, { recursive: true });
  });

  it("should return 'obsidian' when .obsidian/ exists", () => {
    const testDir = join(tmpdir(), `cairn-test-${Date.now()}`);
    mkdirSync(join(testDir, ".obsidian"), { recursive: true });
    expect(checkVaultState(testDir)).toBe("obsidian");
    rmSync(testDir, { recursive: true });
  });

  it("should return 'occupied' when directory has other content", () => {
    const testDir = join(tmpdir(), `cairn-test-${Date.now()}`);
    mkdirSync(testDir, { recursive: true });
    writeFileSync(join(testDir, "something.txt"), "hello");
    expect(checkVaultState(testDir)).toBe("occupied");
    rmSync(testDir, { recursive: true });
  });
});
