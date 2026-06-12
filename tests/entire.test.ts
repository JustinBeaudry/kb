import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, rmSync } from "node:fs";

describe("entire detection", () => {
  let testDir: string;

  beforeEach(() => {
    testDir = join(tmpdir(), `kb-entire-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    mkdirSync(testDir, { recursive: true });
  });

  afterEach(() => {
    rmSync(testDir, { recursive: true, force: true });
  });

  describe("isEntireOnPath", () => {
    it("should return true when entire CLI is installed", async () => {
      if (!Bun.which("entire")) return;
      const { isEntireOnPath } = await import("../src/lib/entire");
      const result = await isEntireOnPath();
      expect(result).toBe(true);
    });
  });
});
