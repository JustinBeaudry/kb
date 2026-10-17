import { describe, it, expect } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, writeFileSync, rmSync } from "node:fs";

function makeTestVault(): string {
  const dir = join(tmpdir(), `kb-inject-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "sessions"), { recursive: true });
  writeFileSync(
    join(dir, "context.md"),
    "# Working Set\n\n## Active\n- [[Auth Flow]] — rebuilding auth this sprint\n"
  );
  writeFileSync(
    join(dir, "index.md"),
    "# Vault Index\n\n## Architecture\n- [[Auth Flow]] — OAuth2 implementation notes\n- [[DB Schema]] — PostgreSQL schema decisions\n"
  );
  writeFileSync(
    join(dir, "sessions", "2026-04-14T09-00-00.md"),
    "---\nsession_id: '2026-04-14T09:00:00'\nstatus: completed\n---\nImplemented auth flow.\n"
  );
  writeFileSync(
    join(dir, "sessions", "2026-04-14T10-00-00.md"),
    "---\nsession_id: '2026-04-14T10:00:00'\nstatus: in-progress\n---\nStarted DB migration.\n"
  );
  return dir;
}

describe("inject hook", () => {
  it("should output curated eager context as valid JSON", async () => {
    const vault = makeTestVault();
    const proc = Bun.spawn(["bash", "hooks/inject", vault], {
      stdout: "pipe",
      stderr: "pipe",
      env: { ...process.env, KB_INJECT_MODE: "eager" },
    });
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;

    expect(exitCode).toBe(0);
    const json = JSON.parse(output);
    expect(json.hookSpecificOutput.additionalContext).toBeDefined();
    expect(json.hookSpecificOutput.additionalContext).toContain("Vault Index");
    expect(json.hookSpecificOutput.additionalContext).toContain("Architecture");
    expect(json.hookSpecificOutput.additionalContext).not.toContain("DB migration");

    rmSync(vault, { recursive: true });
  });

  it("should exit 0 with empty context when vault missing", async () => {
    const proc = Bun.spawn(["bash", "hooks/inject", "/tmp/nonexistent-kb-vault-" + Date.now()], {
      stdout: "pipe",
      stderr: "pipe",
    });
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;

    expect(exitCode).toBe(0);
    const json = JSON.parse(output);
    expect(json.hookSpecificOutput.additionalContext).toBe("");
  });

  it("should respect configured budget", async () => {
    const vault = makeTestVault();
    writeFileSync(join(vault, "context.md"), "Large curated working set. ".repeat(200));

    const proc = Bun.spawn(["bash", "hooks/inject", vault], {
      stdout: "pipe",
      stderr: "pipe",
      env: { ...process.env, KB_INJECT_MODE: "eager", KB_BUDGET: "2048" },
    });
    const output = await new Response(proc.stdout).text();
    const json = JSON.parse(output);
    const context = json.hookSpecificOutput.additionalContext;

    expect(new TextEncoder().encode(context).length).toBeLessThanOrEqual(2048);

    rmSync(vault, { recursive: true });
  });

  it("should inject context.md before index.md", async () => {
    const vault = makeTestVault();
    const proc = Bun.spawn(["bash", "hooks/inject", vault], {
      stdout: "pipe",
      stderr: "pipe",
      env: { ...process.env, KB_INJECT_MODE: "eager" },
    });
    const output = await new Response(proc.stdout).text();
    const json = JSON.parse(output);
    const context = json.hookSpecificOutput.additionalContext;

    const workingSetPos = context.indexOf("Working Set");
    const indexPos = context.indexOf("Vault Index");
    expect(workingSetPos).toBeGreaterThanOrEqual(0);
    expect(indexPos).toBeGreaterThan(workingSetPos);

    rmSync(vault, { recursive: true });
  });

  it("should exclude cached session summaries and manifest bodies in eager mode", async () => {
    const vault = makeTestVault();
    mkdirSync(join(vault, "sessions", "summaries"), { recursive: true });
    writeFileSync(
      join(vault, "sessions", "summaries", "2026-04-14T10-00-00.md"),
      "---\nmanifest_hash: abc\ntranscript_hash: null\ngenerated_at: '2026-04-14T10:00:00Z'\n---\nCached summary body.\n"
    );

    const proc = Bun.spawn(["bash", "hooks/inject", vault], {
      stdout: "pipe",
      stderr: "pipe",
      env: { ...process.env, KB_INJECT_MODE: "eager" },
    });
    const output = await new Response(proc.stdout).text();
    const json = JSON.parse(output);
    const context = json.hookSpecificOutput.additionalContext;

    expect(context).not.toContain("Cached summary body");
    expect(context).not.toContain("Started DB migration");

    rmSync(vault, { recursive: true });
  });
});
