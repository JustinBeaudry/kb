import { describe, it, expect } from "bun:test";
import { readFileSync } from "node:fs";

function read(path: string): string {
  return readFileSync(path, "utf-8");
}

describe("session manifest skill contract", () => {
  it("extract enumerates, summarizes, retrieves, and marks via the sanctioned CLI", () => {
    const text = read("skills/extract/SKILL.md");
    expect(text).toContain("kb sessions --unprocessed");
    expect(text).toContain("kb summarize --json");
    expect(text).toContain("kb read-session");
    expect(text).toContain("--approve");
    expect(text).toContain("kb mark-extracted");
    expect(text).toContain("untrusted data");
    expect(text).toContain("KB_SUMMARIZE_COMMAND");
    // stale mechanism: candidates are not in manifest bodies
    expect(text).not.toContain("visible in the chunk text");
  });

  it("refine retrieves summaries through ask-gated read-session, not direct Reads", () => {
    const text = read("skills/refine/SKILL.md");
    expect(text).toContain("sessions/*.md");
    expect(text).toContain("kb summarize --json");
    expect(text).toContain("kb read-session summaries/");
    expect(text).toContain("degraded: true");
    expect(text).not.toContain("read the returned `path`");
  });

  it("kb overview describes the new session layout", () => {
    const text = read("skills/kb/SKILL.md");
    expect(text).toContain("sessions/<name>.md");
    expect(text).toContain("sessions/summaries/<name>.md");
    expect(text).toContain("sessions/.trash/");
  });

  it("query command does not teach denied session scans", () => {
    const text = read("commands/query.md");
    expect(text).not.toContain("files_changed");
    expect(text).not.toContain("sessions/*.md");
    expect(text).not.toMatch(/use Grep/i);
  });

  it("extract command documents the sanctioned pipeline", () => {
    const text = read("commands/extract.md");
    expect(text).toContain("kb sessions --unprocessed");
    expect(text).toContain("kb mark-extracted");
  });

  it("template KB.md includes summaries and trash rows", () => {
    const text = read("templates/KB.md");
    expect(text).toContain("sessions/summaries/<name>.md");
    expect(text).toContain("sessions/.trash/");
    expect(text).toContain("kb capture-session");
  });

  it("migration-quarantine wording is gone everywhere", () => {
    for (const path of ["README.md", "templates/KB.md", "skills/kb/SKILL.md"]) {
      expect(read(path)).not.toContain("Migration quarantine");
    }
  });
});
