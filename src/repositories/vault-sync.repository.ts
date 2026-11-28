import { lstatSync, mkdirSync, readdirSync, readFileSync, renameSync, rmdirSync, unlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { Result } from "../capture/types";
import { LockBusyError, withExclusiveLock } from "../lib/lockfile";
import { isWithin } from "../lib/path-safety";
import { validVaultID, type VaultError } from "./vault.repository";

export const SYNC_STATE_FILE = ".kb-vault-sync.json";
/** Held while a sync runs, so a scheduled run and a manual one never interleave. */
export const SYNC_LOCK_FILE = ".kb-vault-sync.lock";
/** A sync of the largest export finishes well within this; an older lock is a crashed run's. */
const SYNC_LOCK_STALE_MS = 30 * 60 * 1000;

export type SyncState = {
  version: "kb-vault-sync-1";
  scope: { project_id: string; task_id?: string; session_id?: string };
  /** Every file this folder's syncs have written and not yet removed. */
  files: string[];
};
export type WriteOutcome = "written" | "unchanged";
export type SyncFolderError = VaultError | { code: "busy" };

/** A folder that vault sync owns. It never touches a file it did not write. */
export interface SyncFolder {
  /**
   * Runs fn while holding the folder's lock. Another sync of the same folder
   * in progress gives busy, and fn does not run.
   */
  exclusive<T>(fn: () => Promise<T>): Promise<Result<T, SyncFolderError>>;
  open(): Promise<Result<SyncState | undefined, VaultError>>;
  /** Whether anything (file, folder or link) is at a sync path. */
  occupied(path: string): Promise<Result<boolean, VaultError>>;
  write(path: string, content: string): Promise<Result<WriteOutcome, VaultError>>;
  remove(path: string): Promise<Result<void, VaultError>>;
  save(state: SyncState): Promise<Result<void, VaultError>>;
}

const fail = (code: VaultError["code"]): Result<never, VaultError> => ({ ok: false, error: { code } });
function errno(e: unknown): string | undefined {
  return e && typeof e === "object" && "code" in e && typeof e.code === "string" ? e.code : undefined;
}

/** Only the index and claim pages directly under claims/ are ever written. */
export function validSyncPath(path: unknown): path is string {
  if (!validVaultID(path) || !path.endsWith(".md")) return false;
  const parts = path.split("/");
  return (parts.length === 1 && path === "index.md") || (parts.length === 2 && parts[0] === "claims" && !parts[1]!.startsWith("."));
}

/**
 * Keeps a folder (normally inside an Obsidian vault) equal to the latest sync.
 * The folder must be new, empty, or one this command created: a non-empty
 * folder without the sync state is refused so a user's notes are never
 * overwritten. Writes never follow symbolic links, and only files listed in
 * the saved state are ever removed.
 */
export class FileSyncFolder implements SyncFolder {
  constructor(private readonly dir: string) {}

  private atomic(path: string, content: string): void {
    const tmp = `${path}.tmp-${process.pid}`;
    writeFileSync(tmp, content, { mode: 0o600, flag: "w" });
    try { renameSync(tmp, path); } catch (e) { try { unlinkSync(tmp); } catch { /* best effort */ } throw e; }
  }

  private realDirectories(path: string): boolean {
    let current = this.dir;
    for (const part of ["", ...path.split("/").slice(0, -1)]) {
      if (part) current = join(current, part);
      const stat = lstatSync(current);
      if (stat.isSymbolicLink() || !stat.isDirectory()) return false;
    }
    return true;
  }

  /** Returns the current content of a regular file, undefined when absent. */
  private existing(target: string): string | undefined {
    try {
      const stat = lstatSync(target);
      if (!stat.isFile()) throw Object.assign(new Error("not a regular file"), { code: "EUNSAFE" });
      return readFileSync(target, "utf8");
    } catch (e) {
      if (errno(e) === "ENOENT") return undefined;
      throw e;
    }
  }

  async exclusive<T>(fn: () => Promise<T>): Promise<Result<T, SyncFolderError>> {
    let existed: boolean;
    try {
      // The lock is created inside the folder, so it must not be a link.
      if (lstatSync(this.dir).isSymbolicLink()) return fail("path_unsafe");
      existed = true;
    } catch (e) {
      if (errno(e) !== "ENOENT") return fail("unreadable");
      existed = false;
    }
    try {
      const value = await withExclusiveLock(join(this.dir, SYNC_LOCK_FILE), fn, { staleMs: SYNC_LOCK_STALE_MS, retries: 1, retryMs: 50 });
      return { ok: true, value };
    } catch (e) {
      if (e instanceof LockBusyError) return { ok: false, error: { code: "busy" } };
      return fail("unreadable");
    } finally {
      // A first sync that failed leaves no folder behind, as before locking.
      if (!existed) {
        try { if (readdirSync(this.dir).length === 0) rmdirSync(this.dir); } catch { /* best effort */ }
      }
    }
  }

  async open(): Promise<Result<SyncState | undefined, VaultError>> {
    try {
      let entries: string[] | undefined;
      try {
        if (lstatSync(this.dir).isSymbolicLink()) return fail("path_unsafe");
        entries = readdirSync(this.dir);
      } catch (e) {
        if (errno(e) !== "ENOENT") return fail("unreadable");
      }
      if (entries?.includes(SYNC_STATE_FILE)) {
        const parsed: unknown = JSON.parse(this.existing(join(this.dir, SYNC_STATE_FILE)) ?? "null");
        if (!parsed || typeof parsed !== "object" || !("version" in parsed) || parsed.version !== "kb-vault-sync-1") return fail("unreadable");
        // SAFETY: the service revalidates scope and every listed path.
        return { ok: true, value: parsed as SyncState };
      }
      if (entries && entries.some((e) => e !== SYNC_LOCK_FILE)) return fail("output_exists");
      // Nothing is created until the export has been read, so a failed first
      // sync leaves no folder behind and can simply be retried.
      return { ok: true, value: undefined };
    } catch {
      return fail("unreadable");
    }
  }

  async occupied(path: string): Promise<Result<boolean, VaultError>> {
    if (!validSyncPath(path)) return fail("path_unsafe");
    try {
      lstatSync(join(this.dir, path));
      return { ok: true, value: true };
    } catch (e) {
      return errno(e) === "ENOENT" ? { ok: true, value: false } : fail("unreadable");
    }
  }

  async write(path: string, content: string): Promise<Result<WriteOutcome, VaultError>> {
    if (!validSyncPath(path)) return fail("path_unsafe");
    try {
      const target = join(this.dir, path);
      if (!isWithin(target, this.dir)) return fail("path_unsafe");
      mkdirSync(join(this.dir, "claims"), { recursive: true, mode: 0o700 });
      if (!this.realDirectories(path)) return fail("path_unsafe");
      if (this.existing(target) === content) return { ok: true, value: "unchanged" };
      this.atomic(target, content);
      return { ok: true, value: "written" };
    } catch (e) {
      return fail(errno(e) === "EUNSAFE" ? "path_unsafe" : "unreadable");
    }
  }

  async remove(path: string): Promise<Result<void, VaultError>> {
    if (!validSyncPath(path)) return fail("path_unsafe");
    try {
      const target = join(this.dir, path);
      if (!isWithin(target, this.dir) || !this.realDirectories(path)) return fail("path_unsafe");
      if (this.existing(target) !== undefined) unlinkSync(target);
      return { ok: true, value: undefined };
    } catch (e) {
      return fail(errno(e) === "EUNSAFE" ? "path_unsafe" : "unreadable");
    }
  }

  async save(state: SyncState): Promise<Result<void, VaultError>> {
    try {
      mkdirSync(this.dir, { recursive: true, mode: 0o700 });
      if (lstatSync(this.dir).isSymbolicLink()) return fail("path_unsafe");
      this.atomic(join(this.dir, SYNC_STATE_FILE), JSON.stringify(state, null, 2) + "\n");
      return { ok: true, value: undefined };
    } catch {
      return fail("unreadable");
    }
  }
}
