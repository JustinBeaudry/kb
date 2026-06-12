import { createHash } from "node:crypto";
import { existsSync, realpathSync, statSync } from "node:fs";
import { basename, join, relative, sep } from "node:path";
import { isAbsolute } from "node:path";
import { parseFrontmatter } from "../frontmatter";
import { parseHeadings, parseSections, parseWikilinks, slugify } from "../markdown";
import { assertGenuineScopeDir } from "../path-safety";
import { readWikiFileNoFollow, walkWiki, MAX_FILE_BYTES } from "../wiki-read";
import { makeSectionIdAssigner } from "./node-id";
import type { PageEntry, Section, SectionEntry, TreeCache } from "./types";

interface PageFrontmatter {
  title?: unknown;
  type?: unknown;
  tags?: unknown;
  aliases?: unknown;
}

function stringArray(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((v): v is string => typeof v === "string");
}

function toPageId(vaultPath: string, filePath: string): string {
  return relative(vaultPath, filePath).split(sep).join("/");
}

export async function buildTree(vaultPath: string): Promise<TreeCache> {
  const wikiDir = join(vaultPath, "wiki");
  if (existsSync(wikiDir)) {
    assertGenuineScopeDir(wikiDir, vaultPath);
  } else {
    return { schema_version: "1", pages: [], by_alias: {}, by_tag: {} };
  }
  const wikiReal = realpathSync(wikiDir);

  const files: string[] = [];
  walkWiki(wikiDir, wikiReal, (p) => files.push(p));
  files.sort();

  const pages: PageEntry[] = [];
  for (const file of files) {
    pages.push(buildPage(vaultPath, file));
  }

  const pageIds = new Set(pages.map((p) => p.id));

  const byAlias: Record<string, string> = {};
  for (const page of pages) {
    for (const alias of page.aliases) {
      if (byAlias[alias] !== undefined) {
        process.stderr.write(
          `warning: duplicate alias "${alias}" on ${page.id}; keeping ${byAlias[alias]}\n`
        );
        continue;
      }
      byAlias[alias] = page.id;
    }
  }

  // Resolve wikilink targets now that page IDs and aliases are known, then
  // compute backlinks in a second pass.
  const backlinks = new Map<string, Set<string>>();
  for (const page of pages) {
    const resolvedPage = new Set<string>();
    const unresolved = new Set<string>();
    const resolveTarget = (target: string): string | null => {
      const t = target.trim();
      if (t === "" || isAbsolute(t) || t.split("/").includes("..") || t.includes("\0")) {
        return null;
      }
      const candidates = t.endsWith(".md") ? [t, `wiki/${t}`] : [`wiki/${t}.md`];
      for (const c of candidates) {
        if (pageIds.has(c)) return c;
      }
      return byAlias[t] ?? null;
    };

    const resolveSection = (section: SectionEntry): void => {
      const sectionResolved = new Set<string>();
      for (const raw of section.wikilinks) {
        const resolved = resolveTarget(raw);
        if (resolved !== null && resolved !== page.id) {
          sectionResolved.add(resolved);
          resolvedPage.add(resolved);
        } else if (resolved === null) {
          unresolved.add(raw);
        }
      }
      section.wikilinks = [...sectionResolved].sort();
      for (const child of section.children) resolveSection(child);
    };
    for (const section of page.sections) resolveSection(section);

    page.wikilinks = [...resolvedPage].sort();
    page.unresolved_wikilinks = [...unresolved].sort();
    for (const target of page.wikilinks) {
      if (!backlinks.has(target)) backlinks.set(target, new Set());
      backlinks.get(target)!.add(page.id);
    }
  }
  for (const page of pages) {
    page.backlinks = [...(backlinks.get(page.id) ?? [])].sort();
  }

  const byTag: Record<string, string[]> = {};
  for (const page of pages) {
    for (const tag of page.tags) {
      (byTag[tag] ??= []).push(page.id);
    }
  }

  return {
    schema_version: "1",
    pages,
    by_alias: sortedRecord(byAlias),
    by_tag: sortedRecord(
      Object.fromEntries(Object.entries(byTag).map(([k, v]) => [k, [...v].sort()]))
    ),
  };
}

function sortedRecord<T>(record: Record<string, T>): Record<string, T> {
  return Object.fromEntries(Object.entries(record).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)));
}

function buildPage(vaultPath: string, filePath: string): PageEntry {
  const id = toPageId(vaultPath, filePath);
  const st = statSync(filePath);
  const fallbackTitle = basename(filePath, ".md");
  const base: PageEntry = {
    id,
    title: fallbackTitle,
    tags: [],
    aliases: [],
    content_hash: "",
    size: st.size,
    mtime_ms: Math.trunc(st.mtimeMs),
    sections: [],
    wikilinks: [],
    unresolved_wikilinks: [],
    backlinks: [],
  };

  const content = readWikiFileNoFollow(filePath);
  if (content === null) {
    const reason = st.size > MAX_FILE_BYTES ? `exceeds ${MAX_FILE_BYTES} bytes` : "unreadable";
    process.stderr.write(`warning: skipping body of ${id}: ${reason}\n`);
    return base;
  }
  base.content_hash = createHash("sha256").update(content, "utf-8").digest("hex");

  let fm: PageFrontmatter;
  let body: string;
  try {
    const parsed = parseFrontmatter<PageFrontmatter>(content);
    fm = parsed.data;
    body = parsed.body;
  } catch {
    process.stderr.write(`warning: malformed frontmatter in ${id}; skipping page body\n`);
    base.malformed = true;
    return base;
  }

  base.tags = stringArray(fm.tags).sort();
  base.aliases = stringArray(fm.aliases);
  if (typeof fm.type === "string") base.type = fm.type;

  const h1 = parseHeadings(body).find((h) => h.level === 1);
  base.title =
    typeof fm.title === "string" && fm.title !== "" ? fm.title : (h1?.text ?? fallbackTitle);

  // Section line ranges from the parser are body-relative; shift them so they
  // are file-absolute and usable as citation line ranges.
  const offset = content.split("\n").length - body.split("\n").length;
  const assignId = makeSectionIdAssigner(id);
  let position = 0;
  const rawLinks = parseWikilinks(body);
  const convert = (s: Section): SectionEntry => {
    position += 1;
    return {
      id: assignId(slugify(s.heading), position),
      heading: s.heading,
      level: s.level,
      line_range: [s.line_range[0] + offset, s.line_range[1] + offset],
      wikilinks: rawLinks
        .filter((l) => l.line >= s.line_range[0] && l.line <= s.line_range[1])
        .map((l) => l.target),
      children: s.children.map(convert),
    };
  };
  base.sections = parseSections(body).map(convert);
  return base;
}
