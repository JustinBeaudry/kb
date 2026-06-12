import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, writeFileSync, rmSync } from "node:fs";
import { buildTree } from "../src/lib/map/builder";
import { selectCandidates } from "../src/lib/map/candidates";

const vaults: string[] = [];
afterEach(() => {
  for (const v of vaults.splice(0)) {
    try {
      rmSync(v, { recursive: true, force: true });
    } catch {}
  }
});

function makeVault(): string {
  const dir = join(tmpdir(), `kb-cand-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "wiki"), { recursive: true });
  vaults.push(dir);
  return dir;
}

async function fixture() {
  const vault = makeVault();
  writeFileSync(
    join(vault, "wiki", "authentication.md"),
    [
      "---",
      "title: Authentication",
      "tags: [security]",
      "aliases: [Auth]",
      "---",
      "# Authentication",
      "## Token Setup",
      "Uses [[tokens]] and [[deploy]].",
    ].join("\n")
  );
  writeFileSync(join(vault, "wiki", "tokens.md"), "# Tokens\n## Rotation\nrotate\n");
  writeFileSync(join(vault, "wiki", "deploy.md"), "# Deploy\n## Steps\nmentions security basics\n");
  writeFileSync(
    join(vault, "wiki", "incident.md"),
    "# Incident Log\n## Past\nSee [[authentication]] for the authentication flow.\n"
  );
  const tree = await buildTree(vault);
  return { vault, tree };
}

describe("selectCandidates", () => {
  it("exact title match is the top candidate", async () => {
    const { vault, tree } = await fixture();
    const set = await selectCandidates(tree, "Authentication", { vaultPath: vault });
    expect(set.exact[0]).toBe("wiki/authentication.md");
  });

  it("exact alias match is case-insensitive", async () => {
    const { vault, tree } = await fixture();
    const set = await selectCandidates(tree, "auth", { vaultPath: vault });
    expect(set.exact).toContain("wiki/authentication.md");
  });

  it("tag match returns tagged pages", async () => {
    const { vault, tree } = await fixture();
    const set = await selectCandidates(tree, "security", { vaultPath: vault });
    expect(set.tagged).toContain("wiki/authentication.md");
  });

  it("heading substring match returns section node IDs", async () => {
    const { vault, tree } = await fixture();
    const set = await selectCandidates(tree, "rotation", { vaultPath: vault });
    expect(set.heading).toContain("wiki/tokens.md#rotation");
  });

  it("wikilink neighborhood includes pages linked from matches", async () => {
    const { vault, tree } = await fixture();
    const set = await selectCandidates(tree, "Authentication", { vaultPath: vault });
    expect(set.neighborhood).toContain("wiki/tokens.md");
    expect(set.neighborhood).toContain("wiki/deploy.md");
  });

  it("backlink bucket includes pages linking to matches", async () => {
    const { vault, tree } = await fixture();
    const set = await selectCandidates(tree, "Authentication", { vaultPath: vault });
    expect(set.backlink).toContain("wiki/incident.md");
  });

  it("lexical fallback finds body-only matches", async () => {
    const { vault, tree } = await fixture();
    const set = await selectCandidates(tree, "basics", { vaultPath: vault });
    expect(set.exact).toEqual([]);
    expect(set.lexical).toContain("wiki/deploy.md");
  });

  it("query matching nothing returns all-empty buckets", async () => {
    const { vault, tree } = await fixture();
    const set = await selectCandidates(tree, "zzzNOMATCHzzz", { vaultPath: vault });
    expect(set.exact).toEqual([]);
    expect(set.tagged).toEqual([]);
    expect(set.heading).toEqual([]);
    expect(set.neighborhood).toEqual([]);
    expect(set.backlink).toEqual([]);
    expect(set.lexical).toEqual([]);
  });

  it("caps total candidates at the limit, filling by priority", async () => {
    const vault = makeVault();
    for (let i = 0; i < 50; i++) {
      writeFileSync(join(vault, "wiki", `page-${String(i).padStart(2, "0")}.md`), `# Widget ${i}\n## Widget Heading ${i}\nwidget body\n`);
    }
    const tree = await buildTree(vault);
    const set = await selectCandidates(tree, "widget", { vaultPath: vault, limit: 10 });
    const total =
      set.exact.length +
      set.tagged.length +
      set.heading.length +
      set.neighborhood.length +
      set.backlink.length +
      set.lexical.length +
      (set.qmd?.length ?? 0);
    expect(total).toBe(10);
  });

  it("qmd hints land in their own bucket and never duplicate earlier buckets", async () => {
    const { vault, tree } = await fixture();
    const set = await selectCandidates(tree, "Authentication", {
      vaultPath: vault,
      qmdHints: ["wiki/deploy.md", "wiki/authentication.md", "wiki/unknown.md"],
    });
    // authentication.md already admitted via exact; deploy via neighborhood.
    expect(set.qmd).toEqual(["wiki/unknown.md"]);
  });

  it("absent qmd hints leave the bucket undefined", async () => {
    const { vault, tree } = await fixture();
    const set = await selectCandidates(tree, "Authentication", { vaultPath: vault });
    expect(set.qmd).toBeUndefined();
  });

  it("is deterministic for the same query and tree", async () => {
    const { vault, tree } = await fixture();
    const a = await selectCandidates(tree, "authentication", { vaultPath: vault });
    const b = await selectCandidates(tree, "authentication", { vaultPath: vault });
    expect(JSON.stringify(a)).toBe(JSON.stringify(b));
  });
});
