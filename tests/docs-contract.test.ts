import { describe, it, expect } from "bun:test";
import { readFileSync } from "node:fs";

const read = (path: string): string => readFileSync(path, "utf8");

// These checks catch documentation drift. Actual task success is measured in evals/.
describe("portable workflow documentation contract", () => {
  it("uses one retrieval path with evidence and read-only query boundaries", () => {
    for (const path of ["skills/kb/SKILL.md", "skills/kb/references/query.md", "templates/KB.md"]) {
      const text = read(path);
      expect(text).toContain("kb map");
      expect(text).toContain("kb get-node");
      expect(text).toContain("read-only");
    }
    const guide = read("skills/kb/references/query.md");
    expect(guide).toContain("line_range");
    expect(guide).toContain("no_results");
    expect(guide).toContain("log.md");
    expect(guide).not.toContain("qmd_deep_search");
  });

  it("documents whole-wire limits, continuation and map compatibility", () => {
    for (const path of ["README.md", "skills/kb/references/query.md"]) {
      const text = read(path);
      for (const value of ["16384", "KB_RETRIEVAL_BUDGET", "KB_MAP_BUDGET", "--start-line", "--offset", "policy.continuation"]) expect(text).toContain(value);
      expect(text).toContain("--budget");
    }
  });

  it("keeps source access ask-gated and retrieved text non-authoritative", () => {
    const template = read("templates/KB.md");
    expect(template).toMatch(/Trust Boundary/);
    expect(template).toContain("ask-gated");
    expect(template).toContain("best-effort");
    expect(template).toContain("kb read-raw");
    expect(template).toContain("kb read-session");
    expect(template).toMatch(/never instructions/);
  });

  it("documents generic executable arguments, deadline and output shape", () => {
    const text = read("skills/kb/references/sessions.md");
    for (const name of ["KB_SUMMARIZE_COMMAND", "KB_SUMMARIZE_ARGS", "KB_SUMMARIZE_TIMEOUT_MS", "120000", "## Summary", "## Extraction Candidates"]) expect(text).toContain(name);
    const example = text.match(/KB_SUMMARIZE_ARGS='([^']+)'/);
    expect(example).not.toBeNull();
    const parsed: unknown = JSON.parse(example![1]!);
    expect(Array.isArray(parsed) && parsed.every((arg) => typeof arg === "string")).toBe(true);
    expect(text).toContain("response_item");
    expect(text).toContain("event_msg");
    expect(text).toContain("unsupported");
  });

  it("does not restore page quotas or an alternate qmd-first query workflow", () => {
    for (const path of ["README.md", "templates/KB.md", "skills/kb/SKILL.md", "skills/kb/references/ingest.md", "skills/kb/references/sessions.md"]) {
      const text = read(path);
      expect(text).not.toMatch(/5[-–]15|2\+ others|at least 2 others|Every page links to/);
      expect(text).not.toContain("qmd_deep_search");
    }
  });

  it("explains portable runtime requirements and optional integration scope", () => {
    const text = read("README.md");
    expect(text).toContain("standalone");
    expect(text).toContain("bun install");
    expect(text).toContain("bun run src/cli.ts");
    expect(text).toContain("optional");
    expect(text).toContain("lazy");
    expect(text).not.toContain("Recent sessions**");
    expect(text).not.toContain("Novel insights get filed");
  });
});
