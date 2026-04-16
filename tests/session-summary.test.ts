import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, writeFileSync, rmSync, readFileSync, existsSync } from "node:fs";

const HOOK_PATH = "hooks/session-summary";

function makeTestVault(): string {
  const dir = join(tmpdir(), `cairn-session-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "sessions"), { recursive: true });
  mkdirSync(join(dir, "wiki"), { recursive: true });
  writeFileSync(join(dir, "log.md"), "# Vault Log\n");
  return dir;
}

function makeTranscript(dir: string, messages: Array<{ type: string; content: string }>): string {
  const transcriptPath = join(dir, "transcript.jsonl");
  const lines = messages.map((m) => {
    if (m.type === "human") {
      return JSON.stringify({
        type: "human",
        message: { content: [{ type: "text", text: m.content }] },
      });
    }
    return JSON.stringify({
      type: "assistant",
      message: { content: [{ type: "text", text: m.content }] },
    });
  });
  writeFileSync(transcriptPath, lines.join("\n") + "\n");
  return transcriptPath;
}

function makeHookInput(transcriptPath: string, sessionId = "test-session"): string {
  return JSON.stringify({
    session_id: sessionId,
    transcript_path: transcriptPath,
    cwd: "/tmp",
    hook_event_name: "Stop",
    stop_reason: "end_turn",
  });
}

describe("session-summary hook", () => {
  let vault: string;

  beforeEach(() => {
    vault = makeTestVault();
  });

  afterEach(() => {
    rmSync(vault, { recursive: true, force: true });
  });

  it("should exit 0 when vault does not exist", async () => {
    const transcriptPath = makeTranscript(vault, [
      { type: "human", content: "hello" },
      { type: "assistant", content: "hi" },
    ]);
    const input = makeHookInput(transcriptPath);

    const proc = Bun.spawn(["bash", HOOK_PATH], {
      stdin: new Response(input),
      stdout: "pipe",
      stderr: "pipe",
      env: { ...process.env, CAIRN_VAULT: "/tmp/nonexistent-vault-" + Date.now() },
    });
    const exitCode = await proc.exited;
    expect(exitCode).toBe(0);
  });

  it("should exit 0 when transcript_path is missing", async () => {
    const input = JSON.stringify({
      session_id: "test",
      cwd: "/tmp",
      hook_event_name: "Stop",
    });

    const proc = Bun.spawn(["bash", HOOK_PATH], {
      stdin: new Response(input),
      stdout: "pipe",
      stderr: "pipe",
      env: { ...process.env, CAIRN_VAULT: vault },
    });
    const exitCode = await proc.exited;
    expect(exitCode).toBe(0);
  });

  it("should exit 0 when transcript file does not exist", async () => {
    const input = makeHookInput("/tmp/nonexistent-transcript.jsonl");

    const proc = Bun.spawn(["bash", HOOK_PATH], {
      stdin: new Response(input),
      stdout: "pipe",
      stderr: "pipe",
      env: { ...process.env, CAIRN_VAULT: vault },
    });
    const exitCode = await proc.exited;
    expect(exitCode).toBe(0);
  });

  it("should extract conversation from JSONL transcript", async () => {
    // Test the jq extraction directly — doesn't require claude
    const transcriptPath = makeTranscript(vault, [
      { type: "human", content: "Implement auth flow" },
      { type: "assistant", content: "I'll set up OAuth2 with PKCE." },
    ]);

    // Run just the jq extraction part
    const proc = Bun.spawn(
      [
        "bash",
        "-c",
        `jq -r '
      select(.type == "human" or .type == "assistant") |
      if .type == "human" then
        "## User\\n" + (
          if (.message.content | type) == "array" then
            [.message.content[] | select(.type == "text") | .text] | join("\\n")
          elif (.message.content | type) == "string" then
            .message.content
          else
            ""
          end
        )
      elif .type == "assistant" then
        "## Assistant\\n" + (
          if (.message.content | type) == "array" then
            [.message.content[] | select(.type == "text") | .text] | join("\\n")
          elif (.message.content | type) == "string" then
            .message.content
          else
            ""
          end
        )
      else
        empty
      end
    ' "${transcriptPath}"`,
      ],
      {
        stdout: "pipe",
        stderr: "pipe",
      }
    );

    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;

    expect(exitCode).toBe(0);
    expect(output).toContain("## User");
    expect(output).toContain("Implement auth flow");
    expect(output).toContain("## Assistant");
    expect(output).toContain("OAuth2 with PKCE");
  });

  it("should skip tool_use and tool_result lines in transcript", async () => {
    const transcriptPath = join(vault, "transcript.jsonl");
    const lines = [
      JSON.stringify({ type: "human", message: { content: [{ type: "text", text: "hello" }] } }),
      JSON.stringify({ type: "tool_use", tool_name: "Bash", tool_input: { command: "ls" } }),
      JSON.stringify({ type: "tool_result", content: "file1.ts\nfile2.ts" }),
      JSON.stringify({
        type: "assistant",
        message: { content: [{ type: "text", text: "Found 2 files." }] },
      }),
    ];
    writeFileSync(transcriptPath, lines.join("\n") + "\n");

    const proc = Bun.spawn(
      [
        "bash",
        "-c",
        `jq -r '
      select(.type == "human" or .type == "assistant") |
      if .type == "human" then
        "## User\\n" + (
          if (.message.content | type) == "array" then
            [.message.content[] | select(.type == "text") | .text] | join("\\n")
          elif (.message.content | type) == "string" then
            .message.content
          else
            ""
          end
        )
      elif .type == "assistant" then
        "## Assistant\\n" + (
          if (.message.content | type) == "array" then
            [.message.content[] | select(.type == "text") | .text] | join("\\n")
          elif (.message.content | type) == "string" then
            .message.content
          else
            ""
          end
        )
      else
        empty
      end
    ' "${transcriptPath}"`,
      ],
      {
        stdout: "pipe",
        stderr: "pipe",
      }
    );

    const output = await new Response(proc.stdout).text();
    expect(output).not.toContain("tool_use");
    expect(output).not.toContain("file1.ts");
    expect(output).toContain("hello");
    expect(output).toContain("Found 2 files");
  });

  describe("entire integration", () => {
    it("should detect entire enabled via status output parsing", async () => {
      // Check if entire is available in the current environment before asserting
      const checkProc = Bun.spawn(["bash", "-c", "command -v entire >/dev/null 2>&1"], {
        stdout: "pipe",
        stderr: "pipe",
      });
      const checkExit = await checkProc.exited;
      if (checkExit !== 0) {
        // entire not installed — skip rather than fail
        return;
      }

      const proc = Bun.spawn(
        ["bash", "-c", `
          if command -v entire >/dev/null 2>&1 && entire status 2>/dev/null | grep -q "Enabled"; then
            echo "enabled"
          else
            echo "disabled"
          fi
        `],
        { stdout: "pipe", stderr: "pipe", cwd: process.cwd() }
      );
      const output = (await new Response(proc.stdout).text()).trim();
      expect(output).toBe("enabled");
    });

    it("should report disabled when entire not on path", async () => {
      // Build a hermetic PATH: a temp dir containing only a symlink to bash.
      // This guarantees `entire` is not found regardless of where it is installed.
      const { symlinkSync, realpathSync } = await import("node:fs");
      const hermeticDir = join(tmpdir(), `cairn-hermetic-path-${Date.now()}`);
      mkdirSync(hermeticDir, { recursive: true });
      try {
        // Resolve real bash path and symlink it into the hermetic dir
        const bashPath = realpathSync(
          (await new Response(
            Bun.spawn(["which", "bash"], { stdout: "pipe" }).stdout
          ).text()).trim()
        );
        symlinkSync(bashPath, join(hermeticDir, "bash"));

        const proc = Bun.spawn(
          ["bash", "-c", `
            if command -v entire >/dev/null 2>&1 && entire status 2>/dev/null | grep -q "Enabled"; then
              echo "enabled"
            else
              echo "disabled"
            fi
          `],
          {
            stdout: "pipe",
            stderr: "pipe",
            env: { ...process.env, PATH: hermeticDir },
          }
        );
        const output = (await new Response(proc.stdout).text()).trim();
        expect(output).toBe("disabled");
      } finally {
        rmSync(hermeticDir, { recursive: true, force: true });
      }
    });
  });
});
