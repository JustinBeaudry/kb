// Reproduce the review's first-page allocation probe on macOS.
// Usage: bun profile-recall.mjs CLI_ROOT LABEL OUTPUT_JSON
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";

const [cliRootArg, label, outArg] = process.argv.slice(2);
if (!cliRootArg || !label || !outArg || process.platform !== "darwin") {
  throw new Error("Usage on macOS: bun profile-recall.mjs CLI_ROOT LABEL OUTPUT_JSON");
}
const cliRoot = resolve(cliRootArg);
const out = resolve(outArg);
const root = mkdtempSync(join(tmpdir(), "kb-recall-profile-"));
const vault = join(root, "vault");
const wiki = join(vault, "wiki");
const emptyPath = join(root, "empty-path");
const samples = [];
const content = "# Probe\n" + (`needle ${"evidence ".repeat(10)}\n`).repeat(1000);

try {
  mkdirSync(wiki, { recursive: true });
  mkdirSync(emptyPath);
  for (let i = 0; i < 64; i++) {
    writeFileSync(join(wiki, `${String(i).padStart(3, "0")}.md`), content);
  }
  for (let sample = 0; sample < 3; sample++) {
    const started = performance.now();
    const proc = Bun.spawn(["/usr/bin/time", "-l", process.execPath, join(cliRoot, "src/cli.ts"), "recall", "needle", "-p", vault], {
      cwd: root,
      env: { PATH: emptyPath, LANG: "C.UTF-8", KB_VAULT: vault },
      stdin: "ignore", stdout: "pipe", stderr: "pipe",
    });
    const [stdout, stderr, exitCode] = await Promise.all([
      new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited,
    ]);
    const wallMs = performance.now() - started;
    const rss = stderr.match(/(\d+)\s+maximum resident set size/);
    if (exitCode !== 0 || !rss) throw new Error(`Probe failed: ${exitCode}\n${stderr}`);
    const newline = stdout.indexOf("\n");
    const body = stdout.slice(newline + 1);
    if (Number(stdout.slice(0, newline)) !== Buffer.byteLength(body)) throw new Error("Invalid wire framing");
    const envelope = JSON.parse(body);
    samples.push({ wallMs, peakRssBytes: Number(rss[1]), stdoutBytes: Buffer.byteLength(stdout), chunks: envelope.chunks.length, stderr });
  }
  const median = (key) => samples.map(s => s[key]).sort((a,b) => a-b)[1];
  const report = {
    label, cliRoot, platform: process.platform, arch: process.arch, bunVersion: Bun.version,
    conditions: { files: 64, sourceBytes: 64 * Buffer.byteLength(content), matches: 64000, runs: 3, PATH: "empty directory", inheritedCredentials: false },
    medians: { wallMs: median("wallMs"), peakRssBytes: median("peakRssBytes"), stdoutBytes: median("stdoutBytes"), chunks: median("chunks") }, samples,
  };
  mkdirSync(dirname(out), { recursive: true });
  writeFileSync(out, JSON.stringify(report, null, 2) + "\n");
  console.log(JSON.stringify({ label, ...report.medians, out }));
} finally {
  rmSync(root, { recursive: true, force: true });
}
