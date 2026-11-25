import { createHash } from "node:crypto";
import { constants } from "node:fs";
import { chmod, lstat, mkdir, open, readFile, rename, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import type { CaptureError, Result } from "./types";

/**
 * Byte cursors for explicitly registered transcript sources. The watcher reads
 * only complete newline-terminated records after the cursor; generation
 * changes (replacement, truncation, rewritten head) are detected so a rotated
 * source is reported as a gap instead of being replayed or invented.
 */
export const CURSOR_VERSION = 1;
/** Prefix hashed to detect an in-place rewrite of an already-read generation. */
export const HEAD_BYTES = 256;
/** Upper bound for one record and for one poll window per source. */
export const MAX_LINE_BYTES = 8 * 1024 * 1024;
/** Recently captured event IDs kept per source to suppress repeated delivery. */
export const RECENT_IDS = 2048;

export type CursorState = {
  version: typeof CURSOR_VERSION;
  fileIdentity: string;
  offset: number;
  headBytes: number;
  head: string;
  sequence: number;
  recent: string[];
  /** Discarding the remainder of an oversized record until its newline. */
  skipping: boolean;
};

export type FileStat = { identity: string; size: number; isFile: boolean };

/** Filesystem boundary for registered sources. Never lists directories. */
export interface SourceReader {
  stat(path: string): Promise<Result<FileStat, CaptureError>>;
  read(path: string, start: number, length: number): Promise<Result<Uint8Array, CaptureError>>;
}

/** Persistence boundary for cursors. `corrupt` means the stored state is unusable. */
export interface CursorStore {
  load(key: string): Promise<Result<CursorState | undefined | "corrupt", CaptureError>>;
  save(key: string, state: CursorState): Promise<Result<void, CaptureError>>;
}

const fail = (code: CaptureError["code"]): Result<never, CaptureError> => ({ ok: false, error: { code } });

export const digest = (bytes: Uint8Array): string => createHash("sha256").update(bytes).digest("hex");

export type Line = { text: string; start: number; end: number };

/** Splits a window read at `base` into complete lines; the remainder is pending. */
export function completeLines(bytes: Uint8Array, base: number): { lines: Line[]; consumed: number } {
  const lines: Line[] = [];
  const decoder = new TextDecoder();
  let from = 0;
  for (let i = bytes.indexOf(10); i !== -1; i = bytes.indexOf(10, from)) {
    lines.push({ text: decoder.decode(bytes.subarray(from, i)), start: base + from, end: base + i + 1 });
    from = i + 1;
  }
  return { lines, consumed: from };
}

export function isCursorState(value: unknown): value is CursorState {
  if (typeof value !== "object" || value === null) return false;
  const v: Partial<Record<keyof CursorState, unknown>> = value;
  const count = (n: unknown) => typeof n === "number" && Number.isSafeInteger(n) && n >= 0;
  return (
    v.version === CURSOR_VERSION &&
    typeof v.fileIdentity === "string" &&
    count(v.offset) &&
    count(v.headBytes) &&
    typeof v.head === "string" &&
    count(v.sequence) &&
    Array.isArray(v.recent) &&
    v.recent.length <= RECENT_IDS &&
    v.recent.every((id) => typeof id === "string") &&
    typeof v.skipping === "boolean"
  );
}

/** Reads a registered regular file; symlinks are refused so a registration
 * cannot be redirected to an unselected file. */
export class FsSourceReader implements SourceReader {
  async stat(path: string): Promise<Result<FileStat, CaptureError>> {
    try {
      const s = await lstat(path);
      return { ok: true, value: { identity: `${s.dev}:${s.ino}`, size: s.size, isFile: s.isFile() } };
    } catch {
      return fail("unavailable");
    }
  }
  async read(path: string, start: number, length: number): Promise<Result<Uint8Array, CaptureError>> {
    let handle: Awaited<ReturnType<typeof open>> | undefined;
    try {
      handle = await open(path, constants.O_RDONLY | constants.O_NOFOLLOW);
      const buffer = new Uint8Array(length);
      const { bytesRead } = await handle.read(buffer, 0, length, start);
      return { ok: true, value: buffer.subarray(0, bytesRead) };
    } catch {
      return fail("unavailable");
    } finally {
      await handle?.close().catch(() => undefined);
    }
  }
}

/** Private cursor files (directory 0700, files 0600), written atomically. */
export class FileCursorStore implements CursorStore {
  constructor(private readonly dir: string) {}
  private path(key: string): string {
    return join(this.dir, `${digest(new TextEncoder().encode(key))}.json`);
  }
  async load(key: string): Promise<Result<CursorState | undefined | "corrupt", CaptureError>> {
    let text: string;
    try {
      text = await readFile(this.path(key), "utf8");
    } catch (error) {
      return error instanceof Error && "code" in error && error.code === "ENOENT"
        ? { ok: true, value: undefined }
        : fail("storage_unavailable");
    }
    try {
      const parsed: unknown = JSON.parse(text);
      return { ok: true, value: isCursorState(parsed) ? parsed : "corrupt" };
    } catch {
      return { ok: true, value: "corrupt" };
    }
  }
  async save(key: string, state: CursorState): Promise<Result<void, CaptureError>> {
    const target = this.path(key);
    const tmp = `${target}.${process.pid}.${Date.now()}.tmp`;
    try {
      await mkdir(this.dir, { recursive: true, mode: 0o700 });
      await chmod(this.dir, 0o700);
      await writeFile(tmp, JSON.stringify(state), { mode: 0o600, flag: "wx" });
      await rename(tmp, target);
      return { ok: true, value: undefined };
    } catch {
      await rm(tmp, { force: true }).catch(() => undefined);
      return fail("storage_unavailable");
    }
  }
}
