import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { chmodSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { parseQmdOutput, qmdSearchHints } from "../src/lib/qmd";

const dirs: string[] = [];
afterEach(() => {
  for (const d of dirs.splice(0)) {
    try {
      rmSync(d, { recursive: true, force: true });
    } catch {}
  }
});

function installFakeQmd(script: string): string {
  const dir = join(tmpdir(), `kb-fake-qmd-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(dir, { recursive: true });
  const bin = join(dir, "qmd");
  writeFileSync(bin, `#!/bin/sh\n${script}\n`);
  chmodSync(bin, 0o755);
  dirs.push(dir);
  return dir;
}

// Bun.spawn's binary lookup does not reliably honor in-process PATH
// mutations, so hints are exercised in a child process whose PATH is fully
// controlled (shim dir + system bins only — never the host's real qmd).
// The child deliberately does NOT call process.exit: every test here also
// proves the CLI process exits naturally after qmdSearchHints returns, even
// when a timed-out qmd (or its orphaned children) holds the stdout pipe.
async function runHints(pathDirs: string): Promise<string[] | null> {
  const proc = Bun.spawn(
    [process.execPath, "-e", 'const { qmdSearchHints } = await import("./src/lib/qmd"); process.stdout.write(JSON.stringify(await qmdSearchHints("query")));'],
    {
      stdout: "pipe",
      stderr: "pipe",
      env: { ...process.env, PATH: `${pathDirs}:/usr/bin:/bin` },
    }
  );
  const stdout = await new Response(proc.stdout).text();
  const exitCode = await proc.exited;
  if (exitCode !== 0) throw new Error(`runHints child exited ${exitCode}: ${stdout}`);
  return JSON.parse(stdout);
}

describe("parseQmdOutput", () => {
  it("extracts wiki-prefixed page IDs", () => {
    expect(parseQmdOutput("0.92 wiki/foo.md some snippet\n", 5)).toEqual(["wiki/foo.md"]);
  });

  it("injects the wiki/ prefix for bare tokens and strips ./", () => {
    expect(parseQmdOutput("foo.md\n./bar.md\n", 5)).toEqual(["wiki/foo.md", "wiki/bar.md"]);
  });

  it("slices wiki-relative IDs out of longer path tokens", () => {
    expect(parseQmdOutput("0.91 collections/wiki/bar.md snippet\n", 5)).toEqual(["wiki/bar.md"]);
  });

  it("rejects paths that escape wiki/ scope", () => {
    expect(parseQmdOutput("../escape.md\n/abs/path.md\n", 5)).toEqual([]);
  });

  it("caps at topK and dedupes", () => {
    const lines = ["wiki/a.md", "wiki/a.md", "wiki/b.md", "wiki/c.md", "wiki/d.md"].join("\n");
    expect(parseQmdOutput(lines, 3)).toEqual(["wiki/a.md", "wiki/b.md", "wiki/c.md"]);
  });

  it("returns empty for garbage or empty output", () => {
    expect(parseQmdOutput("", 5)).toEqual([]);
    expect(parseQmdOutput("no markdown paths here\n", 5)).toEqual([]);
  });
});

describe("qmdSearchHints", () => {
  it("parses hints from a fast fake qmd", async () => {
    const shim = installFakeQmd('echo "0.9 wiki/hit.md snippet"');
    expect(await runHints(shim)).toEqual(["wiki/hit.md"]);
  });

  it("returns null when qmd hangs past the deadline", async () => {
    const shim = installFakeQmd("sleep 30");
    const start = Date.now();
    expect(await runHints(shim)).toBeNull();
    expect(Date.now() - start).toBeLessThan(10_000);
  });

  it("returns null when qmd emits large output then hangs (pipe backpressure)", async () => {
    // Output exceeding the pipe buffer must not deadlock the deadline race.
    const shim = installFakeQmd(
      'i=0; while [ $i -lt 5000 ]; do echo "no markdown paths padding line $i"; i=$((i+1)); done; sleep 30'
    );
    const start = Date.now();
    expect(await runHints(shim)).toBeNull();
    expect(Date.now() - start).toBeLessThan(10_000);
  });

  it("a qmd that traps SIGTERM cannot keep the CLI alive past the deadline", async () => {
    const shim = installFakeQmd('trap "" TERM\nsleep 30');
    const start = Date.now();
    expect(await runHints(shim)).toBeNull();
    // runHints awaits natural child exit — this bounds the whole lifecycle,
    // not just the qmdSearchHints return value.
    expect(Date.now() - start).toBeLessThan(10_000);
  });

  it("returns null when qmd exits non-zero", async () => {
    const shim = installFakeQmd("exit 3");
    expect(await runHints(shim)).toBeNull();
  });

  it("returns null when qmd is absent from PATH", async () => {
    const empty = join(tmpdir(), `kb-empty-path-${Date.now()}`);
    mkdirSync(empty, { recursive: true });
    dirs.push(empty);
    expect(await runHints(empty)).toBeNull();
  });
});
