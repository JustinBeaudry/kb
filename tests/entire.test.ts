import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, rmSync } from "node:fs";

describe("entire detection", () => {
  let testDir: string;

  beforeEach(() => {
    testDir = join(tmpdir(), `cairn-entire-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    mkdirSync(testDir, { recursive: true });
  });

  afterEach(() => {
    rmSync(testDir, { recursive: true, force: true });
  });

  describe("isEntireEnabled", () => {
    it("should return false when not a git repo", async () => {
      const { isEntireEnabled } = await import("../src/lib/entire");
      const result = await isEntireEnabled(testDir);
      expect(result).toBe(false);
    });

    it("should return false when git repo but entire not enabled", async () => {
      const proc = Bun.spawn(["git", "init", testDir], { stdout: "ignore", stderr: "ignore" });
      await proc.exited;

      const { isEntireEnabled } = await import("../src/lib/entire");
      const result = await isEntireEnabled(testDir);
      expect(result).toBe(false);
    });

    it("should return true when entire is enabled in project", async () => {
      const { isEntireEnabled } = await import("../src/lib/entire");
      // This test runs in the cairn project which has entire enabled
      const result = await isEntireEnabled(process.cwd());
      expect(result).toBe(true);
    });
  });

  describe("isEntireOnPath", () => {
    it("should return true when entire CLI is installed", async () => {
      const { isEntireOnPath } = await import("../src/lib/entire");
      const result = await isEntireOnPath();
      expect(result).toBe(true);
    });
  });
});
