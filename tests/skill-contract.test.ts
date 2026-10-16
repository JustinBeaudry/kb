import { describe, it, expect } from "bun:test";
import { existsSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { parseFrontmatter } from "../src/lib/frontmatter";

const read = (path: string): string => readFileSync(path, "utf8");

describe("portable skill documentation contract", () => {
  it("uses a harness-neutral trigger and relative operation references", () => {
    const text = read("skills/kb/SKILL.md");
    const { data } = parseFrontmatter<Record<string, unknown>>(text);
    expect(data.name).toBe("kb");
    expect(data.description).toMatch(/questions|recall/);
    expect(data.description).not.toContain("Claude Code");
    for (const operation of ["query", "ingest", "maintenance", "sessions"]) {
      expect(text).toContain(`references/${operation}.md`);
      expect(existsSync(`skills/kb/references/${operation}.md`)).toBe(true);
    }
  });

  it("keeps every relative guide link resolvable from its containing file", () => {
    const paths = [
      "skills/kb/SKILL.md", "skills/extract/SKILL.md", "skills/refine/SKILL.md",
      ...["query", "ingest", "maintenance", "sessions"].map((name) => `skills/kb/references/${name}.md`),
      ...["query", "ingest", "lint", "refine", "extract"].map((name) => `commands/${name}.md`),
    ];
    for (const path of paths) {
      const links = [...read(path).matchAll(/\[[^\]]+\]\(([^)]+\.md)(?:#[^)]*)?\)/g)];
      for (const match of links) {
        const target = match[1]!;
        if (/^https?:/.test(target)) continue;
        expect(existsSync(resolve(dirname(path), target))).toBe(true);
      }
    }
  });

  it("routes optional slash commands to the shared operation guides", () => {
    const routes = {
      query: "query", ingest: "ingest", lint: "maintenance", refine: "maintenance", extract: "sessions",
    };
    for (const [command, guide] of Object.entries(routes)) {
      expect(read(`commands/${command}.md`)).toContain(`../skills/kb/references/${guide}.md`);
    }
  });

  it("documents the sanctioned session pipeline in the on-demand guide", () => {
    const text = read("skills/kb/references/sessions.md");
    for (const command of ["kb sessions --unprocessed", "kb summarize --json", "kb read-session summaries/", "kb mark-extracted"]) {
      expect(text).toContain(command);
    }
    expect(text).toContain("--approve");
    expect(text).toContain("untrusted data");
    expect(text).toContain("degraded: true");
    expect(text).toContain("already filed");
    expect(read("skills/extract/SKILL.md")).toContain("../kb/references/sessions.md");
    expect(read("skills/refine/SKILL.md")).toContain("../kb/references/maintenance.md");
  });

  it("leaves schema conventions and session layout in the vault template", () => {
    const text = read("templates/KB.md");
    for (const field of ["title", "type", "created", "updated", "tags"]) expect(text).toContain(field);
    for (const path of ["sessions/<name>.md", "sessions/summaries/<name>.md", "sessions/.trash/"]) expect(text).toContain(path);
    expect(text).toContain("kb capture-session");
    expect(text).toContain("[[kebab-filename|Display Title]]");
  });
});
