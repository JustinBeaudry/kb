import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import {
  computeManifestHash,
  readManifest,
  readSummaryFrontmatter,
  writeManifest,
  writeSummaryFrontmatter,
  type SessionManifest,
} from "../src/lib/manifest";

const CLI = join(import.meta.dir, "..", "src", "cli.ts");
const VALID_SUMMARY = "## Summary\n\nRECOVERED_FACT was preserved.\n\n## Extraction Candidates\n\nNone.\n";
const ORIGINAL_SESSION_ID = "123e4567-e89b-12d3-a456-426614174000";

interface TestEnv {
  root: string;
  vault: string;
  transcript: string;
  provider: string;
  workerState: string;
  wrapperPid: string;
  completed: string;
  stopCommands: Array<() => void>;
}

interface WorkerState {
  pid: number;
  parentPid: number;
  startedAt: number;
  args: string[];
}

function readWorkerState(path: string): WorkerState {
  const value: unknown = JSON.parse(readFileSync(path, "utf-8"));
  if (
    value === null || typeof value !== "object" ||
    !("pid" in value) || typeof value.pid !== "number" ||
    !("parentPid" in value) || typeof value.parentPid !== "number" ||
    !("startedAt" in value) || typeof value.startedAt !== "number" ||
    !("args" in value) || !Array.isArray(value.args) ||
    !value.args.every((arg: unknown) => typeof arg === "string")
  ) {
    throw new Error("invalid worker fixture state");
  }
  return { pid: value.pid, parentPid: value.parentPid, startedAt: value.startedAt, args: value.args };
}

function isAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    if (process.platform === "linux") {
      // A killed orphan can remain as a zombie under a container's PID 1. It
      // cannot perform work; the late-marker assertion still checks activity.
      const state = readFileSync(`/proc/${pid}/stat`, "utf-8").match(/^\d+ \(.*\) (\w)/)?.[1];
      if (state === "Z" || state === "X") return false;
    }
    return true;
  } catch {
    return false;
  }
}

function stopProcess(pid: number): void {
  if (!Number.isSafeInteger(pid) || pid <= 0) return;
  try {
    process.kill(pid, "SIGKILL");
  } catch {
    // The fixture may already have exited or been killed by the summarizer.
  }
}

async function runCli(
  env: TestEnv,
  args: string[],
  options: { input?: string; env?: Record<string, string>; onSpawn?: (pid: number) => void } = {}
): Promise<{ exitCode: number; signalCode: NodeJS.Signals | null; stdout: string; stderr: string; finishedAt: number }> {
  const proc = Bun.spawn([process.execPath, CLI, ...args], {
    cwd: env.root,
    // Isolate the fixture caller so an incorrect process-group signal cannot
    // terminate the test runner. A killed caller still fails the CLI assertions.
    detached: process.platform !== "win32",
    stdin: options.input === undefined ? "ignore" : new Response(options.input),
    stdout: "pipe",
    stderr: "pipe",
    env: {
      ...process.env,
      PATH: process.platform === "win32" ? process.env.PATH : "/usr/bin:/bin",
      KB_VAULT: env.vault,
      KB_TRANSCRIPT_STABLE_MS: "0",
      KB_SUMMARIZE_COMMAND: process.execPath,
      KB_SUMMARIZE_ARGS: JSON.stringify([env.provider]),
      KB_SUMMARIZE_TIMEOUT_MS: "1000",
      KB_SUMMARIZE_CHUNK_BYTES: undefined,
      KB_TEST_WORKER_STATE: env.workerState,
      KB_TEST_WRAPPER_PID: env.wrapperPid,
      KB_TEST_COMPLETED: env.completed,
      KB_TEST_DELAY_MS: "0",
      GIT_CONFIG_GLOBAL: process.platform === "win32" ? "NUL" : "/dev/null",
      GIT_CONFIG_NOSYSTEM: "1",
      ...options.env,
    },
  });
  options.onSpawn?.(proc.pid);
  const stop = () => {
    if (proc.exitCode === null && proc.signalCode === null) proc.kill("SIGKILL");
  };
  env.stopCommands.push(stop);
  const watchdog = setTimeout(stop, 6000);
  try {
    const [stdout, stderr, exitCode] = await Promise.all([
      new Response(proc.stdout).text(),
      new Response(proc.stderr).text(),
      proc.exited,
    ]);
    return { exitCode, signalCode: proc.signalCode, stdout: stdout.trim(), stderr: stderr.trim(), finishedAt: Date.now() };
  } finally {
    clearTimeout(watchdog);
  }
}

describe("summarizer invocation lifecycle and recovery", () => {
  let env: TestEnv;

  beforeEach(() => {
    const root = mkdtempSync(join(tmpdir(), "kb-summarizer-lifecycle-"));
    env = {
      root,
      vault: join(root, "vault"),
      transcript: join(root, "transcript with 'quotes' and $literal.jsonl"),
      provider: join(root, "local provider.ts"),
      workerState: join(root, "worker-state.json"),
      wrapperPid: join(root, "wrapper-pid"),
      completed: join(root, "worker-completed"),
      stopCommands: [],
    };
    mkdirSync(join(env.vault, "sessions", "summaries"), { recursive: true });
    writeFileSync(env.provider, `import { renameSync, writeFileSync } from "node:fs";
await new Response(Bun.stdin.stream()).text();
const statePath = process.env.KB_TEST_WORKER_STATE!;
writeFileSync(statePath + ".tmp", JSON.stringify({
  pid: process.pid, parentPid: process.ppid, startedAt: Date.now(), args: process.argv.slice(2),
}));
renameSync(statePath + ".tmp", statePath);
await Bun.sleep(Number(process.env.KB_TEST_DELAY_MS));
writeFileSync(process.env.KB_TEST_COMPLETED!, "provider work completed");
process.stdout.write(${JSON.stringify(VALID_SUMMARY)});
`);
  });

  afterEach(() => {
    // Read persisted PIDs even when the RED assertion or CLI watchdog fails.
    // The provider uses a timer, so it has no unrecorded sleep subprocess.
    if (existsSync(env.workerState)) stopProcess(readWorkerState(env.workerState).pid);
    if (existsSync(env.wrapperPid)) stopProcess(Number(readFileSync(env.wrapperPid, "utf-8")));
    for (const stop of env.stopCommands) stop();
    rmSync(env.root, { recursive: true, force: true });
  });

  it("stops a wrapper's foreground provider at the deadline and preserves the cached summary", async () => {
    const transcript = JSON.stringify({ role: "user", content: "RECOVERED_FACT" }) + "\n";
    writeFileSync(env.transcript, transcript);
    const manifestPath = join(env.vault, "sessions", "2026-10-16T12-00-00-123e4567.md");
    const manifest = {
      session_id: ORIGINAL_SESSION_ID,
      timestamp: "2026-10-16T12:00:00.000Z",
      transcript_path: env.transcript,
      transcript_hash: createHash("sha256").update(transcript).digest("hex"),
      transcript_size: Buffer.byteLength(transcript),
      git_head: null, branch: null, files_changed: [],
      excerpt: { head: "RECOVERED_FACT", tail: "" },
    } satisfies SessionManifest;
    writeManifest(manifestPath, manifest);
    const summaryPath = join(env.vault, "sessions", "summaries", basename(manifestPath));
    writeSummaryFrontmatter(summaryPath, {
      manifest_hash: computeManifestHash(manifest),
      transcript_hash: manifest.transcript_hash,
      generated_at: "2026-10-16T12:01:00.000Z",
      user_edited: true,
    }, "## Summary\n\nKEEP_USER_EDITED_SUMMARY\n\n## Extraction Candidates\n\nKeep this decision.\n");
    const originalSummary = readFileSync(summaryPath, "utf-8");
    const wrapper = join(env.root, process.platform === "win32" ? "wrapper.ts" : "wrapper.sh");
    writeFileSync(wrapper, process.platform === "win32"
      ? `import { writeFileSync } from "node:fs";
writeFileSync(process.env.KB_TEST_WRAPPER_PID!, String(process.pid));
const child = Bun.spawn(process.argv.slice(2), { stdin: "inherit", stdout: "inherit", stderr: "inherit" });
process.exit(await child.exited);
`
      : '#!/bin/sh\nprintf \'%s\\n\' "$$" > "$KB_TEST_WRAPPER_PID"\n"$@"\nstatus=$?\nexit "$status"\n');
    const literalArgs = ["argument with spaces", "$HOME; $(not-a-command)"];
    const emptyPath = join(env.root, "empty-path");
    mkdirSync(emptyPath);

    const result = await runCli(env, ["summarize", "--json", "--force", manifestPath], {
      env: {
        PATH: emptyPath,
        KB_SUMMARIZE_COMMAND: process.platform === "win32" ? process.execPath : "/bin/sh",
        KB_SUMMARIZE_ARGS: JSON.stringify([wrapper, process.execPath, env.provider, ...literalArgs]),
        KB_TEST_DELAY_MS: "3000",
      },
    });

    const worker = readWorkerState(env.workerState);
    // Allow OS reaping without assuming a fixed process startup duration.
    const reapDeadline = Date.now() + 500;
    while (isAlive(worker.pid) && Date.now() < reapDeadline) await Bun.sleep(20);
    const childSurvivedDeadline = isAlive(worker.pid);
    // Observe past the provider's own completion time before cleanup, including
    // on RED, so a late marker cannot be hidden by fixture cleanup.
    await Bun.sleep(Math.max(0, worker.startedAt + 3300 - Date.now()));

    expect(result.exitCode).not.toBe(0);
    expect(result.stderr).toMatch(/timed out|timeout|deadline/i);
    expect(result.stderr).toContain("KB_SUMMARIZE_TIMEOUT_MS");
    expect(result.finishedAt - worker.startedAt).toBeLessThan(2500);
    expect(worker.parentPid).toBe(Number(readFileSync(env.wrapperPid, "utf-8")));
    expect(worker.args).toEqual(literalArgs);
    expect(childSurvivedDeadline).toBe(false);
    expect(existsSync(env.completed)).toBe(false);
    expect(readFileSync(summaryPath, "utf-8")).toBe(originalSummary);
  }, 10000);

  it("recovers using the error's new capture input without rewriting the original manifest", async () => {
    writeFileSync(env.transcript, JSON.stringify({ unsupported: "UNREADABLE_FACT" }) + "\n");
    const captured = await runCli(env, ["capture-session"], {
      input: JSON.stringify({ session_id: ORIGINAL_SESSION_ID, transcript_path: env.transcript }),
    });
    expect(captured.exitCode).toBe(0);
    const originalPath = captured.stdout;
    const originalManifest = readFileSync(originalPath, "utf-8");
    const rejected = await runCli(env, ["summarize", "--json", originalPath]);
    expect(rejected.exitCode).not.toBe(0);
    expect(existsSync(env.workerState)).toBe(false);

    // The recovery payload is data to put on stdin, never shell source to eval.
    const recoveryJson = rejected.stderr.split("\n").find((line) => line.trim().startsWith("{"));
    expect(recoveryJson).toBeDefined();
    if (recoveryJson === undefined) throw new Error("no actionable capture input in the error");
    const recovery: unknown = JSON.parse(recoveryJson);
    if (
      recovery === null || typeof recovery !== "object" ||
      !("session_id" in recovery) || typeof recovery.session_id !== "string" ||
      !("transcript_path" in recovery) || typeof recovery.transcript_path !== "string"
    ) {
      throw new Error("recovery input requires session_id and transcript_path");
    }
    expect(recovery.session_id).not.toBe(ORIGINAL_SESSION_ID);
    expect(recovery.transcript_path).toBe(env.transcript);
    expect(rejected.stderr).toContain("kb capture-session");
    expect(rejected.stderr).toMatch(/kb summarize --json.*manifest/i);
    writeFileSync(recovery.transcript_path, JSON.stringify({ role: "user", content: "RECOVERED_FACT" }) + "\n");

    const recaptured = await runCli(env, ["capture-session"], { input: recoveryJson });
    expect(recaptured.exitCode).toBe(0);
    expect(recaptured.stdout).not.toBe(originalPath);
    const summarized = await runCli(env, ["summarize", "--json", recaptured.stdout]);

    expect(summarized.exitCode).toBe(0);
    const summary = readSummaryFrontmatter(join(env.vault, "sessions", "summaries", basename(recaptured.stdout)));
    expect(summary.body).toContain("RECOVERED_FACT");
    expect(summary.data.degraded ?? false).toBe(false);
    expect(readManifest(recaptured.stdout).session_id).toBe(recovery.session_id);
    expect(readFileSync(originalPath, "utf-8")).toBe(originalManifest);
  }, 10000);

  const interruptSignals: Array<"SIGINT" | "SIGTERM"> = ["SIGINT", "SIGTERM"];
  // Windows process.kill terminates directly; it does not deliver catchable
  // POSIX signals. The timeout test above still exercises its wrapper tree.
  const itPosixSignal = process.platform === "win32" ? it.skip : it;
  for (const signal of interruptSignals) {
    itPosixSignal(`stops provider work when the caller receives ${signal}`, async () => {
      writeFileSync(env.transcript, JSON.stringify({ role: "user", content: "RECOVERED_FACT" }) + "\n");
      const captured = await runCli(env, ["capture-session"], {
        input: JSON.stringify({ session_id: ORIGINAL_SESSION_ID, transcript_path: env.transcript }),
      });
      expect(captured.exitCode).toBe(0);
      let callerPid = 0;
      const pending = runCli(env, ["summarize", "--json", captured.stdout], {
        onSpawn: (pid) => { callerPid = pid; },
        env: { KB_TEST_DELAY_MS: "3000", KB_SUMMARIZE_TIMEOUT_MS: "10000" },
      });
      const readyDeadline = Date.now() + 5000;
      while (!existsSync(env.workerState) && Date.now() < readyDeadline) await Bun.sleep(20);
      const worker = readWorkerState(env.workerState);

      process.kill(callerPid, signal);
      const result = await pending;

      const reapDeadline = Date.now() + 500;
      while (isAlive(worker.pid) && Date.now() < reapDeadline) await Bun.sleep(20);
      const childSurvivedInterrupt = isAlive(worker.pid);
      await Bun.sleep(Math.max(0, worker.startedAt + 3300 - Date.now()));
      expect(result.exitCode).not.toBe(0);
      expect(result.signalCode).toBe(signal);
      expect(result.finishedAt - worker.startedAt).toBeLessThan(2500);
      expect(childSurvivedInterrupt).toBe(false);
      expect(existsSync(env.completed)).toBe(false);
      expect(existsSync(join(env.vault, "sessions", "summaries", basename(captured.stdout)))).toBe(false);
    }, 10000);
  }
});
