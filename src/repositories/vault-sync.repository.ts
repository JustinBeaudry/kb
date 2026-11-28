import { randomBytes } from "node:crypto";
import { linkSync, lstatSync, mkdirSync, readdirSync, readFileSync, renameSync, rmdirSync, statSync, unlinkSync, writeFileSync } from "node:fs";
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
type LockRead =
  | { state: "absent" }
  | { state: "held"; lock: SyncLock; raw: string }
  | { state: "malformed"; raw: string; ageMs: number }
  | { state: "unreadable" };

/** Reads the lock. A read that fails for any reason but absence fails closed. */
function readLock(path: string): LockRead {
  let raw: string;
  let modified: number;
  try {
    raw = readFileSync(path, "utf8");
    modified = statSync(path).mtimeMs;
  } catch (e) {
    return errno(e) === "ENOENT" ? { state: "absent" } : { state: "unreadable" };
  }
  try {
    const lock = JSON.parse(raw) as Partial<SyncLock> | null;
    if (lock && typeof lock.pid === "number" && typeof lock.host === "string" && typeof lock.token === "string" && typeof lock.renewedAt === "string") return { state: "held", lock: lock as SyncLock, raw };
  } catch { /* malformed below */ }
  return { state: "malformed", raw, ageMs: Date.now() - modified };
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
 * it was last renewed. A lock that cannot be read is never judged abandoned.
 */
function abandoned(read: LockRead, now: number): boolean {
  if (read.state === "malformed") return read.ageMs > SYNC_LOCK_STALE_MS;
  if (read.state !== "held") return false;
  if (read.lock.host === hostname() && processExited(read.lock.pid)) return true;
  const renewed = Date.parse(read.lock.renewedAt);
  return !Number.isFinite(renewed) || now - renewed > SYNC_LOCK_STALE_MS;
}

function unlinkQuietly(path: string): void {
  try { unlinkSync(path); } catch { /* best effort */ }
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
  /** The token of the lock this folder's current sync holds. */
  private held: string | undefined;

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
    const path = this.lockPath();
    const mine: SyncLock = { pid: process.pid, host: hostname(), token: randomBytes(16).toString("hex"), renewedAt: new Date().toISOString() };
    let renewal: ReturnType<typeof setInterval> | undefined;
    try {
      const taken = this.takeLock(path, mine);
      if (taken !== "taken") return taken === "busy" ? { ok: false, error: { code: "busy" } } : fail("unreadable");
      this.held = mine.token;
      // Renew the lock while the export is read, so a slow export is never
      // mistaken for a stopped one. A run that finds its lock gone stops
      // writing: every write checks the lock first.
      renewal = setInterval(() => {
        if (!this.stillHeld()) return;
        const next = `${path}.${mine.token}.renew`;
        try {
          writeFileSync(next, JSON.stringify({ ...mine, renewedAt: new Date().toISOString() }), { mode: 0o600 });
          renameSync(next, path);
        } catch {
          unlinkQuietly(next); // the next renewal retries
        }
      }, this.renewMs);
      renewal.unref?.();
      return { ok: true, value: await fn() };
    } catch {
      return fail("unreadable");
    } finally {
      if (renewal) clearInterval(renewal);
      // Release only our own lock, never one a later run took over.
      if (this.held === mine.token && this.stillHeld()) unlinkQuietly(path);
      this.held = undefined;
      // A first sync that failed leaves no folder behind, as before locking.
      if (!existed) {
        try { if (readdirSync(this.dir).length === 0) rmdirSync(this.dir); } catch { /* best effort */ }
      }
    }
  }

  private lockPath(): string {
    return join(this.dir, SYNC_LOCK_FILE);
  }

  /** Whether this folder's current sync, if any, still holds the lock. */
  private stillHeld(): boolean {
    if (this.held === undefined) return true;
    const read = readLock(this.lockPath());
    return read.state === "held" && read.lock.token === this.held;
  }

  /**
   * Creates the lock with its whole content in one step (a hard link of a
   * finished file), so no other run ever sees it half-written. An abandoned
   * lock is moved aside before it is removed, and removed only when what was
   * moved is the very lock judged abandoned.
   */
  private takeLock(path: string, mine: SyncLock): "taken" | "busy" | "failed" {
    const draft = `${path}.${mine.token}`;
    try {
      mkdirSync(this.dir, { recursive: true, mode: 0o700 });
      writeFileSync(draft, JSON.stringify(mine), { mode: 0o600, flag: "wx" });
    } catch {
      return "failed";
    }
    try {
      for (let attempt = 0; attempt < 3; attempt++) {
        try {
          linkSync(draft, path);
          return "taken";
        } catch (e) {
          if (errno(e) !== "EEXIST") return "failed";
        }
        const seen = readLock(path);
        if (seen.state === "absent") continue;
        if (attempt === 2 || !abandoned(seen, Date.now()) || (seen.state !== "held" && seen.state !== "malformed")) return "busy";
        const moved = `${path}.${mine.token}.reclaim`;
        try {
          renameSync(path, moved);
        } catch (e) {
          if (errno(e) === "ENOENT") continue;
          return "failed";
        }
        let got: string | undefined;
        try { got = readFileSync(moved, "utf8"); } catch { /* compared below */ }
        if (got !== seen.raw) {
          // Another run's lock replaced the abandoned one first: put it back.
          try { linkSync(moved, path); } catch { /* a newer lock is in place */ }
          unlinkQuietly(moved);
          return "busy";
        }
        unlinkQuietly(moved);
      }
      return "busy";
    } finally {
      unlinkQuietly(draft);
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
      if (entries && entries.some((e) => !e.startsWith(SYNC_LOCK_FILE))) return fail("output_exists");
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
    if (!this.stillHeld()) return fail("unreadable");
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
    if (!this.stillHeld()) return fail("unreadable");
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
    if (!this.stillHeld()) return fail("unreadable");
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
