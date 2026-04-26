import {
  appendFileSync,
  existsSync,
  mkdirSync,
  renameSync,
  rmSync,
  statSync,
} from "node:fs";
import { dirname } from "node:path";
import { withExclusiveLock } from "./lockfile";

const DEFAULT_MAX_BYTES = 2 * 1024 * 1024;

function rotateIfNeeded(logPath: string, maxBytes: number): void {
  if (!existsSync(logPath)) return;
  const size = statSync(logPath).size;
  if (size < maxBytes) return;
  const archive = `${logPath}.1`;
  // Remove any prior archive so renameSync succeeds on platforms where rename
  // does not overwrite (notably Windows). force: true makes this a no-op when
  // the archive does not exist, preserving POSIX behavior. The whole rotation
  // runs inside withExclusiveLock, so this is race-free against other writers.
  rmSync(archive, { force: true });
  renameSync(logPath, archive);
}

export interface AppendOptions {
  logPath: string;
  lockPath: string;
  maxBytes?: number;
}

export async function appendMinimalJsonl(
  entry: Record<string, unknown>,
  opts: AppendOptions
): Promise<void> {
  const { logPath, lockPath } = opts;
  const maxBytes = opts.maxBytes ?? DEFAULT_MAX_BYTES;
  mkdirSync(dirname(logPath), { recursive: true });
  await withExclusiveLock(lockPath, async () => {
    rotateIfNeeded(logPath, maxBytes);
    appendFileSync(logPath, `${JSON.stringify(entry)}\n`);
  });
}
