import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";

// A separate distribution smoke gate: the compiled CLI gets an empty PATH.
// No Bun, Git, Claude, qmd, hooks, credentials, or network calls are required.
const out = resolve(process.argv[2] ?? "evals/results/standalone.json");
const root = mkdtempSync(join(tmpdir(), "kb-standalone-"));
const executable = join(root, process.platform === "win32" ? "kb.exe" : "kb");
const vault = join(root, "vault");
const emptyPath = join(root, "empty-path");
mkdirSync(emptyPath);
const observations: Array<{ args: string[]; exitCode: number; stdout: string; stderr: string; stdoutBytes: number }> = [];
const checks: string[] = [];
let failure: string | null = null;
let buildOutput = "";

function run(args: string[], extra: Record<string, string> = {}, stdin?: string): string {
  const result = Bun.spawnSync([executable, ...args], {
    cwd: root, env: { PATH: emptyPath, KB_VAULT: vault, LANG: "C.UTF-8", ...extra },
    stdin: stdin === undefined ? "ignore" : Buffer.from(stdin), stdout: "pipe", stderr: "pipe",
    timeout: 15_000,
  });
  const record = { args, exitCode: result.exitCode, stdout: result.stdout.toString(), stderr: result.stderr.toString(), stdoutBytes: result.stdout.byteLength };
  observations.push(record);
  if (result.exitCode !== 0) throw new Error(`${args[0]} exited ${result.exitCode}: ${record.stderr}`);
  return record.stdout;
}

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function envelope(wire: string): Record<string, unknown> {
  const newline = wire.indexOf("\n");
  const body = wire.slice(newline + 1);
  if (Number(wire.slice(0, newline)) !== Buffer.byteLength(body)) throw new Error("Invalid wire framing");
  const parsed: unknown = JSON.parse(body);
  if (!object(parsed) || !Array.isArray(parsed.chunks) || !object(parsed.policy)) throw new Error("Invalid envelope");
  return parsed;
}

try {
  const build = Bun.spawnSync([process.execPath, "build", "--compile", "--minify", resolve(import.meta.dir, "../src/cli.ts"), "--outfile", executable], {
    cwd: root, stdout: "pipe", stderr: "pipe",
  });
  buildOutput = build.stdout.toString() + build.stderr.toString();
  if (build.exitCode !== 0) throw new Error(`Compile failed: ${buildOutput}`);
  run(["--help"]);
  run(["init", "-p", vault]);
  if (!readFileSync(join(vault, "KB.md"), "utf8").includes("Trust Boundary")) throw new Error("Missing bundled template");
  checks.push("compiled CLI help and fresh vault scaffolding");

  writeFileSync(join(vault, "wiki", "portable.md"), "# Portable Smoke\n\n## Evidence\nZoë owns the Montréal pilot.\n");
  const mapped = envelope(run(["map", "portable"]));
  if (!Array.isArray(mapped.chunks) || !mapped.chunks.some((chunk: unknown) => object(chunk) && chunk.node_id === "wiki/portable.md")) throw new Error("Missing map page");
  const fetched = envelope(run(["get-node", "wiki/portable.md#evidence"]));
  if (!JSON.stringify(fetched.chunks).includes("Zoë")) throw new Error("Missing Unicode evidence");
  checks.push("map/get-node Unicode evidence");

  const expectedLines = ["# Long", ...Array.from({ length: 200 }, (_, i) => `Line ${i}: ${"Unicode 雪 ".repeat(6)}`)];
  writeFileSync(join(vault, "wiki", "long.md"), expectedLines.join("\n"));
  let args = ["get", "long", "--budget", "1200"];
  const retrievedLines: string[] = [];
  let complete = false;
  for (let call = 0; call < 80; call++) {
    const wire = run(args);
    if (Buffer.byteLength(wire) > 1200) throw new Error("Oversized retrieval");
    const page = envelope(wire);
    const chunk: unknown = Array.isArray(page.chunks) ? page.chunks[0] : null;
    if (!object(chunk) || typeof chunk.text !== "string" || !object(page.policy)) throw new Error("Missing page evidence");
    const lines = chunk.text.split("\n");
    if (!Array.isArray(chunk.line_range) || chunk.line_range[0] !== retrievedLines.length + 1 || chunk.line_range[1] !== retrievedLines.length + lines.length) throw new Error("Continuation lost source positions");
    retrievedLines.push(...lines);
    const next = page.policy.continuation;
    if (next === undefined) { complete = true; break; }
    if (!object(next) || typeof next.command !== "string" || !Array.isArray(next.args) || !next.args.every((arg: unknown) => typeof arg === "string")) throw new Error("Invalid continuation");
    args = [next.command, ...next.args];
  }
  if (!complete || JSON.stringify(retrievedLines) !== JSON.stringify(expectedLines)) throw new Error("Continuation changed or lost source lines");
  checks.push("1200-byte retrieval replays all 201 source lines");

  const transcript = join(root, "transcript.jsonl");
  writeFileSync(transcript, [{ role: "user", content: "USER_SMOKE: audit 45 days" }, { role: "assistant", content: "ASSISTANT_SMOKE: retry 3 times" }].map((record) => JSON.stringify(record)).join("\n") + "\n");
  const manifest = run(["capture-session"], {}, JSON.stringify({ session_id: "123e4567-e89b-12d3-a456-426614174000", transcript_path: transcript })).trim();
  const captured = readFileSync(manifest, "utf8");
  if (!captured.includes("USER_SMOKE") || !captured.includes("ASSISTANT_SMOKE") || !captured.includes("git_head: null")) throw new Error("Capture lost conversation or optional provenance");
  checks.push("generic JSONL capture without Git");

  // This provider fixture requires a POSIX shell; the CLI build above is native.
  if (process.platform !== "win32") {
    const provider = join(root, "provider");
    writeFileSync(provider, "#!/bin/sh\nwhile IFS= read -r line; do :; done\nprintf '%s\\n' '## Summary' 'Portable provider smoke.' '## Extraction Candidates' 'None.'\n");
    chmodSync(provider, 0o755);
    const summary: unknown = JSON.parse(run(["summarize", "--json", manifest], { KB_SUMMARIZE_COMMAND: provider }));
    if (!object(summary) || summary.degraded !== false || typeof summary.path !== "string" || !readFileSync(summary.path, "utf8").includes("Portable provider smoke.")) throw new Error("Generic provider failed");
    checks.push("generic executable summarization without external Bun or Claude");
  }
} catch (error) {
  failure = error instanceof Error ? error.message : String(error);
} finally {
  mkdirSync(dirname(out), { recursive: true });
  const report = { pass: failure === null, failure, platform: process.platform, arch: process.arch, bun_build_version: Bun.version,
    conditions: { PATH: "empty directory", inherited_credentials: false, hooks: false, provider: process.platform === "win32" ? "skipped: POSIX fixture" : "local /bin/sh fixture" }, checks, buildOutput, observations };
  writeFileSync(out, JSON.stringify(report, null, 2).replaceAll(root, "<fixture>") + "\n");
  rmSync(root, { recursive: true, force: true });
  console.log(JSON.stringify({ pass: report.pass, checks, failure, report: out }));
}
if (failure !== null) process.exitCode = 1;
