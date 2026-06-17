import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { existsSync, mkdirSync, readFileSync, rmSync, statSync, utimesSync, writeFileSync } from "node:fs";
import { loadOrBuildTree, invalidateTree } from "../src/lib/map/cache";

const vaults: string[] = [];
afterEach(() => {
  for (const v of vaults.splice(0)) {
    try {
      rmSync(v, { recursive: true, force: true });
    } catch {}
  }
});

function makeVault(): string {
  const dir = join(tmpdir(), `kb-cache-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(join(dir, "wiki"), { recursive: true });
  mkdirSync(join(dir, ".kb"), { recursive: true });
  writeFileSync(join(dir, "wiki", "a.md"), "# A\n\n## One\nlinks to [[b]]\n");
  writeFileSync(join(dir, "wiki", "b.md"), "# B\n\n## Two\ncontent\n");
  vaults.push(dir);
  return dir;
}

const treePath = (vault: string) => join(vault, ".kb", "index", "tree.json");

describe("loadOrBuildTree", () => {
  it("cold start builds, persists, and returns the tree", async () => {
    const vault = makeVault();
    const tree = await loadOrBuildTree(vault);
    expect(tree.pages.map((p) => p.id)).toEqual(["wiki/a.md", "wiki/b.md"]);
    expect(existsSync(treePath(vault))).toBe(true);
    const onDisk = JSON.parse(readFileSync(treePath(vault), "utf-8"));
    expect(onDisk.schema_version).toBe("2");
    expect(typeof onDisk.built_at).toBe("string");
  });

  it("warm load with no changes does not rewrite the cache file", async () => {
    const vault = makeVault();
    await loadOrBuildTree(vault);
    const before = statSync(treePath(vault)).mtimeMs;
    const tree = await loadOrBuildTree(vault);
    expect(tree.pages.length).toBe(2);
    expect(statSync(treePath(vault)).mtimeMs).toBe(before);
  });

  it("reparses only changed pages and recomputes backlinks", async () => {
    const vault = makeVault();
    const first = await loadOrBuildTree(vault);
    expect(first.pages.find((p) => p.id === "wiki/b.md")!.backlinks).toEqual(["wiki/a.md"]);
    // Edit a.md: drop the wikilink (different size → stat detects change).
    writeFileSync(join(vault, "wiki", "a.md"), "# A Updated\n\n## One\nno links now at all\n");
    const second = await loadOrBuildTree(vault);
    const a = second.pages.find((p) => p.id === "wiki/a.md")!;
    const b = second.pages.find((p) => p.id === "wiki/b.md")!;
    expect(a.title).toBe("A Updated");
    expect(b.backlinks).toEqual([]);
    expect(a.content_hash).not.toBe(first.pages.find((p) => p.id === "wiki/a.md")!.content_hash);
    // Unchanged page entry is reused verbatim (same hash).
    expect(b.content_hash).toBe(first.pages.find((p) => p.id === "wiki/b.md")!.content_hash);
  });

  it("detects added and removed pages", async () => {
    const vault = makeVault();
    await loadOrBuildTree(vault);
    writeFileSync(join(vault, "wiki", "c.md"), "# C\nlinks [[a]]\n");
    rmSync(join(vault, "wiki", "b.md"));
    const tree = await loadOrBuildTree(vault);
    expect(tree.pages.map((p) => p.id)).toEqual(["wiki/a.md", "wiki/c.md"]);
    const a = tree.pages.find((p) => p.id === "wiki/a.md")!;
    expect(a.backlinks).toEqual(["wiki/c.md"]);
    // a.md's link to deleted b.md is now unresolved.
    expect(a.unresolved_wikilinks).toContain("b");
  });

  it("rebuilds from scratch on corrupted cache JSON", async () => {
    const vault = makeVault();
    await loadOrBuildTree(vault);
    writeFileSync(treePath(vault), "{ not json");
    const tree = await loadOrBuildTree(vault);
    expect(tree.pages.length).toBe(2);
    expect(JSON.parse(readFileSync(treePath(vault), "utf-8")).schema_version).toBe("2");
  });

  it("rebuilds from scratch on unknown cache schema_version", async () => {
    const vault = makeVault();
    await loadOrBuildTree(vault);
    const raw = JSON.parse(readFileSync(treePath(vault), "utf-8"));
    raw.schema_version = "99";
    writeFileSync(treePath(vault), JSON.stringify(raw));
    const tree = await loadOrBuildTree(vault);
    expect(tree.pages.length).toBe(2);
  });

  it("pre-upgrade v1 caches trigger a clean rebuild that carries preamble links", async () => {
    const vault = makeVault();
    writeFileSync(join(vault, "wiki", "stub.md"), "pointer to [[a]]\n");
    await loadOrBuildTree(vault);
    const raw = JSON.parse(readFileSync(treePath(vault), "utf-8"));
    expect(raw.schema_version).toBe("2");
    // Simulate a cache written by the previous release.
    raw.schema_version = "1";
    for (const p of raw.pages) delete p.preamble_wikilinks;
    writeFileSync(treePath(vault), JSON.stringify(raw));
    const tree = await loadOrBuildTree(vault);
    const stub = tree.pages.find((p) => p.id === "wiki/stub.md")!;
    expect(stub.wikilinks).toEqual(["wiki/a.md"]);
  });

  it("ignores leftover tmp files from interrupted writes", async () => {
    const vault = makeVault();
    await loadOrBuildTree(vault);
    writeFileSync(join(vault, ".kb", "index", "tree.json.tmp-999-1"), "{ half-written");
    const tree = await loadOrBuildTree(vault);
    expect(tree.pages.length).toBe(2);
  });

  it("concurrent cold loads both return valid trees and leave a valid file", async () => {
    const vault = makeVault();
    const [a, b] = await Promise.all([loadOrBuildTree(vault), loadOrBuildTree(vault)]);
    expect(a.pages.length).toBe(2);
    expect(b.pages.length).toBe(2);
    const onDisk = JSON.parse(readFileSync(treePath(vault), "utf-8"));
    expect(onDisk.pages.length).toBe(2);
  });

  it("same-size touch-back edits are missed by design (documented limitation)", async () => {
    const vault = makeVault();
    const first = await loadOrBuildTree(vault);
    const p = join(vault, "wiki", "b.md");
    const st = statSync(p);
    const original = readFileSync(p, "utf-8");
    const swapped = original.replace("content", "tampere"); // same byte length
    writeFileSync(p, swapped);
    utimesSync(p, st.atime, st.mtime);
    const second = await loadOrBuildTree(vault);
    expect(second.pages.find((pg) => pg.id === "wiki/b.md")!.content_hash).toBe(
      first.pages.find((pg) => pg.id === "wiki/b.md")!.content_hash
    );
    // invalidateTree is the manual escape hatch.
    invalidateTree(vault);
    const third = await loadOrBuildTree(vault);
    expect(third.pages.find((pg) => pg.id === "wiki/b.md")!.content_hash).not.toBe(
      first.pages.find((pg) => pg.id === "wiki/b.md")!.content_hash
    );
  });
});
