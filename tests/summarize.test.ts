import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import {
  chmodSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { createHash } from "node:crypto";
import {
  computeManifestHash,
  readManifest,
  readSummaryFrontmatter,
  writeManifest,
  writeSummaryFrontmatter,
  type SessionManifest,
} from "../src/lib/manifest";

const CLI = join(import.meta.dir, "..", "src", "cli.ts");

interface Env {
  root: string;
  vault: string;
  fakeClaude: string;
  fakeLog: string;
  manifestPath: string;
  transcriptPath: string;
  sessionId: string;
}

function makeVault(root: string): string {
  const vault = join(root, "vault");
  mkdirSync(join(vault, "sessions", "summaries"), { recursive: true });
  mkdirSync(join(vault, "sessions", ".trash"), { recursive: true });
  mkdirSync(join(vault, ".cairn"), { recursive: true });
  writeFileSync(join(vault, "log.md"), "# Vault Log\n");
  return vault;
}

function makeTranscript(root: string, texts: string[]): string {
  const path = join(root, "transcript.jsonl");
  const lines = texts.map((text, index) =>
    JSON.stringify({
      type: index % 2 === 0 ? "human" : "assistant",
      message: { content: [{ type: "text", text }] },
    })
  );
  writeFileSync(path, lines.join("\n") + "\n");
  return path;
}

function transcriptHash(path: string): string {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function writeSessionManifest(vault: string, transcriptPath: string, sessionId: string): string {
  const manifest: SessionManifest = {
    session_id: sessionId,
    timestamp: "2026-04-21T12:00:00.000Z",
    transcript_path: transcriptPath,
    transcript_hash: transcriptHash(transcriptPath),
    transcript_size: readFileSync(transcriptPath).byteLength,
    git_head: "a".repeat(40),
    branch: "main",
    files_changed: [{ path: "src/app.ts", action: "modified" }],
    excerpt: { head: "fallback head", tail: "fallback tail" },
    extracted: false,
    decisions: [],
    open_threads: [],
    tags: [],
  };
  manifest.manifest_hash = computeManifestHash(manifest);
  const path = join(vault, "sessions", `2026-04-21T12-00-00-${sessionId.slice(0, 8)}.md`);
  writeManifest(path, manifest);
  return path;
}

function makeFakeClaude(root: string): { path: string; log: string } {
  const path = join(root, "fake-claude.ts");
  const log = join(root, "fake-claude.log");
  writeFileSync(
    path,
    `#!/usr/bin/env bun
import { appendFileSync } from "node:fs";
import { createHash } from "node:crypto";
const input = await new Response(Bun.stdin.stream()).text();
if (process.env.FAKE_CLAUDE_FAIL === "1") {
  console.error("fake failure");
  process.exit(2);
}
appendFileSync(process.env.FAKE_CLAUDE_LOG!, input + "\\n---CALL---\\n");
const digest = createHash("sha256").update(input).digest("hex").slice(0, 8);
console.log("## Summary\\n\\nFake summary " + digest + "\\n\\n## Extraction Candidates\\nNone.");
`
  );
  chmodSync(path, 0o755);
  return { path, log };
}

async function runCairn(
  env: Env,
  args: string[],
  extraEnv: Record<string, string> = {}
): Promise<{ exitCode: number; stdout: string; stderr: string }> {
  const proc = Bun.spawn(["bun", CLI, ...args], {
    cwd: env.root,
    stdout: "pipe",
    stderr: "pipe",
    env: {
      ...process.env,
      CAIRN_VAULT: env.vault,
      CAIRN_SUMMARIZE_COMMAND: env.fakeClaude,
      FAKE_CLAUDE_LOG: env.fakeLog,
      ...extraEnv,
    },
  });
  const [stdout, stderr] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
  ]);
  const exitCode = await proc.exited;
  return { exitCode, stdout, stderr };
}

function callCount(logPath: string): number {
  if (!existsSync(logPath)) return 0;
  return readFileSync(logPath, "utf-8").split("---CALL---").length - 1;
}

describe("cairn summarize", () => {
  let env: Env;

  beforeEach(() => {
    const root = join(tmpdir(), `cairn-summarize-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    mkdirSync(root, { recursive: true });
    const vault = makeVault(root);
    const fake = makeFakeClaude(root);
    const transcriptPath = makeTranscript(root, ["hello", "hi there"]);
    const sessionId = "123e4567e89b12d3a456426614174000";
    const manifestPath = writeSessionManifest(vault, transcriptPath, sessionId);
    env = { root, vault, fakeClaude: fake.path, fakeLog: fake.log, manifestPath, transcriptPath, sessionId };
  });

  afterEach(() => {
    rmSync(env.root, { recursive: true, force: true });
  });

  it("writes a cached summary and short-circuits when manifest_hash matches", async () => {
    const first = await runCairn(env, ["summarize", env.manifestPath]);
    const second = await runCairn(env, ["summarize", env.manifestPath]);

    expect(first.exitCode).toBe(0);
    expect(second.exitCode).toBe(0);
    expect(callCount(env.fakeLog)).toBe(1);
    const summaryPath = first.stdout.trim().split("\n").at(-1)!;
    const { data, body } = readSummaryFrontmatter(summaryPath);
    expect(data.manifest_hash).toBe(computeManifestHash(readManifest(env.manifestPath)));
    expect(body).toContain("## Summary");
  });

  it("emits one-line JSON when --json is set", async () => {
    const result = await runCairn(env, ["summarize", "--json", env.manifestPath]);

    expect(result.exitCode).toBe(0);
    const parsed = JSON.parse(result.stdout.trim()) as { path: string; cached: boolean; degraded: boolean };
    expect(parsed.path).toContain("sessions/summaries");
    expect(parsed.cached).toBe(false);
    expect(parsed.degraded).toBe(false);
  });

  it("keeps cached truncated turn diagnostics in JSON output", async () => {
    const manifest = readManifest(env.manifestPath);
    const summaryPath = join(env.vault, "sessions", "summaries", basename(env.manifestPath));
    writeSummaryFrontmatter(
      summaryPath,
      {
        manifest_hash: computeManifestHash(manifest),
        transcript_hash: manifest.transcript_hash,
        generated_at: "2026-04-21T12:01:00.000Z",
        chunked: true,
        truncated_turns: 2,
      },
      "manual summary\n"
    );

    const result = await runCairn(env, ["summarize", "--json", env.manifestPath]);

    expect(result.exitCode).toBe(0);
    const parsed = JSON.parse(result.stdout.trim()) as {
      cached: boolean;
      chunked: boolean;
      truncated_turns: number;
    };
    expect(parsed.cached).toBe(true);
    expect(parsed.chunked).toBe(true);
    expect(parsed.truncated_turns).toBe(2);
  });

  it("resolves by full path, sessions-relative path, and session id prefix", async () => {
    const relative = `sessions/${basename(env.manifestPath)}`;
    const byPath = await runCairn(env, ["summarize", env.manifestPath]);
    const byRelative = await runCairn(env, ["summarize", relative]);
    const byPrefix = await runCairn(env, ["summarize", env.sessionId.slice(0, 8)]);

    expect(byPath.exitCode).toBe(0);
    expect(byRelative.exitCode).toBe(0);
    expect(byPrefix.exitCode).toBe(0);
    expect(byPath.stdout.trim()).toBe(byRelative.stdout.trim());
    expect(byPath.stdout.trim()).toBe(byPrefix.stdout.trim());
  });

  it("falls back to manifest excerpt when the transcript hash no longer matches", async () => {
    writeFileSync(env.transcriptPath, "mutated");
    const result = await runCairn(env, ["summarize", env.manifestPath]);

    expect(result.exitCode).toBe(0);
    const summaryPath = result.stdout.trim().split("\n").at(-1)!;
    const { data } = readSummaryFrontmatter(summaryPath);
    expect(data.degraded).toBe(true);
    expect(readFileSync(env.fakeLog, "utf-8")).toContain("fallback head");
  });

  it("chunks oversized transcripts under the configured threshold", async () => {
    const longTranscript = makeTranscript(env.root, ["a".repeat(800), "b".repeat(800), "c".repeat(800)]);
    env.manifestPath = writeSessionManifest(env.vault, longTranscript, "abcdef0123456789abcdef0123456789");

    const result = await runCairn(env, ["summarize", env.manifestPath], {
      CAIRN_SUMMARIZE_CHUNK_BYTES: "500",
    });

    expect(result.exitCode).toBe(0);
    expect(callCount(env.fakeLog)).toBeGreaterThan(1);
    const summaryPath = result.stdout.trim().split("\n").at(-1)!;
    const { data } = readSummaryFrontmatter(summaryPath);
    expect(data.chunked).toBe(true);
  });

  it("skips legacy session markdown when summarizing all manifests", async () => {
    writeFileSync(join(env.vault, "sessions", "legacy.md"), "Prompt is too long\n");

    const result = await runCairn(env, ["summarize", "--all"]);

    expect(result.exitCode).toBe(0);
    expect(result.stdout).toContain(`[1/1] summarizing ${basename(env.manifestPath)}...`);
    expect(readdirSync(join(env.vault, "sessions", "summaries")).filter((entry) => entry.endsWith(".md"))).toHaveLength(1);
  });

  it("preserves user-edited summaries unless --force is used, then trashes first", async () => {
    const first = await runCairn(env, ["summarize", env.manifestPath]);
    const summaryPath = first.stdout.trim().split("\n").at(-1)!;
    const { data } = readSummaryFrontmatter(summaryPath);
    writeSummaryFrontmatter(summaryPath, { ...data, user_edited: true }, "manual body\n");

    const skipped = await runCairn(env, ["summarize", env.manifestPath]);
    const forced = await runCairn(env, ["summarize", "--force", env.manifestPath]);

    expect(skipped.stdout.trim()).toBe(summaryPath);
    expect(forced.exitCode).toBe(0);
    const trashed = readdirSync(join(env.vault, "sessions", ".trash", "summaries"));
    expect(trashed).toHaveLength(1);
  });

  it("exits non-zero without writing a summary when the summarizer command fails", async () => {
    const result = await runCairn(env, ["summarize", env.manifestPath], {
      FAKE_CLAUDE_FAIL: "1",
    });

    expect(result.exitCode).not.toBe(0);
    expect(readdirSync(join(env.vault, "sessions", "summaries"))).toHaveLength(0);
  });
});

describe("cairn summaries pin", () => {
  let env: Env;

  beforeEach(async () => {
    const root = join(tmpdir(), `cairn-summaries-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    mkdirSync(root, { recursive: true });
    const vault = makeVault(root);
    const fake = makeFakeClaude(root);
    const transcriptPath = makeTranscript(root, ["hello"]);
    const sessionId = "123e4567e89b12d3a456426614174000";
    const manifestPath = writeSessionManifest(vault, transcriptPath, sessionId);
    env = { root, vault, fakeClaude: fake.path, fakeLog: fake.log, manifestPath, transcriptPath, sessionId };
    await runCairn(env, ["summarize", env.manifestPath]);
  });

  afterEach(() => {
    rmSync(env.root, { recursive: true, force: true });
  });

  it("sets and clears user_edited on the cached summary", async () => {
    const pin = await runCairn(env, ["summaries", "pin", env.manifestPath]);
    const summaryPath = pin.stdout.trim().split("\n").at(-1)!;
    expect(readSummaryFrontmatter(summaryPath).data.user_edited).toBe(true);

    const unpin = await runCairn(env, ["summaries", "unpin", env.manifestPath]);
    expect(unpin.exitCode).toBe(0);
    expect(readSummaryFrontmatter(summaryPath).data.user_edited).toBe(false);
  });
});
