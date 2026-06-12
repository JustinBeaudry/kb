import {
  closeSync,
  constants as fsConstants,
  fstatSync,
  lstatSync,
  openSync,
  readFileSync,
  readdirSync,
  realpathSync,
} from "node:fs";
import { join } from "node:path";
import { isWithin } from "./path-safety";

export const MAX_FILE_BYTES = 256 * 1024;

export function walkWiki(
  wikiDir: string,
  wikiReal: string,
  onFile: (path: string) => void
): void {
  for (const entry of readdirSync(wikiDir)) {
    if (entry.startsWith(".")) continue;
    const full = join(wikiDir, entry);
    const lst = lstatSync(full);
    if (lst.isSymbolicLink()) continue;
    if (lst.isDirectory()) {
      walkWiki(full, wikiReal, onFile);
    } else if (lst.isFile() && entry.endsWith(".md")) {
      let real: string;
      try {
        real = realpathSync(full);
      } catch {
        continue;
      }
      if (!isWithin(real, wikiReal)) continue;
      onFile(full);
    }
  }
}

// Read a wiki file via an fd opened with O_NOFOLLOW, gating on the fd's own
// stat. This closes the TOCTOU window between the traversal-time lstat in
// walkWiki and the read here: if the path was swapped to a symlink after
// traversal, openSync rejects with ELOOP; if it was swapped to a non-regular
// file, fstatSync on the open fd surfaces it before we read any bytes.
export function readWikiFileNoFollow(path: string): string | null {
  let fd: number;
  try {
    fd = openSync(path, fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW);
  } catch {
    return null;
  }
  try {
    const st = fstatSync(fd);
    if (!st.isFile()) return null;
    if (st.size > MAX_FILE_BYTES) return null;
    return readFileSync(fd, "utf-8");
  } catch {
    return null;
  } finally {
    closeSync(fd);
  }
}
