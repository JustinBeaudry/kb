import {
  closeSync,
  constants as fsConstants,
  copyFileSync,
  existsSync,
  openSync,
  readFileSync,
  readdirSync,
  renameSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import { parseFrontmatter, serializeFrontmatter } from "./frontmatter";
import { assertGenuineScopeDir, assertSafeFilename, PathUnsafeError } from "./path-safety";

/** Read a file refusing to follow a symlink at the final path component. */
function readNoFollow(path: string): string {
  const fd = openSync(path, fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW);
  try {
    return readFileSync(fd, "utf-8");
  } finally {
    closeSync(fd);
  }
}

function writeTextAtomic(path: string, content: string): void {
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

function scanManifests(
  vaultPath: string,
  predicate: (frontmatter: Record<string, unknown>) => boolean
): string[] {
  const dir = join(vaultPath, "sessions");
  assertGenuineScopeDir(dir, vaultPath);
  const names: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    // Files only at the top level: subdirectories (summaries/, .trash/) and
    // symlinks are never scanned.
    if (!entry.isFile()) continue;
    if (!entry.name.endsWith(".md")) continue;
    try {
      const { data } = parseFrontmatter<Record<string, unknown>>(
        readNoFollow(join(dir, entry.name))
      );
      if (predicate(data)) names.push(entry.name);
    } catch {
      // Malformed frontmatter or unreadable file: skip, never crash a scan.
      continue;
    }
  }
  return names.sort();
}

/** Names of all top-level session manifests. */
export function listManifests(vaultPath: string): string[] {
  return scanManifests(vaultPath, () => true);
}

/** Names of manifests whose frontmatter does not carry `extracted: true`. */
export function listUnprocessedManifests(vaultPath: string): string[] {
  return scanManifests(vaultPath, (data) => data.extracted !== true);
}

/**
 * One-line session-start nudge, or null. Fail-soft by contract: any error
 * (missing/corrupt state.json, symlinked sessions dir, unreadable manifests)
 * means no nudge — this runs inside the inject hook, which never fails.
 * Acts only on a strict `autoExtractNudge === true`; unrecognized state keys
 * and non-boolean values are ignored.
 */
export function buildNudgeLine(vaultPath: string): string | null {
  try {
    const raw = readFileSync(join(vaultPath, ".kb", "state.json"), "utf-8");
    const state = JSON.parse(raw) as { autoExtractNudge?: unknown };
    if (state.autoExtractNudge !== true) return null;
    const count = listUnprocessedManifests(vaultPath).length;
    if (!Number.isInteger(count) || count <= 0) return null;
    return `${count} unprocessed session manifest(s) — run /kb:extract`;
  } catch {
    return null;
  }
}

/**
 * Flip `extracted: true` on a top-level session manifest. Fail-hard: invalid
 * paths, missing files, and malformed YAML all throw without touching the file.
 */
export function markExtracted(vaultPath: string, filename: string): void {
  assertSafeFilename(filename);
  if (filename.split(/[\\/]/).length > 1) {
    throw new PathUnsafeError(`manifests only — subpaths not allowed: ${filename}`);
  }
  if (!filename.endsWith(".md")) {
    throw new PathUnsafeError(`not a manifest file: ${filename}`);
  }
  const dir = join(vaultPath, "sessions");
  assertGenuineScopeDir(dir, vaultPath);
  const path = join(dir, filename);
  if (!existsSync(path)) {
    throw new Error(`manifest not found: ${filename}`);
  }
  const { data, body } = parseFrontmatter<Record<string, unknown>>(readNoFollow(path));
  data.extracted = true;
  writeTextAtomic(path, serializeFrontmatter(data, body));
}
