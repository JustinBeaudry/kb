import { existsSync, mkdirSync, readFileSync, rmSync, statSync } from "node:fs";
import { join } from "node:path";
import { writeTextAtomic } from "../atomic-write";
import { withExclusiveLock } from "../lockfile";
import { assertGenuineScopeDir } from "../path-safety";
import { buildPage, linkTree, listWikiFiles, toPageId } from "./builder";
import type { PageEntry, TreeCache } from "./types";

const CACHE_SCHEMA_VERSION = "1";

function indexDir(vaultPath: string): string {
  return join(vaultPath, ".kb", "index");
}

function treeFile(vaultPath: string): string {
  return join(indexDir(vaultPath), "tree.json");
}

function lockFile(vaultPath: string): string {
  return join(indexDir(vaultPath), "tree.lock");
}

function readCachedTree(vaultPath: string): TreeCache | null {
  let raw: string;
  try {
    raw = readFileSync(treeFile(vaultPath), "utf-8");
  } catch {
    return null;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (!parsed || typeof parsed !== "object") return null;
  const t = parsed as TreeCache;
  if (t.schema_version !== CACHE_SCHEMA_VERSION) return null;
  if (!Array.isArray(t.pages)) return null;
  return t;
}

async function persistTree(vaultPath: string, tree: TreeCache): Promise<void> {
  const dir = indexDir(vaultPath);
  mkdirSync(dir, { recursive: true });
  // A crafted vault path or a symlinked .kb/index/ must not redirect cache
  // writes outside the vault.
  assertGenuineScopeDir(dir, vaultPath);
  await withExclusiveLock(lockFile(vaultPath), async () => {
    writeTextAtomic(
      treeFile(vaultPath),
      JSON.stringify({ ...tree, built_at: new Date().toISOString() })
    );
  });
}

/**
 * Load the cached tree, revalidating with a stat (size + mtime) fast path:
 * only pages whose stats changed are re-read and re-hashed. A same-size
 * mtime-preserving edit is missed by design; `invalidateTree` is the manual
 * escape hatch.
 */
export async function loadOrBuildTree(vaultPath: string): Promise<TreeCache> {
  const files = listWikiFiles(vaultPath);
  const cached = readCachedTree(vaultPath);

  const cachedById = new Map<string, PageEntry>((cached?.pages ?? []).map((p) => [p.id, p]));
  const pages: PageEntry[] = [];
  let changed = false;
  for (const file of files) {
    const id = toPageId(vaultPath, file);
    const prior = cachedById.get(id);
    if (prior !== undefined) {
      const st = statSync(file);
      if (st.size === prior.size && Math.trunc(st.mtimeMs) === prior.mtime_ms) {
        pages.push(prior);
        cachedById.delete(id);
        continue;
      }
      cachedById.delete(id);
    }
    pages.push(buildPage(vaultPath, file));
    changed = true;
  }
  // Anything left in cachedById was removed from disk.
  if (cachedById.size > 0) changed = true;

  if (cached !== null && !changed) {
    return cached;
  }

  const tree = linkTree(pages);
  await persistTree(vaultPath, tree);
  return tree;
}

export function invalidateTree(vaultPath: string): void {
  rmSync(treeFile(vaultPath), { force: true });
}
