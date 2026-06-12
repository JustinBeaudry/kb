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
