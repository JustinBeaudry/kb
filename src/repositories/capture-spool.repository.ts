import { cc, dlopen, FFIType, ptr } from "bun:ffi";
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
  return dlopen(
    process.platform === "darwin" ? "/usr/lib/libSystem.B.dylib" : "libc.so.6",
    {
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
    },
  );
}
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
    throw Error("unsafe file");
}
function name(s: string) {
  if (
    !/^(?:state|state-next|event-next|lock|dispatch|[a-f0-9]{64}\.json)$/.test(
      s,
    )
  )
    throw Error("name");
  return cstr(s);
}
export class SecureSpoolDirectory {
  constructor(readonly path: string) {}
  async within<T>(
    fn: (io: SpoolIO) => Promise<T>,
    lockName: "lock" | "dispatch" = "lock",
  ): Promise<Result<T, CaptureError>> {
    let fd = -1,
      lock = -1;
    try {
      const n = api();
      const absolute = resolve(this.path);
      const parent = await realpath(dirname(absolute));
      // Walk the canonical parent with no-follow directory opens, retaining each
      // anchor until the next is opened. The final spool component is never resolved.
      fd = check(
        n.openat(
          -2,
          ptr(cstr("/")),
          constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW,
          0,
        ),
      );
      // AT_FDCWD differs by OS, but is ignored for the absolute initial open.
      for (const part of parent.split("/").filter(Boolean)) {
        const next = check(
          n.openat(
            fd,
            ptr(cstr(part)),
            constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW,
            0,
          ),
        );
        n.close(fd);
        fd = next;
      }
      const leaf = cstr(basename(absolute));
      n.mkdirat(fd, ptr(leaf), 0o700);
      check(n.fsync(fd));
      const child = check(
        n.openat(
          fd,
          ptr(leaf),
          constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW,
          0,
        ),
      );
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
          ptr(name(lockName)),
          constants.O_RDWR | constants.O_CREAT | constants.O_NOFOLLOW,
          0o600,
        ),
      );
      privateFile(await info(lock));
      if (n.flock(lock, 2 | 4) !== 0)
        return { ok: false, error: { code: "busy" } };
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
      ptr(name(file)),
      constants.O_RDONLY | constants.O_NOFOLLOW,
      0,
    );
    if (opened < 0) {
      // Missing files are distinguished safely using openat(O_CREAT|O_EXCL): a
      // symlink or inaccessible existing entry fails closed rather than appearing absent.
      const probe = n.openat(
        this.fd,
        ptr(name(file)),
        constants.O_RDWR |
          constants.O_CREAT |
          constants.O_EXCL |
          constants.O_NOFOLLOW,
        0o600,
      );
      if (probe < 0) throw Error("unsafe or unavailable");
      n.close(probe);
      check(n.unlinkat(this.fd, ptr(name(file)), 0));
      return null;
    }
    try {
      const s = await info(opened);
      privateFile(s);
      if (s.size > max) throw Error("bounded");
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
        ptr(name(temp)),
        constants.O_RDWR | constants.O_CREAT | constants.O_NOFOLLOW,
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
    check(n.renameat(this.fd, ptr(name(temp)), this.fd, ptr(name(file))));
    check(n.fsync(this.fd));
  }
  remove(file: string): void {
    check(api().unlinkat(this.fd, ptr(name(file)), 0));
    check(api().fsync(this.fd));
  }
}
