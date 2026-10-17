import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import {
  addSession, createFixture, FACT_ASSISTANT, FACT_USER, FALLBACK_FACT, FIXTURE_VERSION,
  knowledgeSnapshot, SESSION_MARKER, writeFixtureFile, type Fixture, type TranscriptKind,
} from "./fixtures";

const DEFAULT_BUDGET = 16_384;
const CLI_TIMEOUT_MS = 15_000;
const args = process.argv.slice(2);
const option = (name: string): string | undefined => {
  const index = args.indexOf(name);
  return index >= 0 ? args[index + 1] : undefined;
};
if (args.includes("--help")) {
  console.log("bun evals/run.ts [--cli-root PATH] [--label LABEL] [--out PATH] [--compare BASELINE.json]\nRuns local deterministic CLI contracts. Exit 1 means at least one fixed expectation failed. No model calls.");
  process.exit(0);
}
const cliRoot = resolve(option("--cli-root") ?? join(import.meta.dir, ".."));
const outPath = resolve(option("--out") ?? join(import.meta.dir, "results", "current.json"));
const label = option("--label") ?? "current";

interface CommandResult {
  command: string[];
  exitCode: number;
  stdout: string;
  stderr: string;
  stdoutBytes: number;
  latencyMs: number;
  timedOut: boolean;
}
interface Check { name: string; expected: unknown; actual: unknown; pass: boolean }
interface Observation { id: string; kind: "deterministic-contract"; pass: boolean; checks: Check[]; commands: CommandResult[] }
interface Chunk { source: string; line_range: [number, number]; text: string; node_id?: string }
interface Envelope { policy: Record<string, unknown>; chunks: Chunk[] }
const object = (value: unknown): value is Record<string, unknown> => typeof value === "object" && value !== null && !Array.isArray(value);
const json = (text: string): unknown => { try { return JSON.parse(text); } catch { return null; } };
const bytes = (text: string): number => Buffer.byteLength(text, "utf8");
const sha = (text: string): string => createHash("sha256").update(text).digest("hex");

/** An independent wire validator: do not use the implementation's parser as its oracle. */
function envelope(wire: string): Envelope | null {
  const nl = wire.indexOf("\n");
  if (nl < 0 || !/^\d+$/.test(wire.slice(0, nl)) || Number(wire.slice(0, nl)) !== bytes(wire.slice(nl + 1))) return null;
  const value = json(wire.slice(nl + 1));
  if (!object(value) || value.schema_version !== "2" || !object(value.policy) || !Array.isArray(value.chunks)) return null;
  const chunks: Chunk[] = [];
  for (const item of value.chunks) {
    if (!object(item) || typeof item.source !== "string" || typeof item.text !== "string" || !Array.isArray(item.line_range)) return null;
    const [start, end] = item.line_range;
    if (typeof start !== "number" || typeof end !== "number" || !Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end < start) return null;
    chunks.push({ source: item.source, text: item.text, line_range: [start, end], ...(typeof item.node_id === "string" ? { node_id: item.node_id } : {}) });
  }
  return { policy: value.policy, chunks };
}

async function command(fixture: Fixture, commandArgs: string[], extraEnv: Record<string, string> = {}, hook = false): Promise<CommandResult> {
  const executable = hook ? join(cliRoot, "src/hooks/inject.ts") : join(cliRoot, "src/cli.ts");
  const started = performance.now();
  const proc = Bun.spawn([process.execPath, executable, ...commandArgs], {
    cwd: fixture.root, env: { ...fixture.env, ...extraEnv }, stdin: "ignore", stdout: "pipe", stderr: "pipe",
  });
  let timedOut = false;
  const timer = setTimeout(() => { timedOut = true; proc.kill("SIGKILL"); }, CLI_TIMEOUT_MS);
  try {
    const [stdout, stderr, exitCode] = await Promise.all([new Response(proc.stdout).text(), new Response(proc.stderr).text(), proc.exited]);
    return { command: [hook ? "inject" : "kb", ...commandArgs], exitCode, stdout, stderr, stdoutBytes: bytes(stdout), latencyMs: Math.round((performance.now() - started) * 100) / 100, timedOut };
  } finally { clearTimeout(timer); }
}

function providerCalls(fixture: Fixture): Array<{ args: unknown; input: string }> {
  if (!existsSync(fixture.providerLog)) return [];
  return readFileSync(fixture.providerLog, "utf8").trim().split("\n").flatMap((line) => {
    const call = json(line);
    return object(call) && typeof call.input === "string" ? [{ args: call.args, input: call.input }] : [];
  });
}

const observations: Observation[] = [];
async function trial(id: string, work: (fixture: Fixture, check: (name: string, expected: unknown, actual: unknown, pass: boolean) => void, run: typeof command) => Promise<void>): Promise<void> {
  const fixture = createFixture();
  const checks: Check[] = [];
  const commands: CommandResult[] = [];
  const check = (name: string, expected: unknown, actual: unknown, pass: boolean): void => { checks.push({ name, expected, actual, pass }); };
  const run: typeof command = async (...input) => {
    const result = await command(...input);
    commands.push(result);
    return result;
  };
  try { await work(fixture, check, run); }
  catch (error) { check("runner completed the scenario", "no harness exception", String(error), false); }
  const observation = { id, kind: "deterministic-contract", pass: checks.length > 0 && checks.every((check) => check.pass), checks, commands } satisfies Observation;
  // Retain exact stdout bytes as measured; normalize ephemeral paths only in saved text.
  observations.push(JSON.parse(JSON.stringify(observation).replaceAll(fixture.root, "<fixture>")));
  console.log(`${observation.pass ? "PASS" : "FAIL"} ${id}`);
  rmSync(fixture.root, { recursive: true, force: true });
}

for (const kind of ["legacy", "claude-user", "codex", "generic"] satisfies TranscriptKind[]) {
  await trial(`transcript-${kind}`, async (fixture, check, run) => {
    const session = addSession(fixture, kind);
    const result = await run(fixture, ["summarize", "--json", session.manifest]);
    const response = json(result.stdout);
    const input = providerCalls(fixture).map((call) => call.input).join("\n");
    check("supported transcript succeeds", 0, result.exitCode, result.exitCode === 0);
    check("both conversational roles reach provider", [FACT_USER, FACT_ASSISTANT], { user: input.includes(FACT_USER), assistant: input.includes(FACT_ASSISTANT) }, input.includes(FACT_USER) && input.includes(FACT_ASSISTANT));
    check("supported transcript is complete", false, object(response) ? response.degraded : null, object(response) && response.degraded === false);
    if (kind === "codex") {
      const userCount = input.split(FACT_USER).length - 1;
      const assistantCount = input.split(FACT_ASSISTANT).length - 1;
      check("duplicate event stream ignored", [1, 1], [userCount, assistantCount], userCount === 1 && assistantCount === 1);
      check("tool record ignored", false, input.includes("TOOL_CONTENT_MUST_NOT_BECOME_CONVERSATION"), !input.includes("TOOL_CONTENT_MUST_NOT_BECOME_CONVERSATION"));
    }
  });
}

for (const fallback of [true, false]) {
  await trial(`unsupported-transcript-${fallback ? "fallback" : "empty"}`, async (fixture, check, run) => {
    const session = addSession(fixture, "unsupported", fallback);
    const result = await run(fixture, ["summarize", "--json", session.manifest]);
    const response = json(result.stdout);
    const input = providerCalls(fixture).map((call) => call.input).join("\n");
    const rejected = result.exitCode !== 0 && result.stderr.trim().length > 0 && !existsSync(session.summary);
    const usableFallback = fallback && result.exitCode === 0 && object(response) && response.degraded === true && input.includes(FALLBACK_FACT);
    check("unsupported input is never apparently complete", fallback ? "actionable rejection or marked usable fallback" : "actionable rejection and no cache", { exitCode: result.exitCode, degraded: object(response) ? response.degraded : null, fallbackUsed: input.includes(FALLBACK_FACT), summaryWritten: existsSync(session.summary) }, rejected || usableFallback);
  });
}

for (const supplied of [false, true]) {
  await trial(`provider-${supplied ? "explicit-args" : "no-vendor-args"}`, async (fixture, check, run) => {
    const session = addSession(fixture, "legacy");
    const expected = supplied ? ["--fixture-model", "portable", "value with spaces"] : [];
    const result = await run(fixture, ["summarize", "--json", session.manifest], supplied ? { KB_SUMMARIZE_ARGS: JSON.stringify(expected) } : {});
    const calls = providerCalls(fixture);
    check("generic executable succeeds", 0, result.exitCode, result.exitCode === 0);
    check("only configured arguments reach executable", expected, calls[0]?.args ?? null, JSON.stringify(calls[0]?.args) === JSON.stringify(expected));
  });
}

for (const mode of ["empty", "invalid"]) {
  await trial(`provider-reject-${mode}`, async (fixture, check, run) => {
    const session = addSession(fixture, "legacy");
    const result = await run(fixture, ["summarize", "--json", session.manifest], { KB_EVAL_PROVIDER_MODE: mode });
    check("invalid output rejected before cache write", "nonzero, actionable stderr, no cache", { exitCode: result.exitCode, summaryWritten: existsSync(session.summary) }, result.exitCode !== 0 && result.stderr.trim().length > 0 && !existsSync(session.summary));
  });
}

for (const commandArgs of [["get", "long-evidence"], ["get-node", "wiki/long-evidence.md#evidence-rows"], ["recall", "Evidence row"]]) {
  await trial(`bounded-${commandArgs[0]}`, async (fixture, check, run) => {
    const result = await run(fixture, commandArgs);
    const parsed = envelope(result.stdout);
    check("multiline evidence succeeds", 0, result.exitCode, result.exitCode === 0);
    check("complete wire respects default budget", `<= ${DEFAULT_BUDGET}`, result.stdoutBytes, result.stdoutBytes <= DEFAULT_BUDGET);
    check("valid length-prefixed evidence", true, parsed !== null, parsed !== null);
    const next = parsed?.policy.continuation;
    check("truncation has actionable continuation", "truncated=true and command/args", parsed?.policy ?? null, parsed?.policy.truncated === true && object(next) && typeof next.command === "string" && Array.isArray(next.args) && next.args.length > 0);
  });
}

await trial("oversized-single-line", async (fixture, check, run) => {
  const result = await run(fixture, ["get", "huge-line"]);
  const parsed = envelope(result.stdout);
  const boundedSuccess = result.exitCode === 0 && result.stdoutBytes <= DEFAULT_BUDGET && parsed !== null && parsed.policy.truncated === true;
  const actionableFailure = result.exitCode !== 0 && result.stdout === "" && /line/i.test(result.stderr) && result.stderr.includes("--budget");
  check("indivisible line stays bounded with remedy", "bounded truncation or empty stdout failure with line and --budget", { exitCode: result.exitCode, bytes: result.stdoutBytes, policy: parsed?.policy ?? null }, boundedSuccess || actionableFailure);
});

for (const invalid of [false, true]) {
  await trial(`lazy-default-${invalid ? "invalid-config" : "absent-config"}`, async (fixture, check, run) => {
    if (invalid) writeFixtureFile(fixture.vault, ".kb/config.json", "{broken-json");
    const result = await run(fixture, [fixture.vault], {}, true);
    const parsed = json(result.stdout);
    const output = object(parsed) && object(parsed.hookSpecificOutput) ? parsed.hookSpecificOutput.additionalContext : null;
    const logs = existsSync(join(fixture.vault, ".kb/inject-log.jsonl")) ? readFileSync(join(fixture.vault, ".kb/inject-log.jsonl"), "utf8") : "";
    check("startup emits a pointer", "kb map", output, result.exitCode === 0 && typeof output === "string" && output.includes("kb map"));
    check("session body absent", false, typeof output === "string" && output.includes(SESSION_MARKER), typeof output === "string" && !output.includes(SESSION_MARKER));
    // Payload behavior is the oracle; the log is additional observed evidence.
    check("pointer is compact", "<= 500 context bytes", typeof output === "string" ? bytes(output) : null, typeof output === "string" && bytes(output) <= 500);
    check("hook completes", false, result.timedOut, !result.timedOut);
    if (logs) {
      const entry = json(logs.trim().split("\n").at(-1) ?? "");
      check("startup records lazy mode", "lazy", object(entry) ? entry.mode : null, object(entry) && entry.mode === "lazy");
    }
  });
}

await trial("canonical-retrieval-shell-only", async (fixture, check, run) => {
  const before = knowledgeSnapshot(fixture.vault);
  const mapped = await run(fixture, ["map", "retries"]);
  const map = envelope(mapped.stdout);
  const node = map?.chunks.find((chunk) => chunk.node_id === "wiki/delivery-policy.md#retries");
  check("map advertises exact relevant node", "wiki/delivery-policy.md#retries", node?.node_id ?? null, mapped.exitCode === 0 && node !== undefined);
  const fetched = await run(fixture, ["get-node", node?.node_id ?? "wiki/delivery-policy.md#retries"]);
  const evidence = envelope(fetched.stdout);
  const chunk = evidence?.chunks[0];
  const source = readFileSync(join(fixture.vault, "wiki/delivery-policy.md"), "utf8").split("\n");
  const exact = chunk ? source.slice(chunk.line_range[0] - 1, chunk.line_range[1]).join("\n") : null;
  check("exact source lines back answer", "Retry failed deliveries at most 3 times", chunk ?? null, fetched.exitCode === 0 && chunk?.source === "wiki/delivery-policy.md" && chunk.text.includes("Retry failed deliveries at most 3 times") && chunk.text === exact);
  check("knowledge unchanged by retrieval", before, knowledgeSnapshot(fixture.vault), JSON.stringify(before) === JSON.stringify(knowledgeSnapshot(fixture.vault)));
  check("portable tool conditions", ["bun", "fixture-provider"], readdirSync(fixture.bin).sort(), JSON.stringify(readdirSync(fixture.bin).sort()) === JSON.stringify(["bun", "fixture-provider"]));
});

await trial("absence-is-explicit", async (fixture, check, run) => {
  const result = await run(fixture, ["map", "SOC2-auditor-unknown-8271"]);
  const parsed = envelope(result.stdout);
  check("missing evidence yields no-results", { no_results: true, chunks: 0 }, { policy: parsed?.policy, chunks: parsed?.chunks.length }, result.exitCode === 0 && parsed?.policy.no_results === true && parsed.chunks.length === 0);
});

await trial("unicode-evidence", async (fixture, check, run) => {
  const result = await run(fixture, ["get", "unicode"]);
  const parsed = envelope(result.stdout);
  const text = parsed?.chunks.map((chunk) => chunk.text).join("\n") ?? "";
  check("unicode and escaping survive whole-wire framing", ["Montréal", "Zoë", "準備完了", "✅", "\"a\\b\""], text, result.exitCode === 0 && parsed !== null && ["Montréal", "Zoë", "準備完了", "✅", "\"a\\b\""].every((value) => text.includes(value)));
});

await trial("missing-provider-actionable", async (fixture, check, run) => {
  const session = addSession(fixture, "legacy");
  const result = await run(fixture, ["summarize", "--json", session.manifest], { KB_SUMMARIZE_COMMAND: join(fixture.bin, "missing-provider") });
  check("missing optional provider fails without cache", "nonzero with command guidance", { exitCode: result.exitCode, stderr: result.stderr, summaryWritten: existsSync(session.summary) }, result.exitCode !== 0 && result.stderr.trim().length > 0 && !existsSync(session.summary) && !result.timedOut);
});

async function git(...gitArgs: string[]): Promise<string | null> {
  try {
    const proc = Bun.spawn(["git", "-C", cliRoot, ...gitArgs], { stdout: "pipe", stderr: "ignore" });
    const text = await new Response(proc.stdout).text();
    return await proc.exited === 0 ? text.trim() : null;
  } catch { return null; }
}
function sourceDigest(root: string): string {
  const inputs: string[] = [];
  const visit = (path: string): void => {
    for (const item of readdirSync(join(root, path), { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const child = join(path, item.name);
      if (item.isDirectory()) visit(child);
      else if (item.isFile()) inputs.push(`${child}\n${readFileSync(join(root, child), "utf8")}`);
    }
  };
  visit("src");
  return sha(inputs.join("\n"));
}
const packageJson = json(readFileSync(join(cliRoot, "package.json"), "utf8"));
const evaluatorHash = sha(["run.ts", "fixtures.ts", "provider.ts", "scenarios.json"].map((path) => readFileSync(join(import.meta.dir, path), "utf8")).join("\n"));
const report = {
  schema_version: 1,
  evaluation_kind: "deterministic-cli-contracts",
  label,
  generated_at: new Date().toISOString(),
  fixture_version: FIXTURE_VERSION,
  evaluator_sha256: evaluatorHash,
  source: { root: cliRoot, commit: await git("rev-parse", "HEAD"), dirty_paths: await git("diff", "--name-only", "HEAD"), sha256: sourceDigest(cliRoot) },
  runtime: { bun: Bun.version, platform: process.platform, arch: process.arch, package_version: object(packageJson) ? packageJson.version : null },
  conditions: { provider: "local deterministic fixture only", optional_qmd: "absent from PATH", hooks_or_mcp_required_for_queries: false, credentials_inherited: false, cli_timeout_ms: CLI_TIMEOUT_MS, default_wire_budget_bytes: DEFAULT_BUDGET },
  summary: { total: observations.length, passed: observations.filter((item) => item.pass).length, failed: observations.filter((item) => !item.pass).length },
  observations,
  limitations: ["No model was called; these outcomes do not measure answer quality or skill compliance.", "Latency is one local wall-clock sample per CLI invocation, not a performance benchmark.", "Bytes are complete stdout measurements before temporary-path normalization.", "Actual agent outcomes require separate trials using scenarios.json and saved command/answer/write evidence."],
};
mkdirSync(dirname(outPath), { recursive: true });
writeFileSync(outPath, JSON.stringify(report, null, 2) + "\n");
console.log(`${report.summary.passed}/${report.summary.total} contracts pass; ${outPath}`);

const comparisonPath = option("--compare");
if (comparisonPath) {
  const baseline = json(readFileSync(resolve(comparisonPath), "utf8"));
  if (!object(baseline) || !Array.isArray(baseline.observations) || baseline.evaluator_sha256 !== evaluatorHash) {
    console.error("Comparison refused: baseline must use this exact evaluator and fixture corpus.");
    process.exitCode = 2;
  } else {
    const baselineById = new Map(baseline.observations.filter(object).map((item) => [item.id, item.pass]));
    const comparison = { baseline: comparisonPath, current: outPath, evaluator_sha256: evaluatorHash, changes: observations.map((item) => ({ id: item.id, baseline: baselineById.get(item.id), current: item.pass })) };
    const path = outPath.replace(/\.json$/, "") + ".comparison.json";
    writeFileSync(path, JSON.stringify(comparison, null, 2) + "\n");
    console.log(`Comparison: ${path}`);
  }
}
if (report.summary.failed > 0 && !process.exitCode) process.exitCode = 1;
