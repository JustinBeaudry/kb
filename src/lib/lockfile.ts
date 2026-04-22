import {
  closeSync,
  mkdirSync,
  openSync,
  readFileSync,
  unlinkSync,
  writeSync,
} from "node:fs";
import { dirname, join } from "node:path";

export interface LockOptions {
  staleMs?: number;
  retryMs?: number;
  retries?: number;
}

interface LockPayload {
  pid: number;
  createdAt: string;
}

const DEFAULTS: Required<LockOptions> = {
  staleMs: 5 * 60 * 1000,
  retryMs: 100,
  retries: 50,
};

export class LockBusyError extends Error {
  constructor(path: string) {
    super(`Lock is busy after retry budget exhausted: ${path}`);
    this.name = "LockBusyError";
  }
}

function tryAcquire(lockPath: string): boolean {
  try {
    const fd = openSync(lockPath, "wx");
    const payload: LockPayload = {
      pid: process.pid,
      createdAt: new Date().toISOString(),
    };
    writeSync(fd, JSON.stringify(payload));
    closeSync(fd);
    return true;
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code;
    if (code === "EEXIST") return false;
    if (code === "ENOENT") {
      mkdirSync(dirname(lockPath), { recursive: true });
      return tryAcquire(lockPath);
    }
    throw err;
  }
}

function isStale(lockPath: string, staleMs: number): boolean {
  try {
    const raw = readFileSync(lockPath, "utf-8");
    const parsed = JSON.parse(raw) as LockPayload;
    const createdAt = Date.parse(parsed.createdAt);
    if (!Number.isFinite(createdAt)) return false;
    return Date.now() - createdAt > staleMs;
  } catch {
    return false;
  }
}

function reclaim(lockPath: string): void {
  try {
    unlinkSync(lockPath);
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code;
    if (code !== "ENOENT") throw err;
  }
}

function releaseLock(lockPath: string): void {
  try {
    unlinkSync(lockPath);
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code;
    if (code !== "ENOENT") throw err;
  }
}

export async function withExclusiveLock<T>(
  lockPath: string,
  fn: () => Promise<T>,
  opts: LockOptions = {}
): Promise<T> {
  const { staleMs, retryMs, retries } = { ...DEFAULTS, ...opts };

  for (let attempt = 0; attempt <= retries; attempt++) {
    if (tryAcquire(lockPath)) {
      try {
        return await fn();
      } finally {
        releaseLock(lockPath);
      }
    }

    if (isStale(lockPath, staleMs)) {
      reclaim(lockPath);
      continue;
    }

    if (attempt === retries) break;
    await new Promise((resolve) => setTimeout(resolve, retryMs));
  }

  throw new LockBusyError(lockPath);
}

export async function withLogLock<T>(
  vaultPath: string,
  fn: () => Promise<T>,
  opts: LockOptions = {}
): Promise<T> {
  return withExclusiveLock(join(vaultPath, ".cairn", "log.lock"), fn, opts);
}

export async function withMigrationLock<T>(
  vaultPath: string,
  fn: () => Promise<T>,
  opts: LockOptions = {}
): Promise<T> {
  return withExclusiveLock(join(vaultPath, ".cairn", "migration.lock"), fn, opts);
}
