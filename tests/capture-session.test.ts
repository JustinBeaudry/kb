import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import {
  mkdirSync,
  rmSync,
  writeFileSync,
  readFileSync,
  existsSync,
  readdirSync,
} from "node:fs";
import { readManifest } from "../src/lib/manifest";

const CLI = ["bun", join(import.meta.dir, "..", "src", "cli.ts"), "capture-session"];
const describeGit = Bun.which("git") === null ? describe.skip : describe;

interface TestEnv {
  vault: string;
  cwd: string;
}

function makeVault(): string {
  const dir = join(tmpdir(), `cairn-cap-vault-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "sessions"), { recursive: true });
  mkdirSync(join(dir, "sessions", "summaries"), { recursive: true });
  mkdirSync(join(dir, "sessions", ".trash"), { recursive: true });
  mkdirSync(join(dir, ".cairn"), { recursive: true });
  writeFileSync(join(dir, "log.md"), "# Vault Log\n");
  return dir;
}

async function git(cwd: string, ...args: string[]): Promise<string> {
  const proc = Bun.spawn(["git", ...args], { cwd, stdout: "pipe", stderr: "pipe" });
  const out = await new Response(proc.stdout).text();
  await proc.exited;
  return out.trim();
}

async function makeGitRepo(): Promise<string> {
  const dir = join(tmpdir(), `cairn-cap-repo-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(dir, { recursive: true });
  await git(dir, "init", "-q", "--initial-branch=main");
  await git(dir, "config", "user.email", "test@cairn.local");
  await git(dir, "config", "user.name", "Test");
  await git(dir, "config", "commit.gpgsign", "false");
  writeFileSync(join(dir, "seed.txt"), "seed");
  await git(dir, "add", "seed.txt");
  await git(dir, "commit", "-q", "-m", "seed");
  return dir;
}

async function makeEnv(): Promise<TestEnv> {
  return { vault: makeVault(), cwd: await makeGitRepo() };
}

function makeTranscript(dir: string, messages: Array<{ type: "human" | "assistant"; text: string }>): string {
  const path = join(dir, `transcript-${Math.random().toString(36).slice(2)}.jsonl`);
  const lines = messages.map((m) =>
    JSON.stringify({
      type: m.type,
      message: { content: [{ type: "text", text: m.text }] },
    })
  );
  writeFileSync(path, lines.join("\n") + "\n");
  return path;
}

interface Stdin {
  session_id: string;
  transcript_path?: string | null;
  cwd?: string;
  hook_event_name?: string;
}

async function runCapture(env: TestEnv, input: Stdin | string): Promise<{ exitCode: number; stdout: string; stderr: string }> {
  const body = typeof input === "string" ? input : JSON.stringify(input);
  const proc = Bun.spawn(CLI, {
    stdin: new Response(body),
    stdout: "pipe",
    stderr: "pipe",
    cwd: env.cwd,
    env: { ...process.env, CAIRN_VAULT: env.vault },
  });
  const [stdout, stderr] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
  ]);
  const exitCode = await proc.exited;
  return { exitCode, stdout, stderr };
}

function listManifests(vault: string): string[] {
  return readdirSync(join(vault, "sessions")).filter((n) => n.endsWith(".md"));
}

describeGit("cairn capture-session", () => {
  let env: TestEnv;

  beforeEach(async () => {
    env = await makeEnv();
  });

  afterEach(() => {
    rmSync(env.vault, { recursive: true, force: true });
    rmSync(env.cwd, { recursive: true, force: true });
  });

  it("writes a manifest with all R2 fields on happy path", async () => {
    const transcript = makeTranscript(env.cwd, [
      { type: "human", text: "hello" },
      { type: "assistant", text: "hi there" },
    ]);
    const sessionId = "123e4567-e89b-12d3-a456-426614174000";

    const { exitCode } = await runCapture(env, {
      session_id: sessionId,
      transcript_path: transcript,
    });

    expect(exitCode).toBe(0);
    const files = listManifests(env.vault);
    expect(files).toHaveLength(1);
    const manifest = readManifest(join(env.vault, "sessions", files[0]!));

    expect(manifest.session_id).toBe(sessionId);
    expect(manifest.transcript_path).toBe(transcript);
    expect(manifest.transcript_hash).toMatch(/^[0-9a-f]{64}$/);
    expect(manifest.transcript_size).toBeGreaterThan(0);
    expect(manifest.git_head).toMatch(/^[0-9a-f]{40}$/);
    expect(manifest.branch).toBe("main");
    expect(Array.isArray(manifest.files_changed)).toBe(true);
    expect(typeof manifest.excerpt.head).toBe("string");
    expect(typeof manifest.excerpt.tail).toBe("string");
  });

  it("filename matches <ISO-timestamp>-<8hex>.md scheme", async () => {
    const transcript = makeTranscript(env.cwd, [{ type: "human", text: "hi" }]);
    const sessionId = "abcdef01-2345-6789-abcd-ef0123456789";
    await runCapture(env, { session_id: sessionId, transcript_path: transcript });

    const [name] = listManifests(env.vault);
    expect(name).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-[0-9a-f]{8}\.md$/);
    expect(name).toContain("-abcdef01.md");
  });

  it("is idempotent on the same session_id", async () => {
    const transcript = makeTranscript(env.cwd, [{ type: "human", text: "hi" }]);
    const sessionId = "11111111-2222-3333-4444-555555555555";
    const first = await runCapture(env, { session_id: sessionId, transcript_path: transcript });
    const second = await runCapture(env, { session_id: sessionId, transcript_path: transcript });

    expect(first.exitCode).toBe(0);
    expect(second.exitCode).toBe(0);
    expect(listManifests(env.vault)).toHaveLength(1);
  });

  it("handles an empty transcript (no user/assistant turns) with empty excerpt", async () => {
    const transcript = makeTranscript(env.cwd, []);
    const { exitCode } = await runCapture(env, {
      session_id: "22222222-2222-2222-2222-222222222222",
      transcript_path: transcript,
    });

    expect(exitCode).toBe(0);
    const [name] = listManifests(env.vault);
    const manifest = readManifest(join(env.vault, "sessions", name!));
    expect(manifest.excerpt).toEqual({ head: "", tail: "" });
  });

  it("fits full extracted text into head when the transcript is tiny", async () => {
    const transcript = makeTranscript(env.cwd, [
      { type: "human", text: "hi" },
      { type: "assistant", text: "hello" },
    ]);
    const { exitCode } = await runCapture(env, {
      session_id: "33333333-3333-3333-3333-333333333333",
      transcript_path: transcript,
    });

    expect(exitCode).toBe(0);
    const [name] = listManifests(env.vault);
    const manifest = readManifest(join(env.vault, "sessions", name!));
    // Head should hold all content; tail should be empty for a <2KB transcript.
    expect(manifest.excerpt.head).toBe("hi\nhello");
    expect(manifest.excerpt.tail).toBe("");
  });

  it("truncates head/tail on UTF-8 codepoint boundaries (no split surrogate pairs)", async () => {
    const emoji = "🌊".repeat(800); // 2 codepoints in JS but one user-visible glyph; ~3200 bytes
    const transcript = makeTranscript(env.cwd, [
      { type: "human", text: emoji },
      { type: "assistant", text: emoji },
    ]);
    const { exitCode } = await runCapture(env, {
      session_id: "44444444-4444-4444-4444-444444444444",
      transcript_path: transcript,
    });

    expect(exitCode).toBe(0);
    const [name] = listManifests(env.vault);
    const manifest = readManifest(join(env.vault, "sessions", name!));
    // Both head and tail must be valid UTF-16 strings (no unpaired surrogates).
    expect(() => JSON.parse(JSON.stringify(manifest.excerpt.head))).not.toThrow();
    expect(() => JSON.parse(JSON.stringify(manifest.excerpt.tail))).not.toThrow();
    for (const s of [manifest.excerpt.head, manifest.excerpt.tail]) {
      expect(s).not.toContain("\uFFFD");
      for (const ch of s.replace(/\n/g, "")) expect(ch).toBe("🌊");
    }
  });

  it("appends exactly one line to log.md per successful capture", async () => {
    const t1 = makeTranscript(env.cwd, [{ type: "human", text: "a" }]);
    const t2 = makeTranscript(env.cwd, [{ type: "human", text: "b" }]);
    await runCapture(env, { session_id: "55555555-5555-5555-5555-555555555555", transcript_path: t1 });
    await runCapture(env, { session_id: "66666666-6666-6666-6666-666666666666", transcript_path: t2 });

    const log = readFileSync(join(env.vault, "log.md"), "utf-8");
    const sessionLines = log.split("\n").filter((l) => l.includes("session |"));
    expect(sessionLines).toHaveLength(2);
    // Short session_id appears in the line for migration disambiguation later.
    expect(log).toContain("55555555");
    expect(log).toContain("66666666");
  });

  it("writes a manifest with null git fields when cwd is not a git repo", async () => {
    const noGitCwd = join(tmpdir(), `cairn-nogit-${Date.now()}-${Math.random().toString(36).slice(2)}`);
    mkdirSync(noGitCwd, { recursive: true });
    try {
      const transcript = makeTranscript(noGitCwd, [{ type: "human", text: "hi" }]);
      const result = await runCapture(
        { vault: env.vault, cwd: noGitCwd },
        { session_id: "77777777-7777-7777-7777-777777777777", transcript_path: transcript }
      );
      expect(result.exitCode).toBe(0);
      const [name] = listManifests(env.vault);
      const manifest = readManifest(join(env.vault, "sessions", name!));
      expect(manifest.git_head).toBeNull();
      expect(manifest.branch).toBeNull();
      expect(manifest.files_changed).toEqual([]);
    } finally {
      rmSync(noGitCwd, { recursive: true, force: true });
    }
  });

  it("uses abbreviated Entire checkpoints to report committed changes", async () => {
    const base = await git(env.cwd, "rev-parse", "HEAD");
    writeFileSync(join(env.cwd, "changed.txt"), "changed");
    await git(env.cwd, "add", "changed.txt");
    await git(
      env.cwd,
      "commit",
      "-q",
      "-m",
      `capture\n\nEntire-Checkpoint: ${base.slice(0, 8)}`
    );
    const transcript = makeTranscript(env.cwd, [{ type: "human", text: "hi" }]);

    const result = await runCapture(env, {
      session_id: "99999999-9999-9999-9999-999999999999",
      transcript_path: transcript,
    });

    expect(result.exitCode).toBe(0);
    const [name] = listManifests(env.vault);
    const manifest = readManifest(join(env.vault, "sessions", name!));
    expect(manifest.entire_checkpoint).toBe(base.slice(0, 8));
    expect(manifest.files_changed).toEqual([{ path: "changed.txt", action: "created" }]);
  });

  it("still writes a manifest when transcript_path is missing, with null transcript fields", async () => {
    const { exitCode } = await runCapture(env, {
      session_id: "88888888-8888-8888-8888-888888888888",
      transcript_path: "/tmp/does-not-exist.jsonl",
    });
    expect(exitCode).toBe(0);
    const [name] = listManifests(env.vault);
    const manifest = readManifest(join(env.vault, "sessions", name!));
    expect(manifest.transcript_path).toBe("/tmp/does-not-exist.jsonl");
    expect(manifest.transcript_hash).toBeNull();
    expect(manifest.transcript_size).toBeNull();
    expect(manifest.excerpt).toEqual({ head: "", tail: "" });
  });

  it("logs to .cairn/capture-errors.log and exits non-zero on non-JSON stdin", async () => {
    const { exitCode } = await runCapture(env, "this is not JSON");
    expect(exitCode).not.toBe(0);

    const logPath = join(env.vault, ".cairn", "capture-errors.log");
    expect(existsSync(logPath)).toBe(true);
    const content = readFileSync(logPath, "utf-8").trim();
    expect(content.length).toBeGreaterThan(0);
    // Each error line is JSON.
    const lines = content.split("\n");
    for (const line of lines) {
      const parsed = JSON.parse(line);
      expect(parsed.ts).toBeDefined();
      expect(parsed.error).toBeDefined();
    }
  });

  it("no manifest is written when capture fails before idempotency lock", async () => {
    // Missing session_id — capture can't proceed.
    const { exitCode } = await runCapture(env, { session_id: "" } as unknown as Stdin);
    expect(exitCode).not.toBe(0);
    expect(listManifests(env.vault)).toHaveLength(0);
  });
});
