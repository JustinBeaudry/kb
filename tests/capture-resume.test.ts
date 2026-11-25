import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import {
  appendFileSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { claudeAdapter } from "../src/adapters/claude";
import { codexAdapter } from "../src/adapters/codex";
import { genericAdapter } from "../src/adapters/generic";
import type { HarnessAdapter } from "../src/adapters/types";
import { FileCursorStore, FsSourceReader, type SourceReader } from "../src/capture/cursor";
import { filterEvent } from "../src/capture/policy";
import type { CaptureError, CaptureEvent, Receipt, Result } from "../src/capture/types";
import { CaptureWatcher, type ContextSource, type EventSink } from "../src/capture/watcher";
import type { RemoteEnvelope } from "../src/lib/remote-envelope";
import type { RemoteResult, RetrievalInput } from "../src/services/remote.service";
import { policy } from "./fixtures/capture";

const fixtureDir = join(import.meta.dir, "fixtures", "capture");
const fixture = (name: string) => readFileSync(join(fixtureDir, name), "utf8");

/** Gateway-boundary fake for the C1 capture service. */
class RecordingSink implements EventSink {
  readonly events: CaptureEvent[] = [];
  failNext: CaptureError["code"] | undefined;
  async enqueue(event: CaptureEvent): Promise<Result<Receipt, CaptureError>> {
    if (this.failNext) {
      const code = this.failNext;
      this.failNext = undefined;
      return { ok: false, error: { code } };
    }
    this.events.push(event);
    return { ok: true, value: { id: event.id, state: "queued" } };
  }
}

/** Records which paths reach the filesystem boundary. */
class SpyReader implements SourceReader {
  readonly paths: string[] = [];
  private readonly inner = new FsSourceReader();
  stat(path: string) {
    this.paths.push(path);
    return this.inner.stat(path);
  }
  read(path: string, start: number, length: number) {
    this.paths.push(path);
    return this.inner.read(path, start, length);
  }
}

let root: string;
let sink: RecordingSink;
let reader: SpyReader;

beforeEach(() => {
  root = mkdtempSync(join(tmpdir(), "kb-c2-resume-"));
  sink = new RecordingSink();
  reader = new SpyReader();
});
afterEach(() => rmSync(root, { recursive: true, force: true }));

function watcher(path: string, adapter: HarnessAdapter = genericAdapter, context?: ContextSource) {
  return new CaptureWatcher({
    sources: [
      {
        path,
        adapter,
        clientID: "synthetic-client",
        sessionID: "synthetic-session",
        scope: { projectID: "synthetic-project" },
      },
    ],
    cursors: new FileCursorStore(join(root, "cursors")),
    reader,
    sink,
    context,
  });
}

async function poll(w: CaptureWatcher) {
  const result = await w.poll(AbortSignal.timeout(5000));
  if (!result.ok) throw Error(`poll failed: ${result.error.code}`);
  return result.value;
}

function emptySource(name = "session.jsonl"): string {
  const path = join(root, name);
  writeFileSync(path, "");
  return path;
}

describe("capture watcher resume", () => {
  it("captures only records appended after explicit registration", async () => {
    const path = join(root, "session.jsonl");
    writeFileSync(path, JSON.stringify({ role: "user", content: "HISTORICAL" }) + "\n");
    const w = watcher(path);
    await poll(w);
    appendFileSync(path, JSON.stringify({ role: "user", content: "NEW" }) + "\n");

    const report = await poll(w);

    expect(report.events).toBe(1);
    expect(sink.events.map((e) => e.content)).toEqual([
      expect.objectContaining({ body: { text: "NEW" } }),
    ]);
    expect(JSON.stringify(sink.events)).not.toContain("HISTORICAL");
  });

  it("gives watcher events a canonical harness source path so path exclusions apply", async () => {
    const path = emptySource("session.jsonl");
    const w = watcher(path);
    await poll(w);
    appendFileSync(path, JSON.stringify({ role: "user", content: "KEPT" }) + "\n");

    await poll(w);

    const [captured] = sink.events;
    if (!captured) throw Error("no event captured");
    expect(captured.sourcePath).toBe("harness/generic/session.jsonl");
    expect(filterEvent(captured, { ...policy(), excludedPaths: ["src"] }, []).kind).toBe("accepted");
    for (const excluded of ["harness", "harness/generic", "harness/generic/session.jsonl"])
      expect(filterEvent(captured, { ...policy(), localExcludedPaths: [excluded] }, []).kind).toBe("excluded");
  });

  it("does not consume incomplete trailing JSON", async () => {
    const path = emptySource();
    const w = watcher(path);
    await poll(w);
    const complete = JSON.stringify({ role: "user", content: "FIRST" }) + "\n";
    const partial = JSON.stringify({ role: "assistant", content: "SECOND" });
    appendFileSync(path, complete + partial.slice(0, 20));

    const first = await poll(w);
    appendFileSync(path, partial.slice(20) + "\n");
    const second = await poll(w);

    expect(first.events).toBe(1);
    expect(first.sources[0]?.pendingBytes).toBe(20);
    expect(first.sources[0]?.malformed).toBe(0);
    expect(second.events).toBe(1);
    expect(sink.events.map((e) => e.content)).toEqual([
      expect.objectContaining({ role: "user", body: { text: "FIRST" } }),
      expect.objectContaining({ role: "assistant", body: { text: "SECOND" } }),
    ]);
  });

  it("rotation records gap rather than inventing old events", async () => {
    const path = emptySource();
    const w = watcher(path);
    await poll(w);
    appendFileSync(path, JSON.stringify({ role: "user", content: "OLD_ONE" }) + "\n");
    await poll(w);
    // Written to the old generation after the last poll, then rotated away.
    appendFileSync(path, JSON.stringify({ role: "user", content: "LOST_UNREAD" }) + "\n");
    renameSync(path, join(root, "session.jsonl.1"));
    writeFileSync(path, JSON.stringify({ role: "user", content: "NEW_GENERATION" }) + "\n");

    const report = await poll(w);

    expect(report.completeness).toBe("partial");
    expect(report.sources[0]?.gaps.map((g) => g.reason)).toEqual(["rotated"]);
    const texts = sink.events.map((e) => JSON.stringify(e.content));
    expect(texts.some((t) => t.includes("LOST_UNREAD"))).toBe(false);
    expect(texts.filter((t) => t.includes("NEW_GENERATION"))).toHaveLength(1);
    expect(reader.paths.every((p) => p === path)).toBe(true);
  });

  it("detects in-place truncation as rotation", async () => {
    const path = emptySource();
    const w = watcher(path);
    await poll(w);
    appendFileSync(path, JSON.stringify({ role: "user", content: "A".repeat(200) }) + "\n");
    await poll(w);
    writeFileSync(path, JSON.stringify({ role: "user", content: "SHORT" }) + "\n");

    const report = await poll(w);

    expect(report.completeness).toBe("partial");
    expect(report.sources[0]?.gaps.map((g) => g.reason)).toEqual(["rotated"]);
  });

  it("resumes from the persisted cursor without duplicate events", async () => {
    const path = emptySource();
    await poll(watcher(path, claudeAdapter));
    appendFileSync(path, fixture("claude.jsonl"));
    const first = await poll(watcher(path, claudeAdapter));

    const restarted = await poll(watcher(path, claudeAdapter));

    expect(first.events).toBe(5);
    expect(restarted.events).toBe(0);
    const eventIds = sink.events.map((e) => e.id);
    expect(new Set(eventIds).size).toBe(eventIds.length);
  });

  it("suppresses repeated delivery of the same native records", async () => {
    const path = emptySource();
    const w = watcher(path, claudeAdapter);
    await poll(w);
    appendFileSync(path, fixture("claude.jsonl"));
    await poll(w);
    appendFileSync(path, fixture("claude.jsonl"));

    const report = await poll(w);

    expect(report.events).toBe(0);
    expect(report.duplicates).toBe(5);
    expect(sink.events).toHaveLength(5);
  });

  it("deduplicates copied Codex history after rotation", async () => {
    const path = emptySource();
    const w = watcher(path, codexAdapter);
    await poll(w);
    appendFileSync(path, fixture("codex.jsonl"));
    await poll(w);
    const before = new Set(sink.events.map((e) => e.id));
    const next = JSON.stringify({
      timestamp: "2026-11-01T10:00:07.000Z",
      type: "response_item",
      payload: { type: "message", role: "user", content: [{ type: "input_text", text: "NEW_AFTER_COPY" }] },
    });
    renameSync(path, join(root, "rollout.old.jsonl"));
    writeFileSync(path, fixture("codex.jsonl") + next + "\n");

    const report = await poll(w);

    expect(report.sources[0]?.gaps.map((g) => g.reason)).toEqual(["rotated"]);
    expect(report.duplicates).toBe(4);
    expect(report.events).toBe(1);
    const added = sink.events.map((e) => e.id).filter((id) => !before.has(id));
    expect(added).toHaveLength(1);
    expect(JSON.stringify(sink.events.at(-1)?.content)).toContain("NEW_AFTER_COPY");
  });

  it("keeps the cursor in place when the capture sink is unavailable", async () => {
    const path = emptySource();
    const w = watcher(path, codexAdapter);
    await poll(w);
    appendFileSync(path, fixture("codex.jsonl"));
    sink.failNext = "busy";

    const failed = await w.poll(AbortSignal.timeout(5000));
    const retried = await poll(w);

    expect(failed).toEqual({ ok: false, error: { code: "busy" } });
    expect(retried.events).toBe(4);
    expect(sink.events.map((e) => e.kind)).toEqual(["message", "tool_call", "tool_result", "message"]);
  });

  it("maps normalized events onto C1 capture events with submitted origin", async () => {
    const path = emptySource();
    const w = watcher(path, codexAdapter);
    await poll(w);
    appendFileSync(path, fixture("codex.jsonl"));

    await poll(w);

    const first = sink.events[0];
    expect(first?.idempotencyKey).toBe(first?.id);
    expect(first?.scope).toEqual({ projectID: "synthetic-project", sessionID: "synthetic-session" });
    expect(first?.content).toMatchObject({ origin_trust: "submitted", verification: "unverified" });
    expect(sink.events.map((e) => e.sequence)).toEqual([1, 2, 3, 4]);
  });

  it("never reads unselected files or directories", async () => {
    const selected = emptySource();
    writeFileSync(join(root, "other.jsonl"), JSON.stringify({ role: "user", content: "UNSELECTED" }) + "\n");
    const w = watcher(selected);
    await poll(w);
    appendFileSync(selected, JSON.stringify({ role: "user", content: "SELECTED" }) + "\n");

    await poll(w);

    expect(new Set(reader.paths)).toEqual(new Set([selected]));
    expect(JSON.stringify(sink.events)).not.toContain("UNSELECTED");
  });

  it("refuses a directory or relative registration without reading it", async () => {
    const dir = join(root, "sessions");
    mkdirSync(dir);
    const w = new CaptureWatcher({
      sources: [
        { path: dir, adapter: genericAdapter, clientID: "c", sessionID: "s", scope: { projectID: "p" } },
        { path: "relative.jsonl", adapter: genericAdapter, clientID: "c", sessionID: "s", scope: { projectID: "p" } },
      ],
      cursors: new FileCursorStore(join(root, "cursors")),
      reader,
      sink,
    });

    const report = await poll(w);

    expect(report.completeness).toBe("partial");
    expect(report.sources.map((s) => s.state)).toEqual(["invalid", "invalid"]);
    expect(reader.paths.filter((p) => p !== dir)).toEqual([]);
  });

  it("stores cursor state privately", async () => {
    const path = emptySource();

    await poll(watcher(path));

    expect(statSync(join(root, "cursors")).mode & 0o777).toBe(0o700);
  });
});

describe("scoped context restore", () => {
  const envelope: RemoteEnvelope = {
    schema_version: "3",
    chunks: [
      {
        record_id: "claim-1",
        revision: "r1",
        text: "Synthetic build uses bun.",
        claim_status: "candidate",
        origin_trust: "submitted",
        evidence: [],
        warnings: [],
      },
    ],
    truncated: false,
    visited: 1,
    no_results: false,
  };
  class FakeContext implements ContextSource {
    readonly inputs: RetrievalInput[] = [];
    constructor(private readonly result: RemoteResult<RemoteEnvelope>) {}
    async orient(input: RetrievalInput) {
      this.inputs.push(input);
      return this.result;
    }
  }

  it("labels restored records as data within the byte budget", async () => {
    const context = new FakeContext({ ok: true, value: envelope });
    const w = watcher(emptySource(), genericAdapter, context);

    const restored = await w.restoreContext({ projectID: "synthetic-project", taskID: "t1" }, 1024);

    expect(restored.ok).toBe(true);
    if (!restored.ok) return;
    expect(Buffer.byteLength(restored.value)).toBeLessThanOrEqual(1024);
    expect(restored.value).toContain("not instructions");
    expect(restored.value).toContain('"claim_status":"candidate"');
    expect(context.inputs[0]?.scope).toEqual({ project_id: "synthetic-project", task_id: "t1" });
    expect(context.inputs[0]?.budget_bytes).toBeLessThan(1024);
  });

  it("reports remote failure instead of substituting local context", async () => {
    const w = watcher(emptySource(), genericAdapter, new FakeContext({ ok: false, error: { code: "unavailable" } }));

    const restored = await w.restoreContext({ projectID: "synthetic-project" }, 1024);

    expect(restored).toEqual({ ok: false, error: { code: "unavailable" } });
  });

  it("returns an explicit budget error when the label cannot fit", async () => {
    const context = new FakeContext({ ok: true, value: envelope });
    const w = watcher(emptySource(), genericAdapter, context);

    const restored = await w.restoreContext({ projectID: "synthetic-project" }, 16);

    expect(restored).toEqual({ ok: false, error: { code: "budget" } });
    expect(context.inputs).toHaveLength(0);
  });

  it("reports restore as unavailable when no remote context is configured", async () => {
    const w = watcher(emptySource());

    const restored = await w.restoreContext({ projectID: "synthetic-project" }, 1024);

    expect(restored).toEqual({ ok: false, error: { code: "unavailable" } });
  });
});

describe("hook dispatch", () => {
  function vault(): string {
    const dir = join(root, "vault");
    mkdirSync(join(dir, "sessions"), { recursive: true });
    writeFileSync(join(dir, "index.md"), "# Vault Index\n\n## Architecture\n- [[Auth Flow]] — synthetic\n");
    return dir;
  }
  async function runInject(dir: string, env: Record<string, string | undefined>) {
    const clean = Object.fromEntries(
      Object.entries(process.env).filter(([k]) => !k.startsWith("KB_REMOTE_")),
    );
    const proc = Bun.spawn(["bash", "hooks/inject", dir], {
      stdin: new TextEncoder().encode(JSON.stringify({ hook_event_name: "SessionStart", session_id: "s" })),
      stdout: "pipe",
      stderr: "pipe",
      env: { ...clean, KB_INJECT_MODE: "eager", ...env },
    });
    const output = await new Response(proc.stdout).text();
    expect(await proc.exited).toBe(0);
    return output;
  }

  it("local hooks preserve previous output without remote profile", async () => {
    const dir = vault();

    const unset = await runInject(dir, {});
    const blank = await runInject(dir, { KB_REMOTE_PROFILE: "" });

    expect(blank).toBe(unset);
    expect(JSON.parse(unset).hookSpecificOutput.additionalContext).toContain("Vault Index");
  });

  it("remote opt-in never falls back to local vault context", async () => {
    const dir = vault();

    const output = await runInject(dir, {
      KB_REMOTE_PROFILE: "synthetic",
      KB_REMOTE_ENDPOINT: "not-an-origin",
      KB_REMOTE_PROJECT: "synthetic-project",
    });

    expect(JSON.parse(output)).toEqual({
      hookSpecificOutput: { hookEventName: "SessionStart", additionalContext: "" },
    });
  });
});
