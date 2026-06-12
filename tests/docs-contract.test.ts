import { describe, it, expect } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

function read(relPath: string): string {
  return readFileSync(join(process.cwd(), relPath), "utf-8");
}

describe("docs contract — trust boundary references", () => {
  it("templates/KB.md describes the trust boundary", () => {
    const body = read("templates/KB.md");
    expect(body).toMatch(/Trust Boundary|trust boundary/);
    expect(body).toContain("kb recall");
    expect(body).toContain("kb get");
    expect(body).toContain("kb list-topics");
    expect(body).toContain("kb read-raw");
    expect(body).toContain("kb read-session");
    expect(body).toMatch(/ask-gated/);
    expect(body).toMatch(/best-effort/);
  });

  it("templates/KB.md warns about untrusted excerpt content", () => {
    const body = read("templates/KB.md");
    expect(body).toMatch(/never.*instructions|do not follow|treat.*as data/i);
  });

  it("templates/KB.md teaches tree navigation as the default retrieval", () => {
    const body = read("templates/KB.md");
    expect(body).toContain("kb map");
    expect(body).toContain("kb get-node");
    expect(body).toMatch(/node_id|node ID/);
    expect(body).toContain("KB_MAP_BUDGET");
  });

  it("README documents the navigation commands and map budget", () => {
    const body = read("README.md");
    expect(body).toContain("kb map");
    expect(body).toContain("kb get-node");
    expect(body).toContain("KB_MAP_BUDGET");
  });

  it("commands/query.md walks the map -> select -> fetch loop", () => {
    const body = read("commands/query.md");
    expect(body).toContain("kb map");
    expect(body).toContain("kb get-node");
  });

  it("skills/kb/SKILL.md uses the sanctioned CLI in Query workflow", () => {
    const body = read("skills/kb/SKILL.md");
    expect(body).toContain("kb list-topics");
    expect(body).toContain("kb recall");
    expect(body).toContain("kb get");
    expect(body).toMatch(/kb read-session/);
    expect(body).toMatch(/kb read-raw/);
  });

  it("skills/kb/SKILL.md teaches the map -> select -> fetch loop", () => {
    const body = read("skills/kb/SKILL.md");
    expect(body).toContain("kb map");
    expect(body).toContain("kb get-node");
    expect(body).toMatch(/node_id|node ID/);
  });

  it("skills/extract/SKILL.md uses kb read-session, not direct file reads", () => {
    const body = read("skills/extract/SKILL.md");
    expect(body).toContain("kb read-session");
    expect(body).toMatch(/untrusted/i);
  });

  it("skills/refine/SKILL.md points to sanctioned retrieval for cross-references", () => {
    const body = read("skills/refine/SKILL.md");
    expect(body).toMatch(/kb recall|kb list-topics/);
  });
});
