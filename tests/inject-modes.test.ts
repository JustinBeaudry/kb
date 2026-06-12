import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, writeFileSync, rmSync, existsSync, readFileSync } from "node:fs";

function makeVault(): string {
  const dir = join(tmpdir(), `kb-inject-modes-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "sessions"), { recursive: true });
  mkdirSync(join(dir, "wiki"), { recursive: true });
  writeFileSync(
    join(dir, "context.md"),
    "# Working Set\n\n## Active\n- [[Auth Flow]] — rebuilding auth this sprint\n"
  );
  writeFileSync(
    join(dir, "index.md"),
    [
      "# Vault Index",
      "",
      "## Architecture",
      "- [[Auth Flow]]",
      "",
      "## Operations",
      "- [[Deploy Runbook]]",
      "",
      "## People",
      "- [[Team]]",
      "",
      "## Security",
      "- [[Threat Model]]",
      "",
      "## Finance",
      "- [[Budget]]",
      "",
      "## Hiring",
      "- [[Pipeline]]",
      "",
    ].join("\n")
  );
  writeFileSync(
    join(dir, "sessions", "2026-04-14T09-00-00.md"),
    "---\nsession_id: '2026-04-14T09:00:00'\nstatus: completed\n---\nImplemented auth flow.\n"
  );
  return dir;
}

const vaults: string[] = [];
function track(dir: string): string {
  vaults.push(dir);
  return dir;
}

afterEach(() => {
  for (const v of vaults.splice(0)) {
    try {
      rmSync(v, { recursive: true, force: true });
    } catch {}
  }
});

async function runHook(vault: string, env: Record<string, string> = {}): Promise<{ output: string; exitCode: number }> {
  const proc = Bun.spawn(["bash", "hooks/inject", vault], {
    stdout: "pipe",
    stderr: "pipe",
    env: { ...process.env, ...env },
  });
  const output = await new Response(proc.stdout).text();
  const exitCode = await proc.exited;
  return { output, exitCode };
}

describe("inject hook — mode: off", () => {
  it("produces empty additionalContext when KB_INJECT_MODE=off", async () => {
    const vault = track(makeVault());
    const { exitCode, output } = await runHook(vault, { KB_INJECT_MODE: "off" });
    expect(exitCode).toBe(0);
    const json = JSON.parse(output);
    expect(json.hookSpecificOutput.additionalContext).toBe("");
  });
});

describe("inject hook — mode: lazy (pointer payload)", () => {
  it("produces a pointer payload under 500 bytes", async () => {
    const vault = track(makeVault());
    const { exitCode, output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    expect(exitCode).toBe(0);
    const json = JSON.parse(output);
    const ctx: string = json.hookSpecificOutput.additionalContext;
    expect(ctx.length).toBeGreaterThan(0);
    expect(new TextEncoder().encode(ctx).length).toBeLessThan(500);
  });

  it("includes a recall hint in the pointer payload", async () => {
    const vault = track(makeVault());
    const { output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx).toMatch(/kb recall/);
  });

  it("advertises top-N index headings when index.md present", async () => {
    const vault = track(makeVault());
    const { output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx).toContain("Architecture");
  });

  it("produces a pointer payload with no categories when index.md missing", async () => {
    const vault = track(makeVault());
    rmSync(join(vault, "index.md"));
    const { exitCode, output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    expect(exitCode).toBe(0);
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx.length).toBeGreaterThan(0);
    expect(ctx).toMatch(/kb recall/);
  });

  it("stays under budget even with many index headings", async () => {
    const vault = track(makeVault());
    const headings: string[] = ["# Vault Index", ""];
    for (let i = 0; i < 100; i++) headings.push(`## Category${i}`, `- [[Page${i}]]`, "");
    writeFileSync(join(vault, "index.md"), headings.join("\n"));
    const { output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(new TextEncoder().encode(ctx).length).toBeLessThan(500);
  });

  it("does not embed raw session or wiki contents", async () => {
    const vault = track(makeVault());
    writeFileSync(join(vault, "sessions", "2026-04-14T09-00-00.md"), "SECRET_SESSION_MARKER\n");
    const { output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx).not.toContain("SECRET_SESSION_MARKER");
    expect(ctx).not.toContain("Implemented auth flow");
  });
});

describe("inject hook — mode: eager (default)", () => {
  it("preserves current behavior when no mode is set", async () => {
    const vault = track(makeVault());
    const { exitCode, output } = await runHook(vault);
    expect(exitCode).toBe(0);
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx).toContain("Working Set");
    expect(ctx).toContain("Vault Index");
    expect(ctx).toContain("Architecture");
  });

  it("respects KB_INJECT_MODE=eager explicitly", async () => {
    const vault = track(makeVault());
    const { output } = await runHook(vault, { KB_INJECT_MODE: "eager" });
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx).toContain("Working Set");
  });
});

describe("inject hook — mode precedence", () => {
  it("env overrides config.json", async () => {
    const vault = track(makeVault());
    mkdirSync(join(vault, ".kb"), { recursive: true });
    writeFileSync(join(vault, ".kb", "config.json"), JSON.stringify({ inject_mode: "eager" }));
    const { output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(new TextEncoder().encode(ctx).length).toBeLessThan(500);
    expect(ctx).toMatch(/kb recall/);
  });

  it("config.json used when env not set", async () => {
    const vault = track(makeVault());
    mkdirSync(join(vault, ".kb"), { recursive: true });
    writeFileSync(join(vault, ".kb", "config.json"), JSON.stringify({ inject_mode: "lazy" }));
    const { output } = await runHook(vault);
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(new TextEncoder().encode(ctx).length).toBeLessThan(500);
    expect(ctx).toMatch(/kb recall/);
  });

  it("invalid config.json falls through to default (eager)", async () => {
    const vault = track(makeVault());
    mkdirSync(join(vault, ".kb"), { recursive: true });
    writeFileSync(join(vault, ".kb", "config.json"), "{ not valid json");
    const { output } = await runHook(vault);
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx).toContain("Working Set");
  });
});

describe("inject hook — logging", () => {
  it("appends a minimized JSONL line to inject-log.jsonl", async () => {
    const vault = track(makeVault());
    await runHook(vault, { KB_INJECT_MODE: "lazy" });
    const logPath = join(vault, ".kb", "inject-log.jsonl");
    expect(existsSync(logPath)).toBe(true);
    const lines = readFileSync(logPath, "utf-8").trim().split("\n").filter(Boolean);
    expect(lines.length).toBe(1);
    const entry = JSON.parse(lines[0]!);
    expect(entry).toHaveProperty("timestamp");
    expect(entry).toHaveProperty("event", "inject");
    expect(entry).toHaveProperty("mode", "lazy");
    expect(entry).toHaveProperty("bytes");
    expect(typeof entry.bytes).toBe("number");
  });

  it("does not embed query or content strings in log entries", async () => {
    const vault = track(makeVault());
    writeFileSync(join(vault, "index.md"), "# Vault Index\n\n## SECRET_HEADING_MARKER\n");
    await runHook(vault, { KB_INJECT_MODE: "eager" });
    const logPath = join(vault, ".kb", "inject-log.jsonl");
    const raw = readFileSync(logPath, "utf-8");
    expect(raw).not.toContain("SECRET_HEADING_MARKER");
  });

  it("multiple inject calls append, do not overwrite", async () => {
    const vault = track(makeVault());
    await runHook(vault, { KB_INJECT_MODE: "lazy" });
    await runHook(vault, { KB_INJECT_MODE: "off" });
    await runHook(vault, { KB_INJECT_MODE: "eager" });
    const logPath = join(vault, ".kb", "inject-log.jsonl");
    const lines = readFileSync(logPath, "utf-8").trim().split("\n").filter(Boolean);
    expect(lines.length).toBe(3);
    const modes = lines.map((l) => JSON.parse(l).mode);
    expect(modes).toEqual(["lazy", "off", "eager"]);
  });
});

describe("inject hook — autoExtractNudge", () => {
  function enableNudge(vault: string): void {
    mkdirSync(join(vault, ".kb"), { recursive: true });
    writeFileSync(join(vault, ".kb", "state.json"), JSON.stringify({ autoExtractNudge: true }));
  }

  function writeManifest(vault: string, name: string, frontmatter: string): void {
    writeFileSync(join(vault, "sessions", name), `---\n${frontmatter}\n---\n`);
  }

  it("lazy mode surfaces the unprocessed count when enabled", async () => {
    const vault = track(makeVault());
    enableNudge(vault);
    writeManifest(vault, "u1.md", "extracted: false");
    // makeVault's manifest has no extracted field -> also unprocessed
    const { exitCode, output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    expect(exitCode).toBe(0);
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx).toContain("2 unprocessed session manifest");
    expect(ctx).toContain("/kb:extract");
  });

  it("lazy mode stays under the pointer budget with the nudge present", async () => {
    const vault = track(makeVault());
    enableNudge(vault);
    const headings: string[] = ["# Vault Index", ""];
    for (let i = 0; i < 100; i++) headings.push(`## Category${i}`, `- [[Page${i}]]`, "");
    writeFileSync(join(vault, "index.md"), headings.join("\n"));
    const { output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(new TextEncoder().encode(ctx).length).toBeLessThan(500);
    expect(ctx).toContain("unprocessed session manifest");
  });

  it("eager mode appends the nudge after content", async () => {
    const vault = track(makeVault());
    enableNudge(vault);
    const { output } = await runHook(vault, { KB_INJECT_MODE: "eager" });
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx).toContain("Working Set");
    expect(ctx).toContain("unprocessed session manifest");
  });

  it("no nudge when all manifests are extracted", async () => {
    const vault = track(makeVault());
    enableNudge(vault);
    writeFileSync(
      join(vault, "sessions", "2026-04-14T09-00-00.md"),
      "---\nsession_id: 'x'\nextracted: true\n---\n"
    );
    const { output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx).not.toContain("unprocessed");
  });

  it("no nudge when the flag is false or not a strict boolean", async () => {
    for (const value of [false, "yes", 1] as const) {
      const vault = track(makeVault());
      mkdirSync(join(vault, ".kb"), { recursive: true });
      writeFileSync(join(vault, ".kb", "state.json"), JSON.stringify({ autoExtractNudge: value }));
      const { output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
      const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
      expect(ctx).not.toContain("unprocessed");
    }
  });

  it("corrupt state.json silently skips the nudge", async () => {
    const vault = track(makeVault());
    mkdirSync(join(vault, ".kb"), { recursive: true });
    writeFileSync(join(vault, ".kb", "state.json"), "{ not valid json");
    const { exitCode, output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    expect(exitCode).toBe(0);
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx).toMatch(/kb recall/);
    expect(ctx).not.toContain("unprocessed");
  });

  it("malformed manifest frontmatter does not crash the scan; nudge stays one line", async () => {
    const vault = track(makeVault());
    enableNudge(vault);
    writeManifest(vault, "bad.md", "broken: [unclosed");
    const { exitCode, output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    expect(exitCode).toBe(0);
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    const nudgeLines = ctx.split("\n").filter((l) => l.includes("unprocessed"));
    expect(nudgeLines.length).toBe(1);
  });

  it("symlinked sessions dir skips the nudge and exits 0", async () => {
    const vault = track(makeVault());
    enableNudge(vault);
    const outside = track(join(tmpdir(), `kb-nudge-out-${Date.now()}`));
    mkdirSync(outside, { recursive: true });
    rmSync(join(vault, "sessions"), { recursive: true, force: true });
    const { symlinkSync } = await import("node:fs");
    symlinkSync(outside, join(vault, "sessions"));
    const { exitCode, output } = await runHook(vault, { KB_INJECT_MODE: "lazy" });
    expect(exitCode).toBe(0);
    const ctx: string = JSON.parse(output).hookSpecificOutput.additionalContext;
    expect(ctx).not.toContain("unprocessed");
  });

  it("mode off emits nothing even with the nudge enabled", async () => {
    const vault = track(makeVault());
    enableNudge(vault);
    const { output } = await runHook(vault, { KB_INJECT_MODE: "off" });
    expect(JSON.parse(output).hookSpecificOutput.additionalContext).toBe("");
  });
});

describe("inject hook — concurrency", () => {
  it("concurrent invocations produce valid JSONL (one entry per call)", async () => {
    const vault = track(makeVault());
    await Promise.all(
      Array.from({ length: 5 }).map(() => runHook(vault, { KB_INJECT_MODE: "lazy" }))
    );
    const logPath = join(vault, ".kb", "inject-log.jsonl");
    const raw = readFileSync(logPath, "utf-8");
    const lines = raw.trim().split("\n").filter(Boolean);
    expect(lines.length).toBe(5);
    for (const line of lines) {
      expect(() => JSON.parse(line)).not.toThrow();
    }
  });
});
