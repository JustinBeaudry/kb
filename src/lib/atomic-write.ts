import { copyFileSync, renameSync, unlinkSync, writeFileSync } from "node:fs";

/**
 * Write a file atomically: tmp + rename, with a copy fallback when the tmp
 * lands on a different filesystem (EXDEV).
 */
export function writeTextAtomic(path: string, content: string): void {
  const tmp = `${path}.tmp-${process.pid}-${Date.now()}`;
  writeFileSync(tmp, content);
  try {
    renameSync(tmp, path);
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code !== "EXDEV") throw err;
    copyFileSync(tmp, path);
    unlinkSync(tmp);
  }
}

/** Move a file with the same EXDEV copy fallback. */
export function moveFileAtomic(from: string, to: string): void {
  try {
    renameSync(from, to);
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code !== "EXDEV") throw err;
    copyFileSync(from, to);
    unlinkSync(from);
  }
}
