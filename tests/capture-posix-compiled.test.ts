import { expect, test } from "bun:test";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

// Regression: release binaries are built with `bun build --compile`, where the
// capture opener's C source has no on-disk path. The opener must still compile
// inside a compiled executable. kb_openat is plain POSIX, so this runs on every
// platform even though the rest of the native spool is darwin/arm64 only.
test("capture opener compiles inside a compiled kb binary", async () => {
  const dir = await mkdtemp(join(tmpdir(), "kb-compiled-opener-"));
  try {
    const repo = resolve(import.meta.dir, "../src/repositories/capture-spool.repository.ts");
    const entry = join(dir, "entry.ts");
    await writeFile(
      entry,
      `import { compileOpener } from ${JSON.stringify(repo)};\n` +
        `const fd = compileOpener().symbols.kb_openat(-100, Buffer.from("/\\0"), 0, 0);\n` +
        `process.exit(fd >= 0 ? 0 : 3);\n`,
    );
    const out = join(dir, "opener");
    const build = Bun.spawnSync([process.execPath, "build", "--compile", entry, "--outfile", out], {
      stdout: "pipe",
      stderr: "pipe",
    });
    expect(build.exitCode).toBe(0);
    const run = Bun.spawnSync([out], { cwd: tmpdir(), stdout: "pipe", stderr: "pipe" });
    expect(run.stderr.toString()).toBe("");
    expect(run.exitCode).toBe(0);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}, 60_000);
