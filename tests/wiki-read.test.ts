import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, writeFileSync, rmSync, symlinkSync, realpathSync } from "node:fs";
import { walkWiki, readWikiFileNoFollow, MAX_FILE_BYTES } from "../src/lib/wiki-read";

const dirs: string[] = [];
afterEach(() => {
  for (const d of dirs.splice(0)) {
    try {
      rmSync(d, { recursive: true, force: true });
    } catch {}
  }
});

function makeWiki(): { vault: string; wikiDir: string; wikiReal: string } {
  const vault = join(tmpdir(), `kb-wiki-read-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  const wikiDir = join(vault, "wiki");
  mkdirSync(wikiDir, { recursive: true });
  dirs.push(vault);
  return { vault, wikiDir, wikiReal: realpathSync(wikiDir) };
}

function collect(wikiDir: string, wikiReal: string): string[] {
  const found: string[] = [];
  walkWiki(wikiDir, wikiReal, (p) => found.push(p));
  return found.sort();
}

describe("walkWiki", () => {
  it("returns only .md files, recursing subdirectories", () => {
    const { wikiDir, wikiReal } = makeWiki();
    writeFileSync(join(wikiDir, "a.md"), "# A\n");
    writeFileSync(join(wikiDir, "notes.txt"), "not markdown\n");
    mkdirSync(join(wikiDir, "sub"), { recursive: true });
    writeFileSync(join(wikiDir, "sub", "b.md"), "# B\n");
    const found = collect(wikiDir, wikiReal);
    expect(found).toEqual([join(wikiDir, "a.md"), join(wikiDir, "sub", "b.md")]);
  });

  it("excludes symlinked files and symlinked directories", () => {
    const { vault, wikiDir, wikiReal } = makeWiki();
    writeFileSync(join(vault, "outside.md"), "# Outside\n");
    mkdirSync(join(vault, "outside-dir"), { recursive: true });
    writeFileSync(join(vault, "outside-dir", "c.md"), "# C\n");
    symlinkSync(join(vault, "outside.md"), join(wikiDir, "linked.md"));
    symlinkSync(join(vault, "outside-dir"), join(wikiDir, "linked-dir"));
    writeFileSync(join(wikiDir, "real.md"), "# Real\n");
    const found = collect(wikiDir, wikiReal);
    expect(found).toEqual([join(wikiDir, "real.md")]);
  });

  it("skips dotfiles and dot-directories", () => {
    const { wikiDir, wikiReal } = makeWiki();
    writeFileSync(join(wikiDir, ".hidden.md"), "# Hidden\n");
    mkdirSync(join(wikiDir, ".obsidian"), { recursive: true });
    writeFileSync(join(wikiDir, ".obsidian", "d.md"), "# D\n");
    writeFileSync(join(wikiDir, "visible.md"), "# Visible\n");
    const found = collect(wikiDir, wikiReal);
    expect(found).toEqual([join(wikiDir, "visible.md")]);
  });
});

describe("readWikiFileNoFollow", () => {
  it("reads a regular file's contents", () => {
    const { wikiDir } = makeWiki();
    const p = join(wikiDir, "a.md");
    writeFileSync(p, "# A\nbody\n");
    expect(readWikiFileNoFollow(p)).toBe("# A\nbody\n");
  });

  it("returns null for symlinks (O_NOFOLLOW)", () => {
    const { vault, wikiDir } = makeWiki();
    writeFileSync(join(vault, "target.md"), "secret\n");
    const link = join(wikiDir, "link.md");
    symlinkSync(join(vault, "target.md"), link);
    expect(readWikiFileNoFollow(link)).toBeNull();
  });

  it("returns null for files over MAX_FILE_BYTES", () => {
    const { wikiDir } = makeWiki();
    const p = join(wikiDir, "big.md");
    writeFileSync(p, "x".repeat(MAX_FILE_BYTES + 1));
    expect(readWikiFileNoFollow(p)).toBeNull();
  });

  it("returns null for missing files", () => {
    const { wikiDir } = makeWiki();
    expect(readWikiFileNoFollow(join(wikiDir, "nope.md"))).toBeNull();
  });
});
