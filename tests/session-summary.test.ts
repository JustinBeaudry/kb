import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import {
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { readManifest } from "../src/lib/manifest";

const HOOK_PATH = "hooks/session-summary";

function makeTestVault(): string {
  const dir = join(
    tmpdir(),
    `cairn-session-${Date.now()}-${Math.random().toString(36).slice(2)}`
  );
  mkdirSync(join(dir, "sessions", "summaries"), { recursive: true });
  mkdirSync(join(dir, "sessions", ".trash"), { recursive: true });
  mkdirSync(join(dir, ".cairn"), { recursive: true });
  writeFileSync(join(dir, "log.md"), "# Vault Log\n");
  return dir;
}

function makeTranscript(dir: string, messages: Array<{ type: string; content: string }>): string {
  const transcriptPath = join(dir, "transcript.jsonl");
  const lines = messages.map((m) =>
    JSON.stringify({
      type: m.type,
      message: { content: [{ type: "text", text: m.content }] },
    })
  );
  writeFileSync(transcriptPath, lines.join("\n") + "\n");
  return transcriptPath;
}

function makeHookInput(transcriptPath: string | null, sessionId: string): string {
  return JSON.stringify({
    session_id: sessionId,
    transcript_path: transcriptPath,
    cwd: process.cwd(),
    hook_event_name: "Stop",
    stop_reason: "end_turn",
  });
}

async function runHook(vault: string, input: string): Promise<number> {
  const proc = Bun.spawn(["bash", HOOK_PATH], {
    stdin: new Response(input),
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env, CAIRN_VAULT: vault },
  });
  await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
  ]);
  return proc.exited;
}

function manifestFiles(vault: string): string[] {
  return readdirSync(join(vault, "sessions")).filter((name) => name.endsWith(".md"));
}

describe("session-summary hook", () => {
  let vault: string;

  beforeEach(() => {
    vault = makeTestVault();
  });

  afterEach(() => {
    rmSync(vault, { recursive: true, force: true });
  });

  it("forwards Stop input to cairn capture-session and exits 0", async () => {
    const transcriptPath = makeTranscript(vault, [
      { type: "human", content: "Implement auth flow" },
      { type: "assistant", content: "I'll set up OAuth2 with PKCE." },
    ]);

    const exitCode = await runHook(
      vault,
      makeHookInput(transcriptPath, "123e4567-e89b-12d3-a456-426614174000")
    );

    expect(exitCode).toBe(0);
    const [manifestName] = manifestFiles(vault);
    expect(manifestName).toBeDefined();
    const manifest = readManifest(join(vault, "sessions", manifestName!));
    expect(manifest.session_id).toBe("123e4567-e89b-12d3-a456-426614174000");
    expect(manifest.excerpt.head).toContain("Implement auth flow");
    expect(manifest.excerpt.head).toContain("OAuth2 with PKCE");
  });

  it("exits 0 even when capture-session reports malformed stdin", async () => {
    const exitCode = await runHook(vault, "not json");

    expect(exitCode).toBe(0);
    const errorLog = join(vault, ".cairn", "capture-errors.log");
    expect(existsSync(errorLog)).toBe(true);
    expect(readFileSync(errorLog, "utf-8")).toContain("expected JSON");
  });

  it("forwards missing transcript paths as partial manifests", async () => {
    const exitCode = await runHook(
      vault,
      makeHookInput("/tmp/nonexistent-transcript.jsonl", "abcdef01-2345-6789-abcd-ef0123456789")
    );

    expect(exitCode).toBe(0);
    const [manifestName] = manifestFiles(vault);
    const manifest = readManifest(join(vault, "sessions", manifestName!));
    expect(manifest.transcript_hash).toBeNull();
    expect(manifest.excerpt).toEqual({ head: "", tail: "" });
  });

  it("does not contain the old jq and claude summarization path", () => {
    const hook = readFileSync(HOOK_PATH, "utf-8");
    expect(hook).not.toContain("claude -p");
    expect(hook).not.toContain("jq -r");
    expect(hook).toContain("capture-session");
  });
});
