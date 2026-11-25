import { closeSync, constants, fstatSync, lstatSync, mkdirSync, openSync, readdirSync, readFileSync, readSync, realpathSync, renameSync, unlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { Result } from "../capture/types";
import { isWithin } from "../lib/path-safety";

export type VaultEntryKind = "curated" | "raw" | "session";
/** Inventory metadata only: no entry content is ever read to build it. */
export type VaultEntry = { id: string; kind: VaultEntryKind; bytes: number; unsafe?: true };
export type VaultError = { code: "path_unsafe" | "not_found" | "too_large" | "unreadable" | "output_exists" };

export const MAX_VAULT_FILE_BYTES = 8 * 1024 * 1024;
const MAX_INVENTORY_ENTRIES = 10_000;
const MAX_DEPTH = 16;

export interface VaultRepository {
  readonly root: string;
  inventory(): Promise<Result<VaultEntry[], VaultError>>;
  read(id: string): Promise<Result<Uint8Array, VaultError>>;
}

const fail = (code: VaultError["code"]): Result<never, VaultError> => ({ ok: false, error: { code } });

export function classifyVaultEntry(id: string): VaultEntryKind {
  if (id.startsWith("raw/")) return "raw";
  if (id.startsWith("sessions/")) return "session";
  return "curated";
}

/** A vault file ID is a normalized, relative, slash-separated path. */
export function validVaultID(id: unknown): id is string {
  if (typeof id !== "string" || id.length === 0 || Buffer.byteLength(id) > 1024 || id.includes("\0") || id.includes("\\") || id.startsWith("/")) return false;
  return id.split("/").every((s) => s.length > 0 && s !== "." && s !== "..");
}

function errno(e: unknown): string | undefined {
  return e && typeof e === "object" && "code" in e && typeof e.code === "string" ? e.code : undefined;
}

/**
 * Read-only access to a local Markdown vault. Never writes to the vault, never
 * follows a symlink in any path component, and never reads content during
 * inventory.
 */
export class FileVaultRepository implements VaultRepository {
  private constructor(readonly root: string) {}

  static open(path: string): Result<FileVaultRepository, VaultError> {
    try {
      const root = realpathSync(path);
      if (!lstatSync(root).isDirectory()) return fail("not_found");
      return { ok: true, value: new FileVaultRepository(root) };
    } catch (e) {
      return fail(errno(e) === "ENOENT" ? "not_found" : "unreadable");
    }
  }

  async inventory(): Promise<Result<VaultEntry[], VaultError>> {
    const out: VaultEntry[] = [];
    const walk = (dir: string, prefix: string, depth: number): VaultError["code"] | undefined => {
      if (depth > MAX_DEPTH) return "too_large";
      for (const name of readdirSync(dir).sort()) {
        if (name.startsWith(".")) continue; // .kb state, .obsidian, .git: never migrated
        const id = prefix ? `${prefix}/${name}` : name;
        if (!validVaultID(id)) continue;
        const stat = lstatSync(join(dir, name));
        if (out.length >= MAX_INVENTORY_ENTRIES) return "too_large";
        if (stat.isSymbolicLink()) out.push({ id, kind: classifyVaultEntry(id), bytes: 0, unsafe: true });
        else if (stat.isDirectory()) {
          const nested = walk(join(dir, name), id, depth + 1);
          if (nested) return nested;
        } else if (stat.isFile()) out.push({ id, kind: classifyVaultEntry(id), bytes: stat.size });
        else out.push({ id, kind: classifyVaultEntry(id), bytes: 0, unsafe: true });
      }
      return undefined;
    };
    try {
      const failed = walk(this.root, "", 0);
      return failed ? fail(failed) : { ok: true, value: out };
    } catch {
      return fail("unreadable");
    }
  }

  async read(id: string): Promise<Result<Uint8Array, VaultError>> {
    if (!validVaultID(id)) return fail("path_unsafe");
    const segments = id.split("/");
    let current = this.root;
    let fd: number | undefined;
    try {
      for (const [i, segment] of segments.entries()) {
        current = join(current, segment);
        const stat = lstatSync(current);
        if (stat.isSymbolicLink()) return fail("path_unsafe");
        if (i < segments.length - 1 ? !stat.isDirectory() : !stat.isFile()) return fail("path_unsafe");
      }
      if (!isWithin(realpathSync(current), this.root)) return fail("path_unsafe");
      const expected = lstatSync(current);
      if (expected.size > MAX_VAULT_FILE_BYTES) return fail("too_large");
      fd = openSync(current, constants.O_RDONLY | constants.O_NOFOLLOW);
      const opened = fstatSync(fd);
      if (opened.ino !== expected.ino || opened.dev !== expected.dev || !opened.isFile()) return fail("path_unsafe");
      const buffer = Buffer.alloc(Math.min(opened.size, MAX_VAULT_FILE_BYTES) + 1);
      let total = 0;
      for (;;) {
        const n = readSync(fd, buffer, total, buffer.length - total, null);
        if (n === 0) break;
        total += n;
        if (total > MAX_VAULT_FILE_BYTES) return fail("too_large");
        if (total === buffer.length) return fail("unreadable"); // changed while reading
      }
      return { ok: true, value: new Uint8Array(buffer.subarray(0, total)) };
    } catch (e) {
      const code = errno(e);
      if (code === "ENOENT" || code === "ENOTDIR") return fail("not_found");
      if (code === "ELOOP") return fail("path_unsafe");
      return fail("unreadable");
    } finally {
      if (fd !== undefined) closeSync(fd);
    }
  }
}

/** Resumable export bundle state, stored beside the bundle (dot-file, so a
 * later import inventory never lists it as content). */
export type BundleState = {
  version: "kb-export-1";
  scope: { project_id: string; task_id?: string; session_id?: string };
  continuation?: string;
  claims: number;
  omitted: number;
  completeness: "complete" | "partial" | "withheld";
  done: boolean;
};
export interface BundleWriter {
  begin(resume: boolean): Promise<Result<BundleState | undefined, VaultError>>;
  write(id: string, content: string): Promise<Result<void, VaultError>>;
  save(state: BundleState): Promise<Result<void, VaultError>>;
}

const STATE_FILE = ".kb-export.json";

/** Writes a portable export into a new directory, never into an existing vault. */
export class FileBundleWriter implements BundleWriter {
  constructor(private readonly dir: string) {}

  private atomic(path: string, content: string): void {
    const tmp = `${path}.tmp-${process.pid}`;
    writeFileSync(tmp, content, { mode: 0o600, flag: "w" });
    try { renameSync(tmp, path); } catch (e) { try { unlinkSync(tmp); } catch { /* best effort */ } throw e; }
  }

  async begin(resume: boolean): Promise<Result<BundleState | undefined, VaultError>> {
    try {
      let entries: string[] | undefined;
      try {
        if (lstatSync(this.dir).isSymbolicLink()) return fail("path_unsafe");
        entries = readdirSync(this.dir);
      } catch (e) {
        if (errno(e) !== "ENOENT") return fail("unreadable");
      }
      if (resume) {
        if (!entries) return fail("not_found");
        const parsed: unknown = JSON.parse(readFileSync(join(this.dir, STATE_FILE), "utf8"));
        if (!parsed || typeof parsed !== "object" || !("version" in parsed) || parsed.version !== "kb-export-1") return fail("unreadable");
        // SAFETY: the service revalidates every state field before resuming.
        return { ok: true, value: parsed as BundleState };
      }
      if (entries && entries.length > 0) return fail("output_exists");
      mkdirSync(join(this.dir, "claims"), { recursive: true, mode: 0o700 });
      return { ok: true, value: undefined };
    } catch {
      return fail("unreadable");
    }
  }

  async write(id: string, content: string): Promise<Result<void, VaultError>> {
    if (!validVaultID(id) || id.startsWith(".")) return fail("path_unsafe");
    try {
      const target = join(this.dir, id);
      if (!isWithin(target, this.dir)) return fail("path_unsafe");
      this.atomic(target, content);
      return { ok: true, value: undefined };
    } catch {
      return fail("unreadable");
    }
  }

  async save(state: BundleState): Promise<Result<void, VaultError>> {
    try {
      this.atomic(join(this.dir, STATE_FILE), JSON.stringify(state, null, 2) + "\n");
      return { ok: true, value: undefined };
    } catch {
      return fail("unreadable");
    }
  }
}
