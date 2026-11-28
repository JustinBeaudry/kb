import { randomBytes } from "node:crypto";
import { closeSync, lstatSync, mkdirSync, openSync, readdirSync, readFileSync, renameSync, rmdirSync, unlinkSync, writeFileSync, writeSync } from "node:fs";
import { hostname } from "node:os";
import { join } from "node:path";
import type { Result } from "../capture/types";
import { isWithin } from "../lib/path-safety";
import { validVaultID, type VaultError } from "./vault.repository";

export const SYNC_STATE_FILE = ".kb-vault-sync.json";
/** Held while a sync runs, so a scheduled run and a manual one never interleave. */
export const SYNC_LOCK_FILE = ".kb-vault-sync.lock";
/** A running sync renews its lock this often, however long its export takes. */
export const SYNC_LOCK_RENEW_MS = 60 * 1000;
/**
 * A lock not renewed for this long belongs to a run that stopped. A lock
 * taken on this machine by a process that has exited is reclaimed at once.
 */
export const SYNC_LOCK_STALE_MS = 10 * 60 * 1000;

type SyncLock = { pid: number; host: string; token: string; renewedAt: string };

function readLock(path: string): SyncLock | undefined {
  try {
    const parsed: unknown = JSON.parse(readFileSync(path, "utf8"));
    if (!parsed || typeof parsed !== "object") return undefined;
    const lock = parsed as Partial<SyncLock>;
    return typeof lock.pid === "number" && typeof lock.host === "string" && typeof lock.token === "string" && typeof lock.renewedAt === "string" ? (lock as SyncLock) : undefined;
  } catch {
    return undefined;
  }
}

function processExited(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return false;
  } catch (e) {
    return errno(e) === "ESRCH";
  }
}

/**
 * Whether a lock was left by a run that stopped. The folder may sit in a vault
 * synced between machines, so a lock from another host is judged only by when
 * it was last renewed.
 */
function abandoned(lock: SyncLock | undefined, now: number): boolean {
  if (!lock) return true;
  if (lock.host === hostname() && processExited(lock.pid)) return true;
  const renewed = Date.parse(lock.renewedAt);
  return !Number.isFinite(renewed) || now - renewed > SYNC_LOCK_STALE_MS;
}

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
  constructor(private readonly dir: string, private readonly renewMs = SYNC_LOCK_RENEW_MS) {}

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
    const path = join(this.dir, SYNC_LOCK_FILE);
    const mine: SyncLock = { pid: process.pid, host: hostname(), token: randomBytes(16).toString("hex"), renewedAt: new Date().toISOString() };
    let renewal: ReturnType<typeof setInterval> | undefined;
    try {
      const taken = this.takeLock(path, mine);
      if (taken !== "taken") return taken === "busy" ? { ok: false, error: { code: "busy" } } : fail("unreadable");
      // Renew the lock while the export is read, so a slow export is never
      // mistaken for a stopped one.
      renewal = setInterval(() => {
        if (readLock(path)?.token !== mine.token) return;
        try { this.atomic(path, JSON.stringify({ ...mine, renewedAt: new Date().toISOString() })); } catch { /* the next renewal retries */ }
      }, this.renewMs);
      renewal.unref?.();
      return { ok: true, value: await fn() };
    } catch {
      return fail("unreadable");
    } finally {
      if (renewal) clearInterval(renewal);
      // Release only our own lock, never one a later run took over.
      if (readLock(path)?.token === mine.token) {
        try { unlinkSync(path); } catch { /* best effort */ }
      }
      // A first sync that failed leaves no folder behind, as before locking.
      if (!existed) {
        try { if (readdirSync(this.dir).length === 0) rmdirSync(this.dir); } catch { /* best effort */ }
      }
    }
  }

  private takeLock(path: string, mine: SyncLock): "taken" | "busy" | "failed" {
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        mkdirSync(this.dir, { recursive: true, mode: 0o700 });
        const fd = openSync(path, "wx", 0o600);
        try { writeSync(fd, JSON.stringify(mine)); } finally { closeSync(fd); }
        return "taken";
      } catch (e) {
        if (errno(e) !== "EEXIST") return "failed";
      }
      if (attempt > 0 || !abandoned(readLock(path), Date.now())) return "busy";
      try { unlinkSync(path); } catch (e) { if (errno(e) !== "ENOENT") return "failed"; }
    }
    return "busy";
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
