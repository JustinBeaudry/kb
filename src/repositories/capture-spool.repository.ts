import { cc, dlopen, FFIType } from "bun:ffi";
import { constants, fstat, type Stats } from "node:fs";
import { realpath } from "node:fs/promises";
import { basename, dirname, resolve } from "node:path";
import type { CaptureError, Result } from "../capture/types";

/** POSIX directory-relative I/O. Never fall back to path-based payload writes.
 * The private directory is owner-only; fd anchoring survives rename/substitution.
 */
function native() {
  if (process.platform !== "darwin" || process.arch !== "arm64")
    throw Error("unsupported: unqualified native platform");
  return dlopen("/usr/lib/libSystem.B.dylib", {
    mkdirat: {
      args: [FFIType.i32, FFIType.ptr, FFIType.i32],
      returns: FFIType.i32,
    },
    renameat: {
      args: [FFIType.i32, FFIType.ptr, FFIType.i32, FFIType.ptr],
      returns: FFIType.i32,
    },
    unlinkat: {
      args: [FFIType.i32, FFIType.ptr, FFIType.i32],
      returns: FFIType.i32,
    },
    flock: { args: [FFIType.i32, FFIType.i32], returns: FFIType.i32 },
    close: { args: [FFIType.i32], returns: FFIType.i32 },
    fsync: { args: [FFIType.i32], returns: FFIType.i32 },
    ftruncate: { args: [FFIType.i32, FFIType.i64], returns: FFIType.i32 },
  });
}
/** Darwin value; node:fs does not export O_CLOEXEC. Only darwin/arm64 is enabled. */
const O_CLOEXEC = 0x01000000;
const DIRECTORY =
  constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW | O_CLOEXEC;
/** Payload/state/lock files: an unsafe entry (symlink, wrong owner or mode,
 * hard link) is tampering, not corruption. It fails closed and is never
 * quarantined automatically. */
export class UnsafeStorage extends Error {}
/** A file larger than the caller's bound: its content is invalid, not unsafe. */
export class BoundedRead extends Error {}
let library: ReturnType<typeof native> | undefined;
let opener: ReturnType<typeof compileOpener> | undefined;
function compileOpener() {
  return cc({
    source: new URL("./capture-posix.c", import.meta.url),
    symbols: {
      kb_openat: {
        args: [FFIType.i32, FFIType.ptr, FFIType.i32, FFIType.i32],
        returns: FFIType.i32,
      },
    },
  });
}
const api = () => ({
  ...(library ??= native()).symbols,
  openat: (opener ??= compileOpener()).symbols.kb_openat,
});
const cstr = (s: string) => Buffer.from(s + "\0");
const check = (n: number) => {
  if (n < 0) throw Error("storage");
  return n;
};
const info = (fd: number) =>
  new Promise<Stats>((resolve, reject) =>
    fstat(fd, (e, s) => (e ? reject(e) : resolve(s))),
  );
function privateFile(s: Stats) {
  if (
    !s.isFile() ||
    s.uid !== process.getuid?.() ||
    (s.mode & 0o7777) !== 0o600 ||
    s.nlink !== 1
  )
    throw new UnsafeStorage("unsafe file");
}
function name(s: string) {
  if (
    !/^(?:state|state-next|event-next|policy|policy-next|lock|dispatch|[a-f0-9]{64}\.json)$/.test(
      s,
    )
  )
    throw Error("name");
  return cstr(s);
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
export class SecureSpoolDirectory {
  /** The state lock guards short critical sections, so callers wait for it up
   * to lockWaitMs. The dispatch lease is held across network requests and is
   * never waited for. */
  constructor(
    readonly path: string,
    private readonly lockWaitMs = 2000,
  ) {}
  async within<T>(
    fn: (io: SpoolIO) => Promise<T>,
    lockName: "lock" | "dispatch" = "lock",
    /** Optional absolute (Date.now) deadline that can only shorten the wait. */
    deadlineAt?: number,
  ): Promise<Result<T, CaptureError>> {
    let fd = -1,
      lock = -1;
    try {
      const n = api();
      const absolute = resolve(this.path);
      const parent = await realpath(dirname(absolute));
      // Walk the canonical parent with no-follow directory opens, retaining each
      // anchor until the next is opened. The final spool component is never resolved.
      fd = check(n.openat(-2, cstr("/"), DIRECTORY, 0));
      // AT_FDCWD differs by OS, but is ignored for the absolute initial open.
      for (const part of parent.split("/").filter(Boolean)) {
        const next = check(n.openat(fd, cstr(part), DIRECTORY, 0));
        n.close(fd);
        fd = next;
      }
      const leaf = cstr(basename(absolute));
      n.mkdirat(fd, leaf, 0o700);
      check(n.fsync(fd));
      const child = check(n.openat(fd, leaf, DIRECTORY, 0));
      n.close(fd);
      fd = child;
      const s = await info(fd);
      if (
        !s.isDirectory() ||
        s.uid !== process.getuid?.() ||
        (s.mode & 0o7777) !== 0o700
      )
        throw Error("unsafe directory");
      lock = check(
        n.openat(
          fd,
          name(lockName),
          constants.O_RDWR |
            constants.O_CREAT |
            constants.O_NOFOLLOW |
            O_CLOEXEC,
          0o600,
        ),
      );
      privateFile(await info(lock));
      const deadline = Math.min(
        Date.now() + (lockName === "lock" ? this.lockWaitMs : 0),
        deadlineAt ?? Number.POSITIVE_INFINITY,
      );
      // LOCK_EX | LOCK_NB, polled: a blocking flock would stall the JS thread.
      while (n.flock(lock, 2 | 4) !== 0) {
        if (Date.now() >= deadline)
          return { ok: false, error: { code: "busy" } };
        await sleep(5);
      }
      return { ok: true, value: await fn(new SpoolIO(fd)) };
    } catch {
      return { ok: false, error: { code: "storage_unavailable" } };
    } finally {
      if (lock >= 0) api().close(lock);
      if (fd >= 0) api().close(fd);
    }
  }
}
export class SpoolIO {
  constructor(private readonly fd: number) {}
  async read(file: string, max: number): Promise<string | null> {
    const n = api();
    const opened = n.openat(
      this.fd,
      name(file),
      constants.O_RDONLY | constants.O_NOFOLLOW | O_CLOEXEC,
      0,
    );
    if (opened < 0) {
      // Missing files are distinguished safely using openat(O_CREAT|O_EXCL): a
      // symlink or inaccessible existing entry fails closed rather than appearing absent.
      const probe = n.openat(
        this.fd,
        name(file),
        constants.O_RDWR |
          constants.O_CREAT |
          constants.O_EXCL |
          constants.O_NOFOLLOW |
          O_CLOEXEC,
        0o600,
      );
      if (probe < 0) throw new UnsafeStorage("unsafe or unavailable");
      n.close(probe);
      check(n.unlinkat(this.fd, name(file), 0));
      return null;
    }
    try {
      const s = await info(opened);
      privateFile(s);
      if (s.size > max) throw new BoundedRead("bounded");
      return await Bun.file(opened).text();
    } finally {
      n.close(opened);
    }
  }
  async write(file: string, data: string, temp = "state-next"): Promise<void> {
    const n = api();
    const opened = check(
      n.openat(
        this.fd,
        name(temp),
        constants.O_RDWR | constants.O_CREAT | constants.O_NOFOLLOW | O_CLOEXEC,
        0o600,
      ),
    );
    try {
      privateFile(await info(opened));
      check(n.ftruncate(opened, 0));
      await Bun.write(Bun.file(opened), data);
      check(n.fsync(opened));
    } finally {
      n.close(opened);
    }
    // Both names stay referenced for the duration of the native call.
    const from = name(temp),
      to = name(file);
    check(n.renameat(this.fd, from, this.fd, to));
    check(n.fsync(this.fd));
  }
  /** Remove a file if present without reading its contents. */
  async removeIfPresent(file: string): Promise<boolean> {
    const n = api();
    const opened = n.openat(
      this.fd,
      name(file),
      constants.O_RDONLY | constants.O_NOFOLLOW | O_CLOEXEC,
      0,
    );
    if (opened < 0) return (await this.read(file, 0)) !== null;
    try {
      privateFile(await info(opened));
    } finally {
      n.close(opened);
    }
    this.remove(file);
    return true;
  }
  remove(file: string): void {
    check(api().unlinkat(this.fd, name(file), 0));
    check(api().fsync(this.fd));
  }
}
