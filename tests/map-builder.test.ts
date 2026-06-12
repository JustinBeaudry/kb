import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, writeFileSync, rmSync, symlinkSync } from "node:fs";
import { buildPage, buildTree } from "../src/lib/map/builder";
import { isValidNodeId, parseNodeId } from "../src/lib/map/node-id";

const vaults: string[] = [];
afterEach(() => {
  for (const v of vaults.splice(0)) {
    try {
      rmSync(v, { recursive: true, force: true });
    } catch {}
  }
});

function makeVault(): string {
  const dir = join(tmpdir(), `kb-map-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "wiki"), { recursive: true });
  mkdirSync(join(dir, ".kb"), { recursive: true });
  vaults.push(dir);
  return dir;
}

function fixtureVault(): string {
  const vault = makeVault();
  writeFileSync(
    join(vault, "wiki", "auth.md"),
    [
      "---",
      "title: Authentication",
      "tags: [security, identity]",
      "aliases: [Auth, Login Flow]",
      "type: concept",
      "---",
      "# Authentication",
      "",
      "## Overview",
      "We use OAuth2. See [[deploy]] for rollout.",
      "",
      "## Setup",
      "Steps here. Related: [[Tokens]]",
      "",
    ].join("\n")
  );
  writeFileSync(
    join(vault, "wiki", "deploy.md"),
    ["# Deploy Runbook", "", "## Steps", "Staging first. Back to [[auth]].", ""].join("\n")
  );
  writeFileSync(
    join(vault, "wiki", "tokens.md"),
    ["---", "aliases: [Tokens]", "---", "# Token Lifetimes", "", "## Rotation", "Rotate often.", ""].join("\n")
  );
  return vault;
}

describe("buildTree", () => {
  it("builds pages with node IDs, resolved wikilinks, and backlinks", async () => {
    const vault = fixtureVault();
    const tree = await buildTree(vault);
    expect(tree.pages.length).toBe(3);
    expect(tree.pages.map((p) => p.id)).toEqual(["wiki/auth.md", "wiki/deploy.md", "wiki/tokens.md"]);

    const auth = tree.pages[0]!;
    expect(auth.title).toBe("Authentication");
    expect(auth.tags).toEqual(["identity", "security"]);
    expect(auth.aliases).toEqual(["Auth", "Login Flow"]);
    expect(auth.type).toBe("concept");
    expect(auth.content_hash).toMatch(/^[0-9a-f]{64}$/);

    const collectIds = (s: { id: string; children: unknown[] }): string[] => [
      s.id,
      ...(s.children as { id: string; children: unknown[] }[]).flatMap(collectIds),
    ];
    const sectionIds = auth.sections.flatMap(collectIds);
    expect(sectionIds).toContain("wiki/auth.md#authentication");
    expect(sectionIds).toContain("wiki/auth.md#overview");
    expect(sectionIds).toContain("wiki/auth.md#setup");

    // [[deploy]] resolves to wiki/deploy.md; [[Tokens]] resolves via alias.
    const deploy = tree.pages[1]!;
    const tokens = tree.pages[2]!;
    expect(deploy.backlinks).toContain("wiki/auth.md");
    expect(tokens.backlinks).toContain("wiki/auth.md");
    expect(auth.backlinks).toContain("wiki/deploy.md");
  });

  it("section line ranges are file-absolute (frontmatter offset applied)", async () => {
    const vault = fixtureVault();
    const tree = await buildTree(vault);
    const auth = tree.pages[0]!;
    // "# Authentication" is line 7 of the file (after 6 frontmatter lines).
    expect(auth.sections[0]!.line_range[0]).toBe(7);
  });

  it("populates by_alias and by_tag indexes with sorted keys", async () => {
    const vault = fixtureVault();
    const tree = await buildTree(vault);
    expect(tree.by_alias["Auth"]).toBe("wiki/auth.md");
    expect(tree.by_alias["Tokens"]).toBe("wiki/tokens.md");
    expect(tree.by_tag["security"]).toEqual(["wiki/auth.md"]);
    expect(Object.keys(tree.by_alias)).toEqual([...Object.keys(tree.by_alias)].sort());
  });

  it("page with no frontmatter gets filename title and empty tags/aliases", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "bare.md"), "just text, no headings\n");
    const tree = await buildTree(vault);
    expect(tree.pages[0]!.title).toBe("bare");
    expect(tree.pages[0]!.tags).toEqual([]);
    expect(tree.pages[0]!.aliases).toEqual([]);
  });

  it("title falls back to first H1 when frontmatter has no title", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "h1.md"), "# From Heading\n\nbody\n");
    const tree = await buildTree(vault);
    expect(tree.pages[0]!.title).toBe("From Heading");
  });

  it("duplicate headings get ordinal-suffixed section IDs", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "dup.md"), "## Setup\na\n## Setup\nb\n## Setup\nc\n");
    const tree = await buildTree(vault);
    const ids = tree.pages[0]!.sections.map((s) => s.id);
    expect(ids).toEqual(["wiki/dup.md#setup", "wiki/dup.md#setup-2", "wiki/dup.md#setup-3"]);
  });

  it("ordinal suffix never collides with a natural '-2' slug", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "col.md"), "## Setup\na\n## Setup\nb\n## Setup 2\nc\n");
    const tree = await buildTree(vault);
    const ids = tree.pages[0]!.sections.map((s) => s.id);
    expect(new Set(ids).size).toBe(3);
    expect(ids).toEqual(["wiki/col.md#setup", "wiki/col.md#setup-2", "wiki/col.md#setup-2-2"]);
  });

  it("natural '-2' slug arriving first still yields unique IDs", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "col2.md"), "## Setup 2\na\n## Setup\nb\n## Setup\nc\n");
    const tree = await buildTree(vault);
    const ids = tree.pages[0]!.sections.map((s) => s.id);
    expect(new Set(ids).size).toBe(3);
    expect(ids).toEqual(["wiki/col2.md#setup-2", "wiki/col2.md#setup", "wiki/col2.md#setup-3"]);
  });

  it("positional fallback never collides with a literal section-N heading", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "pos.md"), "## ???\na\n## section-1\nb\n");
    const tree = await buildTree(vault);
    const ids = tree.pages[0]!.sections.map((s) => s.id);
    expect(new Set(ids).size).toBe(2);
    expect(ids[0]).toBe("wiki/pos.md#section-1");
    expect(ids[1]).toBe("wiki/pos.md#section-1-2");
  });

  it("heading that slugifies to empty falls back to positional ID", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "odd.md"), "## ???\ntext\n");
    const tree = await buildTree(vault);
    expect(tree.pages[0]!.sections[0]!.id).toBe("wiki/odd.md#section-1");
  });

  it("malformed frontmatter records page as malformed without throwing", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "bad.md"), "---\ntags: [unclosed\n---\nbody\n");
    const tree = await buildTree(vault);
    expect(tree.pages.length).toBe(1);
    expect(tree.pages[0]!.malformed).toBe(true);
    expect(tree.pages[0]!.sections).toEqual([]);
  });

  it("symlinks inside wiki/ are excluded", async () => {
    const vault = fixtureVault();
    writeFileSync(join(vault, "outside.md"), "# Outside\n");
    symlinkSync(join(vault, "outside.md"), join(vault, "wiki", "linked.md"));
    const tree = await buildTree(vault);
    expect(tree.pages.map((p) => p.id)).not.toContain("wiki/linked.md");
  });

  it("buildPage on a file deleted mid-scan returns null instead of throwing", async () => {
    const vault = makeVault();
    expect(buildPage(vault, join(vault, "wiki", "ghost.md"))).toBeNull();
  });

  it("empty wiki returns empty tree", async () => {
    const vault = makeVault();
    const tree = await buildTree(vault);
    expect(tree.pages).toEqual([]);
    expect(tree.by_alias).toEqual({});
    expect(tree.by_tag).toEqual({});
  });

  it("traversal-shaped and absolute wikilink targets stay unresolved", async () => {
    const vault = makeVault();
    writeFileSync(
      join(vault, "wiki", "hostile.md"),
      "## Links\n[[../etc/passwd]] and [[/abs/path]] and [[nonexistent]]\n"
    );
    const tree = await buildTree(vault);
    const page = tree.pages[0]!;
    expect(page.unresolved_wikilinks).toContain("../etc/passwd");
    expect(page.unresolved_wikilinks).toContain("/abs/path");
    expect(page.unresolved_wikilinks).toContain("nonexistent");
    // Sections keep raw targets; resolution is recomputed by linkTree.
    expect(page.sections[0]!.wikilinks).toEqual(["../etc/passwd", "/abs/path", "nonexistent"]);
    expect(page.wikilinks).toEqual([]);
  });

  it("heading-free pages contribute their wikilinks to the graph", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "stub.md"), "just a pointer to [[target]]\n");
    writeFileSync(join(vault, "wiki", "target.md"), "# Target\nbody\n");
    const tree = await buildTree(vault);
    const stub = tree.pages.find((p) => p.id === "wiki/stub.md")!;
    const target = tree.pages.find((p) => p.id === "wiki/target.md")!;
    expect(stub.wikilinks).toEqual(["wiki/target.md"]);
    expect(target.backlinks).toEqual(["wiki/stub.md"]);
  });

  it("preamble wikilinks before the first heading resolve without duplicating section links", async () => {
    const vault = makeVault();
    writeFileSync(
      join(vault, "wiki", "pre.md"),
      "preamble link [[target]] and [[ghost-page]]\n# Pre\nbody [[target]]\n"
    );
    writeFileSync(join(vault, "wiki", "target.md"), "# Target\nbody\n");
    const tree = await buildTree(vault);
    const pre = tree.pages.find((p) => p.id === "wiki/pre.md")!;
    expect(pre.wikilinks).toEqual(["wiki/target.md"]);
    expect(pre.unresolved_wikilinks).toContain("ghost-page");
  });

  it("duplicate alias across pages: first page in path order wins", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "a.md"), "---\naliases: [Shared]\n---\n# A\n");
    writeFileSync(join(vault, "wiki", "b.md"), "---\naliases: [Shared]\n---\n# B\n");
    const tree = await buildTree(vault);
    expect(tree.by_alias["Shared"]).toBe("wiki/a.md");
  });

  it("is deterministic: same vault builds JSON-equal output", async () => {
    const vault = fixtureVault();
    const a = await buildTree(vault);
    const b = await buildTree(vault);
    expect(JSON.stringify(a)).toBe(JSON.stringify(b));
  });
});

describe("node-id grammar", () => {
  it("accepts page IDs and section IDs", () => {
    expect(isValidNodeId("wiki/foo.md")).toBe(true);
    expect(isValidNodeId("wiki/sub/foo.md")).toBe(true);
    expect(isValidNodeId("wiki/foo.md#installation")).toBe(true);
    expect(isValidNodeId("wiki/foo.md#setup-2")).toBe(true);
    expect(isValidNodeId("wiki/foo.md#section-1")).toBe(true);
  });

  it("rejects traversal, absolute paths, and non-wiki scopes", () => {
    expect(isValidNodeId("wiki/../etc/passwd")).toBe(false);
    expect(isValidNodeId("/etc/passwd")).toBe(false);
    expect(isValidNodeId("raw/foo.md")).toBe(false);
    expect(isValidNodeId("sessions/x.md")).toBe(false);
    expect(isValidNodeId("wiki/foo.md#Bad Slug")).toBe(false);
    expect(isValidNodeId("wiki/foo.md#")).toBe(false);
    expect(isValidNodeId("wiki/foo.txt")).toBe(false);
    expect(isValidNodeId("wiki/foo.md\0")).toBe(false);
  });

  it("parses page and section parts", () => {
    expect(parseNodeId("wiki/foo.md")).toEqual({ page: "wiki/foo.md" });
    expect(parseNodeId("wiki/foo.md#setup-2")).toEqual({ page: "wiki/foo.md", section: "setup-2" });
  });
});
