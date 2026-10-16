import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { createHash } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import {
  computeManifestHash,
  readSummaryFrontmatter,
  writeManifest,
  writeSummaryFrontmatter,
  type SessionManifest,
} from "../src/lib/manifest";

const CLI = join(import.meta.dir, "..", "src", "cli.ts");
const VALID_SUMMARY = "## Summary\n\nA useful summary.\n\n## Extraction Candidates\n\nNone.\n";

interface TestEnv {
  root: string;
  vault: string;
  provider: string;
  providerLog: string;
  providerCompleted: string;
  transcript: string;
  manifest: string;
  summary: string;
}

interface ProviderCall {
  args: string[];
  prompt: string;
  pid: number;
}

function writeSession(
  env: TestEnv,
  records: unknown[],
  excerpt = { head: "FALLBACK_HEAD", tail: "FALLBACK_TAIL" }
): SessionManifest {
  const transcript = records.map((record) => JSON.stringify(record)).join("\n") + "\n";
  writeFileSync(env.transcript, transcript);
  const manifest = {
    session_id: "123e4567e89b12d3a456426614174000",
    timestamp: "2026-10-16T12:00:00.000Z",
    transcript_path: env.transcript,
    transcript_hash: createHash("sha256").update(transcript).digest("hex"),
    transcript_size: Buffer.byteLength(transcript),
    git_head: null,
    branch: null,
    files_changed: [],
    excerpt,
  } satisfies SessionManifest;
  writeManifest(env.manifest, manifest);
  return manifest;
}

function readProviderCalls(path: string): ProviderCall[] {
  if (!existsSync(path)) return [];
  return readFileSync(path, "utf-8").trim().split("\n").map((line) => {
    const value: unknown = JSON.parse(line);
    if (
      value === null || typeof value !== "object" ||
      !("args" in value) || !Array.isArray(value.args) ||
      !value.args.every((arg: unknown) => typeof arg === "string") ||
      !("prompt" in value) || typeof value.prompt !== "string" ||
      !("pid" in value) || typeof value.pid !== "number"
    ) {
      throw new Error("invalid provider fixture log");
    }
    return { args: value.args, prompt: value.prompt, pid: value.pid };
  });
}

async function runSummarize(
  env: TestEnv,
  options: { args?: string[]; env?: Record<string, string> } = {}
): Promise<{ exitCode: number; stdout: string; stderr: string }> {
  const proc = Bun.spawn([process.execPath, CLI, "summarize", "--json", ...(options.args ?? []), env.manifest], {
    cwd: env.root,
    stdout: "pipe",
    stderr: "pipe",
    env: {
      ...process.env,
      KB_VAULT: env.vault,
      KB_SUMMARIZE_COMMAND: env.provider,
      KB_SUMMARIZE_ARGS: undefined,
      KB_SUMMARIZE_TIMEOUT_MS: undefined,
      KB_SUMMARIZE_CHUNK_BYTES: undefined,
      SUMMARY_FIXTURE_LOG: env.providerLog,
      SUMMARY_FIXTURE_COMPLETED: env.providerCompleted,
      SUMMARY_FIXTURE_OUTPUT: VALID_SUMMARY,
      SUMMARY_FIXTURE_DELAY_MS: "0",
      ...options.env,
    },
  });
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ]);
  return { exitCode, stdout, stderr };
}

function seedCachedSummary(env: TestEnv, manifest: SessionManifest): string {
  writeSummaryFrontmatter(env.summary, {
    manifest_hash: computeManifestHash(manifest),
    transcript_hash: manifest.transcript_hash,
    generated_at: "2026-10-16T12:01:00.000Z",
    user_edited: true,
  }, "## Summary\n\nUSER_EDITED_GOOD_SUMMARY\n\n## Extraction Candidates\n\nKeep this decision.\n");
  return readFileSync(env.summary, "utf-8");
}

describe("portable session summarization", () => {
  let env: TestEnv;
  let manifest: SessionManifest;

  beforeEach(() => {
    const root = mkdtempSync(join(tmpdir(), "kb-summarizer-portability-"));
    const vault = join(root, "vault");
    mkdirSync(join(vault, "sessions", "summaries"), { recursive: true });
    const manifestPath = join(vault, "sessions", "2026-10-16T12-00-00-123e4567.md");
    env = {
      root,
      vault,
      provider: join(root, "generic summarizer.ts"),
      providerLog: join(root, "provider.jsonl"),
      providerCompleted: join(root, "provider-completed"),
      transcript: join(root, "transcript.jsonl"),
      manifest: manifestPath,
      summary: join(vault, "sessions", "summaries", basename(manifestPath)),
    };
    writeFileSync(env.provider, `#!/usr/bin/env bun
import { appendFileSync, writeFileSync } from "node:fs";
const prompt = await new Response(Bun.stdin.stream()).text();
appendFileSync(process.env.SUMMARY_FIXTURE_LOG!, JSON.stringify({ args: process.argv.slice(2), prompt, pid: process.pid }) + "\\n");
await new Promise((resolve) => setTimeout(resolve, Number(process.env.SUMMARY_FIXTURE_DELAY_MS ?? "0")));
writeFileSync(process.env.SUMMARY_FIXTURE_COMPLETED!, "completed");
process.stdout.write(process.env.SUMMARY_FIXTURE_OUTPUT ?? "");
`);
    chmodSync(env.provider, 0o755);
    manifest = writeSession(env, [
      { type: "human", message: { content: "USER_FACT" } },
      { type: "assistant", message: { content: [{ type: "text", text: "ASSISTANT_FACT" }] } },
    ]);
  });

  afterEach(() => {
    rmSync(env.root, { recursive: true, force: true });
  });

  it("sends legacy user and assistant text to the summarizer", async () => {
    const result = await runSummarize(env);

    expect(result.exitCode).toBe(0);
    expect(readProviderCalls(env.providerLog)[0]?.prompt).toContain("## User\nUSER_FACT");
    expect(readProviderCalls(env.providerLog)[0]?.prompt).toContain("## Assistant\nASSISTANT_FACT");
    expect(JSON.parse(result.stdout)).toMatchObject({ degraded: false });
  });

  for (const fixture of [
    {
      name: "Claude user",
      records: [
        { type: "user", message: { content: [{ type: "text", text: "USER_FACT" }] } },
        { type: "assistant", message: { content: "ASSISTANT_FACT" } },
      ],
    },
    {
      name: "Codex response_item",
      records: [
        { type: "event_msg", payload: { type: "user_message", message: "USER_FACT" } },
        { type: "response_item", payload: { type: "message", role: "user", content: [{ type: "input_text", text: "USER_FACT" }] } },
        { type: "response_item", payload: { type: "function_call_output", output: "TOOL_SECRET" } },
        { type: "event_msg", payload: { type: "agent_message", message: "ASSISTANT_FACT" } },
        { type: "response_item", payload: { type: "message", role: "assistant", content: [{ type: "output_text", text: "ASSISTANT_FACT" }] } },
      ],
    },
    {
      name: "generic role/content",
      records: [
        { role: "user", content: "USER_FACT" },
        { type: "message", role: "assistant", content: [{ type: "text", text: "ASSISTANT_FACT" }] },
      ],
    },
  ]) {
    it(`sends complete ${fixture.name} conversation text without duplicates`, async () => {
      writeSession(env, fixture.records);

      const result = await runSummarize(env);

      expect(result.exitCode).toBe(0);
      const prompt = readProviderCalls(env.providerLog)[0]?.prompt ?? "";
      expect(prompt).toContain("## User\nUSER_FACT");
      expect(prompt).toContain("## Assistant\nASSISTANT_FACT");
      expect(prompt.match(/USER_FACT/g)).toHaveLength(1);
      expect(prompt.match(/ASSISTANT_FACT/g)).toHaveLength(1);
      expect(prompt).not.toContain("TOOL_SECRET");
      expect(prompt).not.toContain("FALLBACK_HEAD");
      expect(JSON.parse(result.stdout)).toMatchObject({ degraded: false });
    });
  }

  it("uses the excerpt as an explicitly degraded fallback for an unsupported nonempty transcript", async () => {
    writeSession(env, [{ kind: "unknown-format", text: "UNSUPPORTED_CONTENT" }]);

    const result = await runSummarize(env);

    expect(result.exitCode).toBe(0);
    expect(readProviderCalls(env.providerLog)[0]?.prompt).toContain("FALLBACK_HEAD");
    expect(readProviderCalls(env.providerLog)[0]?.prompt).toContain("FALLBACK_TAIL");
    expect(JSON.parse(result.stdout)).toMatchObject({ degraded: true });
    expect(readSummaryFrontmatter(env.summary).data.degraded).toBe(true);
  });

  it("rejects an unsupported nonempty transcript without usable fallback before calling the provider", async () => {
    writeSession(env, [{ kind: "unknown-format", text: "UNSUPPORTED_CONTENT" }], { head: "", tail: "" });

    const result = await runSummarize(env);

    expect(result.exitCode).not.toBe(0);
    expect(result.stderr).toMatch(/transcript|conversation/i);
    expect(result.stderr).toMatch(/unsupported|supported|text|excerpt/i);
    expect(readProviderCalls(env.providerLog)).toEqual([]);
    expect(existsSync(env.summary)).toBe(false);
  });

  it("reports degraded input when a recognized malformed message accompanies valid conversation text", async () => {
    writeSession(env, [
      { type: "response_item", payload: { type: "message", role: "user", content: 42 } },
      { type: "assistant", message: { content: "VALID_RETAINED_TURN" } },
    ]);

    const result = await runSummarize(env);

    expect(result.exitCode).toBe(0);
    expect(readProviderCalls(env.providerLog)[0]?.prompt).toContain("VALID_RETAINED_TURN");
    expect(JSON.parse(result.stdout)).toMatchObject({ degraded: true });
    expect(readSummaryFrontmatter(env.summary).data.degraded).toBe(true);
  });

  for (const fixture of [
    {
      name: "Codex message with a missing role",
      record: { type: "response_item", payload: { type: "message", content: [{ type: "input_text", text: "MALFORMED_TEXT" }] } },
      retainedText: [],
    },
    {
      name: "Codex message with a non-string role",
      record: { type: "response_item", payload: { type: "message", role: 42, content: [{ type: "input_text", text: "MALFORMED_TEXT" }] } },
      retainedText: [],
    },
    {
      name: "generic message with a missing role",
      record: { type: "message", content: "MALFORMED_TEXT" },
      retainedText: [],
    },
    {
      name: "generic message with a non-string role",
      record: { type: "message", role: null, content: "MALFORMED_TEXT" },
      retainedText: [],
    },
    {
      name: "Claude content block with a missing type",
      record: { type: "user", message: { content: [{ text: "MALFORMED_TEXT" }, { type: "text", text: "RETAINED_BLOCK" }] } },
      retainedText: ["RETAINED_BLOCK"],
    },
    {
      name: "Codex content block with a non-string type",
      record: { type: "response_item", payload: { type: "message", role: "user", content: [{ type: 42, text: "MALFORMED_TEXT" }, { type: "input_text", text: "RETAINED_BLOCK" }] } },
      retainedText: ["RETAINED_BLOCK"],
    },
    {
      name: "generic content block with a missing type",
      record: { role: "user", content: [{ text: "MALFORMED_TEXT" }, { type: "text", text: "RETAINED_BLOCK" }] },
      retainedText: ["RETAINED_BLOCK"],
    },
    {
      name: "generic content block with a non-string type",
      record: { type: "message", role: "user", content: [{ type: null, text: "MALFORMED_TEXT" }, { type: "text", text: "RETAINED_BLOCK" }] },
      retainedText: ["RETAINED_BLOCK"],
    },
  ]) {
    it(`reports degraded input for a ${fixture.name} while preserving usable text`, async () => {
      writeSession(env, [fixture.record, { role: "assistant", content: "SURVIVING_TEXT" }]);

      const result = await runSummarize(env);

      expect(result.exitCode).toBe(0);
      const prompt = readProviderCalls(env.providerLog)[0]?.prompt ?? "";
      expect(prompt).toContain("## Assistant\nSURVIVING_TEXT");
      for (const text of fixture.retainedText) expect(prompt).toContain(text);
      expect(prompt).not.toContain("MALFORMED_TEXT");
      expect(prompt).not.toContain("FALLBACK_HEAD");
      expect(JSON.parse(result.stdout)).toMatchObject({ degraded: true });
      expect(readSummaryFrontmatter(env.summary).data.degraded).toBe(true);
    });
  }

  for (const fixture of [
    {
      name: "Codex system, developer, and tool roles",
      records: [
        { type: "response_item", payload: { type: "message", role: "system", content: "IGNORED_TEXT" } },
        { type: "response_item", payload: { type: "message", role: "developer", content: "IGNORED_TEXT" } },
        { type: "response_item", payload: { type: "message", role: "tool", content: "IGNORED_TEXT" } },
      ],
    },
    {
      name: "generic system, developer, and tool roles",
      records: [
        { type: "message", role: "system", content: "IGNORED_TEXT" },
        { type: "message", role: "developer", content: "IGNORED_TEXT" },
        { type: "message", role: "tool", content: "IGNORED_TEXT" },
        { role: "system", content: "IGNORED_TEXT" },
        { role: "developer", content: "IGNORED_TEXT" },
        { role: "tool", content: "IGNORED_TEXT" },
      ],
    },
    {
      name: "typed non-text content blocks",
      records: [
        { type: "user", message: { content: [{ type: "image", text: "IGNORED_TEXT" }] } },
        { type: "response_item", payload: { type: "message", role: "user", content: [{ type: "input_image", text: "IGNORED_TEXT" }] } },
        { role: "user", content: [{ type: "tool_result", text: "IGNORED_TEXT" }] },
      ],
    },
    {
      name: "unrelated metadata and tool records",
      records: [
        { type: "session_meta", role: 42, content: "IGNORED_TEXT" },
        { kind: "metadata", content: "IGNORED_TEXT" },
        { type: "response_item", payload: { type: "function_call_output", output: "IGNORED_TEXT" } },
      ],
    },
    {
      name: "mirrored Codex event records",
      records: [
        { type: "event_msg", payload: { type: "user_message", message: "IGNORED_TEXT" } },
        { type: "event_msg", payload: { type: "agent_message", message: "IGNORED_TEXT" } },
      ],
    },
  ]) {
    it(`keeps complete-source status when ignoring ${fixture.name}`, async () => {
      writeSession(env, [...fixture.records, { role: "assistant", content: "SURVIVING_TEXT" }]);

      const result = await runSummarize(env);

      expect(result.exitCode).toBe(0);
      const prompt = readProviderCalls(env.providerLog)[0]?.prompt ?? "";
      expect(prompt).toContain("## Assistant\nSURVIVING_TEXT");
      expect(prompt).not.toContain("IGNORED_TEXT");
      expect(prompt).not.toContain("FALLBACK_HEAD");
      expect(JSON.parse(result.stdout)).toMatchObject({ degraded: false });
      expect(readSummaryFrontmatter(env.summary).data.degraded ?? false).toBe(false);
    });
  }

  it("invokes a custom summarizer executable without implicit vendor flags", async () => {
    const result = await runSummarize(env);

    expect(result.exitCode).toBe(0);
    expect(readProviderCalls(env.providerLog)[0]?.args).toEqual([]);
  });

  it("passes KB_SUMMARIZE_ARGS as literal JSON string-array arguments", async () => {
    const result = await runSummarize(env, {
      env: { KB_SUMMARIZE_ARGS: '["--mode","plain text","$HOME","; touch should-not-exist"]' },
    });

    expect(result.exitCode).toBe(0);
    expect(readProviderCalls(env.providerLog)[0]?.args).toEqual([
      "--mode", "plain text", "$HOME", "; touch should-not-exist",
    ]);
    expect(existsSync(join(env.root, "should-not-exist"))).toBe(false);
  });

  for (const args of ["not json", '{"mode":"text"}', '["valid",42]']) {
    it(`rejects invalid KB_SUMMARIZE_ARGS before invoking the provider: ${args}`, async () => {
      const result = await runSummarize(env, { env: { KB_SUMMARIZE_ARGS: args } });

      expect(result.exitCode).not.toBe(0);
      expect(result.stderr).toContain("KB_SUMMARIZE_ARGS");
      expect(readProviderCalls(env.providerLog)).toEqual([]);
      expect(existsSync(env.summary)).toBe(false);
    });
  }

  for (const fixture of [
    { name: "empty", output: " \n\t" },
    { name: "unstructured", output: "The provider did not return the requested summary." },
    { name: "empty sections", output: "## Summary\n\n## Extraction Candidates\n" },
    { name: "frontmatter only", output: "---\nstatus: complete\n---\n" },
  ]) {
    it(`preserves the usable cached summary when a forced refresh returns ${fixture.name} output`, async () => {
      const previous = seedCachedSummary(env, manifest);

      const result = await runSummarize(env, {
        args: ["--force"],
        env: { SUMMARY_FIXTURE_OUTPUT: fixture.output },
      });

      expect(result.exitCode).not.toBe(0);
      expect(result.stderr).toMatch(/empty|invalid|summary|sections/i);
      expect(existsSync(env.summary)).toBe(true);
      expect(readFileSync(env.summary, "utf-8")).toBe(previous);
    });
  }

  it("does not cache empty output on the first generation", async () => {
    const result = await runSummarize(env, { env: { SUMMARY_FIXTURE_OUTPUT: "" } });

    expect(result.exitCode).not.toBe(0);
    expect(existsSync(env.summary)).toBe(false);
  });

  it("ends a timed-out provider promptly without replacing a usable cached summary", async () => {
    const previous = seedCachedSummary(env, manifest);
    const started = Date.now();

    const result = await runSummarize(env, {
      args: ["--force"],
      // A fresh executable can take hundreds of milliseconds to start on the
      // host. Allow initialization, then time out well before its 3-second work.
      env: { KB_SUMMARIZE_TIMEOUT_MS: "1000", SUMMARY_FIXTURE_DELAY_MS: "3000" },
    });

    expect(result.exitCode).not.toBe(0);
    expect(result.stderr).toMatch(/timed out|timeout|deadline/i);
    expect(result.stderr).toContain("KB_SUMMARIZE_TIMEOUT_MS");
    expect(Date.now() - started).toBeLessThan(2500);
    expect(existsSync(env.providerCompleted)).toBe(false);
    const provider = readProviderCalls(env.providerLog)[0];
    expect(provider).toBeDefined();
    if (provider) expect(() => process.kill(provider.pid, 0)).toThrow();
    expect(existsSync(env.summary)).toBe(true);
    expect(readFileSync(env.summary, "utf-8")).toBe(previous);
  });
});
