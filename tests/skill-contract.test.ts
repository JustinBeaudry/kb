import { describe, it, expect } from "bun:test";
import { readFileSync } from "node:fs";

function read(path: string): string {
  return readFileSync(path, "utf-8");
}

describe("session manifest skill contract", () => {
  it("extract uses ask-gated cairn read-session for unprocessed sessions", () => {
    const text = read("skills/extract/SKILL.md");
    expect(text).toContain("cairn read-session");
    expect(text).toContain("--approve");
    expect(text).toContain("untrusted data");
  });

  it("refine documents session manifest summarization when session context is needed", () => {
    const text = read("skills/refine/SKILL.md");
    expect(text).toContain("sessions/*.md");
    expect(text).toContain("cairn summarize --json");
    expect(text).toContain("degraded: true");
  });

  it("cairn overview describes the new session layout", () => {
    const text = read("skills/cairn/SKILL.md");
    expect(text).toContain("sessions/<name>.md");
    expect(text).toContain("sessions/summaries/<name>.md");
    expect(text).toContain("sessions/.trash/");
  });

  it("query command documents files_changed manifest scans", () => {
    const text = read("commands/query.md");
    expect(text).toContain("files_changed");
    expect(text).toContain("sessions/*.md");
    expect(text).toContain("Do not invoke `cairn summarize`");
  });

  it("template CAIRN.md includes summaries and trash rows", () => {
    const text = read("templates/CAIRN.md");
    expect(text).toContain("sessions/summaries/<name>.md");
    expect(text).toContain("sessions/.trash/");
    expect(text).toContain("cairn capture-session");
  });
});
