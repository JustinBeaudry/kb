import { afterEach, describe, expect, it } from "bun:test";
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const vaults: string[] = [];

afterEach(() => {
  for (const vault of vaults.splice(0)) rmSync(vault, { recursive: true, force: true });
});

function makeVault(): string {
  const vault = mkdtempSync(join(tmpdir(), "kb-startup-"));
  mkdirSync(join(vault, ".kb"));
  mkdirSync(join(vault, "sessions", "summaries"), { recursive: true });
  writeFileSync(join(vault, "context.md"), "# Working Set\nCURATED_WORKING_FACT\n");
  writeFileSync(join(vault, "index.md"), "# Vault Index\n## Architecture\nCURATED_INDEX_FACT\n");
  writeFileSync(join(vault, "sessions", "with-summary.md"), "---\nextracted: false\n---\nRAW_MANIFEST_WITH_SUMMARY\n");
  writeFileSync(join(vault, "sessions", "manifest-only.md"), "---\nextracted: false\n---\nRAW_MANIFEST_ONLY\n");
  writeFileSync(join(vault, "sessions", "summaries", "with-summary.md"), "GENERATED_SESSION_SUMMARY\n");
  vaults.push(vault);
  return vault;
}

async function runHook(vault: string, mode = "") {
  const proc = Bun.spawn(["bash", "hooks/inject", vault], {
    stdout: "pipe",
    stderr: "pipe",
    stdin: "ignore",
    env: { ...process.env, KB_INJECT_MODE: mode, KB_BUDGET: "20000" },
  });
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ]);
  const parsed: { hookSpecificOutput: { additionalContext: string } } = JSON.parse(stdout);
  return { context: parsed.hookSpecificOutput.additionalContext, stderr, exitCode };
}

describe("optional startup context", () => {
  for (const config of [undefined, "{ invalid JSON", JSON.stringify({ inject_mode: "nonsense" })]) {
    it(`uses a bounded lazy pointer when configuration is ${config === undefined ? "absent" : config.startsWith("{ invalid") ? "malformed" : "invalid"}`, async () => {
      const vault = makeVault();
      if (config !== undefined) writeFileSync(join(vault, ".kb", "config.json"), config);
      const { context, stderr, exitCode } = await runHook(vault);
      expect(exitCode).toBe(0);
      expect(stderr).toBe("");
      expect(new TextEncoder().encode(context).length).toBeLessThanOrEqual(500);
      expect(context).toContain("kb map");
      expect(context).toContain("Architecture");
      expect(context).not.toContain("CURATED_WORKING_FACT");
      expect(context).not.toContain("CURATED_INDEX_FACT");
      expect(context).not.toContain("RAW_MANIFEST");
      expect(context).not.toContain("GENERATED_SESSION_SUMMARY");
    });
  }

  it("falls back to lazy mode for an invalid environment setting", async () => {
    const vault = makeVault();
    const { context, exitCode } = await runHook(vault, "unknown-mode");
    expect(exitCode).toBe(0);
    expect(context).toContain("kb map");
    expect(context).not.toContain("CURATED_WORKING_FACT");
  });

  for (const source of ["environment", "configuration"]) {
    it(`explicit eager mode from ${source} includes curated context and excludes all session manifests and summaries`, async () => {
      const vault = makeVault();
      const config = JSON.stringify({ inject_mode: "eager", preserve_this: "unchanged" });
      if (source === "configuration") writeFileSync(join(vault, ".kb", "config.json"), config);
      const { context, exitCode } = await runHook(vault, source === "environment" ? "eager" : "");
      expect(exitCode).toBe(0);
      expect(context).toContain("CURATED_WORKING_FACT");
      expect(context).toContain("CURATED_INDEX_FACT");
      expect(context.indexOf("CURATED_WORKING_FACT")).toBeLessThan(context.indexOf("CURATED_INDEX_FACT"));
      expect(context).not.toContain("RAW_MANIFEST_WITH_SUMMARY");
      expect(context).not.toContain("RAW_MANIFEST_ONLY");
      expect(context).not.toContain("GENERATED_SESSION_SUMMARY");
      if (source === "configuration") expect(readFileSync(join(vault, ".kb", "config.json"), "utf-8")).toBe(config);
    });
  }
});
