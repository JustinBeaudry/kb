import { describe, it, expect } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

function read(relPath: string): string {
  return readFileSync(join(process.cwd(), relPath), "utf-8");
}

describe("docs contract — trust boundary references", () => {
  it("templates/CAIRN.md describes the trust boundary", () => {
    const body = read("templates/CAIRN.md");
    expect(body).toMatch(/Trust Boundary|trust boundary/);
    expect(body).toContain("cairn recall");
    expect(body).toContain("cairn get");
    expect(body).toContain("cairn list-topics");
    expect(body).toContain("cairn read-raw");
    expect(body).toContain("cairn read-session");
    expect(body).toMatch(/ask-gated/);
    expect(body).toMatch(/best-effort/);
  });

  it("templates/CAIRN.md warns about untrusted excerpt content", () => {
    const body = read("templates/CAIRN.md");
    expect(body).toMatch(/never.*instructions|do not follow|treat.*as data/i);
  });

  it("skills/cairn/SKILL.md uses the sanctioned CLI in Query workflow", () => {
    const body = read("skills/cairn/SKILL.md");
    expect(body).toContain("cairn list-topics");
    expect(body).toContain("cairn recall");
    expect(body).toContain("cairn get");
    expect(body).toMatch(/cairn read-session/);
    expect(body).toMatch(/cairn read-raw/);
  });

  it("skills/extract/SKILL.md uses cairn read-session, not direct file reads", () => {
    const body = read("skills/extract/SKILL.md");
    expect(body).toContain("cairn read-session");
    expect(body).toMatch(/untrusted/i);
  });

  it("skills/refine/SKILL.md points to sanctioned retrieval for cross-references", () => {
    const body = read("skills/refine/SKILL.md");
    expect(body).toMatch(/cairn recall|cairn list-topics/);
  });
});
