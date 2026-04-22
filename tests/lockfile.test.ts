import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { mkdtempSync, rmSync, existsSync, writeFileSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  withExclusiveLock,
  withLogLock,
  withMigrationLock,
  LockBusyError,
} from "../src/lib/lockfile";

describe("withExclusiveLock", () => {
  let dir: string;

  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "cairn-lock-"));
  });

  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it("runs the function and removes the lockfile on success", async () => {
    const lockPath = join(dir, "my.lock");
    let ran = false;

    await withExclusiveLock(lockPath, async () => {
      ran = true;
      expect(existsSync(lockPath)).toBe(true);
    });

    expect(ran).toBe(true);
    expect(existsSync(lockPath)).toBe(false);
  });

  it("removes the lockfile even when the function throws", async () => {
    const lockPath = join(dir, "my.lock");

    await expect(
      withExclusiveLock(lockPath, async () => {
        throw new Error("boom");
      })
    ).rejects.toThrow("boom");

    expect(existsSync(lockPath)).toBe(false);
  });

  it("writes PID and createdAt JSON into the lockfile", async () => {
    const lockPath = join(dir, "my.lock");
    let captured: string = "";

    await withExclusiveLock(lockPath, async () => {
      captured = readFileSync(lockPath, "utf-8");
    });

    const parsed = JSON.parse(captured);
    expect(parsed.pid).toBe(process.pid);
    expect(typeof parsed.createdAt).toBe("string");
    expect(new Date(parsed.createdAt).toString()).not.toBe("Invalid Date");
  });

  it("blocks a second concurrent acquirer until the first releases", async () => {
    const lockPath = join(dir, "my.lock");
    const order: string[] = [];

    const first = withExclusiveLock(lockPath, async () => {
      order.push("first-start");
      await new Promise((r) => setTimeout(r, 50));
      order.push("first-end");
    });

    // Start second after first has surely acquired.
    await new Promise((r) => setTimeout(r, 10));
    const second = withExclusiveLock(
      lockPath,
      async () => {
        order.push("second-start");
        order.push("second-end");
      },
      { retryMs: 20, retries: 20 }
    );

    await Promise.all([first, second]);

    expect(order).toEqual([
      "first-start",
      "first-end",
      "second-start",
      "second-end",
    ]);
  });

  it("reclaims a stale lockfile whose createdAt is older than staleMs", async () => {
    const lockPath = join(dir, "stale.lock");
    writeFileSync(
      lockPath,
      JSON.stringify({
        pid: 99999,
        createdAt: new Date(Date.now() - 10 * 60 * 1000).toISOString(),
      })
    );

    let ran = false;
    await withExclusiveLock(
      lockPath,
      async () => {
        ran = true;
      },
      { staleMs: 60_000, retries: 1 }
    );

    expect(ran).toBe(true);
    expect(existsSync(lockPath)).toBe(false);
  });

  it("withLogLock acquires .cairn/log.lock under the vault", async () => {
    const vaultDir = mkdtempSync(join(tmpdir(), "cairn-vault-"));
    try {
      let ran = false;
      await withLogLock(vaultDir, async () => {
        ran = true;
        expect(existsSync(join(vaultDir, ".cairn", "log.lock"))).toBe(true);
      });
      expect(ran).toBe(true);
      expect(existsSync(join(vaultDir, ".cairn", "log.lock"))).toBe(false);
    } finally {
      rmSync(vaultDir, { recursive: true, force: true });
    }
  });

  it("withMigrationLock acquires .cairn/migration.lock under the vault", async () => {
    const vaultDir = mkdtempSync(join(tmpdir(), "cairn-vault-"));
    try {
      let ran = false;
      await withMigrationLock(vaultDir, async () => {
        ran = true;
        expect(existsSync(join(vaultDir, ".cairn", "migration.lock"))).toBe(true);
      });
      expect(ran).toBe(true);
    } finally {
      rmSync(vaultDir, { recursive: true, force: true });
    }
  });

  it("throws LockBusyError when retry budget is exhausted", async () => {
    const lockPath = join(dir, "busy.lock");

    // Simulate a fresh, still-valid lockfile that we refuse to reclaim.
    writeFileSync(
      lockPath,
      JSON.stringify({ pid: process.pid, createdAt: new Date().toISOString() })
    );

    await expect(
      withExclusiveLock(
        lockPath,
        async () => {
          /* never reached */
        },
        { staleMs: 60_000, retryMs: 5, retries: 3 }
      )
    ).rejects.toBeInstanceOf(LockBusyError);

    // Lockfile untouched.
    expect(existsSync(lockPath)).toBe(true);
  });
});
