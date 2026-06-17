import { createHash } from "node:crypto";
import { existsSync, realpathSync, statSync } from "node:fs";
import { basename, join, relative, sep } from "node:path";
import { parseFrontmatter } from "../frontmatter";
import { parseSections, parseWikilinks, slugify } from "../markdown";
import { assertGenuineScopeDir, assertSafeFilename } from "../path-safety";
import { readWikiFileNoFollow, walkWiki, MAX_FILE_BYTES } from "../wiki-read";
import { makeSectionIdAssigner } from "./node-id";
import { walkSections } from "./traverse";
import { CACHE_SCHEMA_VERSION, type PageEntry, type Section, type SectionEntry, type TreeCache } from "./types";

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

export function toPageId(vaultPath: string, filePath: string): string {
  return relative(vaultPath, filePath).split(sep).join("/");
}

export function listWikiFiles(vaultPath: string): string[] {
  const wikiDir = join(vaultPath, "wiki");
  if (!existsSync(wikiDir)) return [];
  assertGenuineScopeDir(wikiDir, vaultPath);
  const wikiReal = realpathSync(wikiDir);
  const files: string[] = [];
  walkWiki(wikiDir, wikiReal, (p) => files.push(p));
  return files.sort();
}

export async function buildTree(vaultPath: string): Promise<TreeCache> {
  const pages = listWikiFiles(vaultPath)
    .map((file) => buildPage(vaultPath, file))
    .filter((p): p is PageEntry => p !== null);
  return linkTree(pages);
}

/**
 * Resolve a raw wikilink target against the tree's page IDs and aliases.
 * Returns the page ID, or null when the target is unsafe or unknown. Section
 * wikilinks store raw targets, so resolution stays recomputable from cached
 * pages as the vault changes.
 */
export function resolveTarget(
  pageIds: Set<string>,
  byAlias: Record<string, string>,
  target: string
): string | null {
  const t = target.trim();
  if (t === "") return null;
  try {
    assertSafeFilename(t);
  } catch {
    return null;
  }
  const candidates = t.endsWith(".md") ? [t, `wiki/${t}`] : [`wiki/${t}.md`];
  for (const c of candidates) {
    if (pageIds.has(c)) return c;
  }
  return byAlias[t] ?? null;
}

/**
 * Resolve a section's raw wikilink targets to page IDs, excluding self-links
 * and unresolvable targets — the same pipeline linkTree runs page-wide.
 */
export function resolveSectionLinks(
  pageIds: Set<string>,
  byAlias: Record<string, string>,
  pageId: string,
  section: SectionEntry
): string[] {
  const out: string[] = [];
  for (const raw of section.wikilinks) {
    const resolved = resolveTarget(pageIds, byAlias, raw);
    if (resolved !== null && resolved !== pageId && !out.includes(resolved)) {
      out.push(resolved);
    }
  }
  return out;
}

/**
 * Compute the cross-page layer — alias/tag indexes, resolved page wikilinks,
 * unresolved targets, backlinks — from per-page parses. Pure recompute: it
 * reads only raw section targets, so it is idempotent over cached pages.
 */
export function linkTree(pages: PageEntry[]): TreeCache {
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

  const backlinks = new Map<string, Set<string>>();
  for (const page of pages) {
    const resolvedPage = new Set<string>();
    const unresolved = new Set<string>();
    const collect = (raw: string): void => {
      const resolved = resolveTarget(pageIds, byAlias, raw);
      if (resolved !== null && resolved !== page.id) {
        resolvedPage.add(resolved);
      } else if (resolved === null) {
        unresolved.add(raw);
      }
    };
    for (const raw of page.preamble_wikilinks ?? []) collect(raw);
    walkSections(page, (section) => {
      for (const raw of section.wikilinks) collect(raw);
    });

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
    schema_version: CACHE_SCHEMA_VERSION,
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

export function buildPage(vaultPath: string, filePath: string): PageEntry | null {
  const id = toPageId(vaultPath, filePath);
  let st: ReturnType<typeof statSync>;
  try {
    st = statSync(filePath);
  } catch (err) {
    // Deleted between directory walk and stat — treat as removed, not fatal.
    if ((err as NodeJS.ErrnoException).code === "ENOENT") return null;
    throw err;
  }
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
    // A null read means oversize/unreadable OR the file was deleted between
    // the stat above and this read. Re-check existence: a now-missing file is
    // "removed" (return null, matching the stat-time ENOENT branch) rather
    // than a phantom page with empty sections.
    if (!existsSync(filePath)) return null;
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

  const parsed = parseSections(body);
  // H1 can never nest (level 1 is the minimum), so the first level-1 entry in
  // the top-level section list is the first H1 of the body.
  const h1 = parsed.find((s) => s.level === 1);
  base.title =
    typeof fm.title === "string" && fm.title !== "" ? fm.title : (h1?.heading ?? fallbackTitle);

  // Section line ranges from the parser are body-relative; shift them so they
  // are file-absolute and usable as citation line ranges.
  const offset = content.split("\n").length - body.split("\n").length;
  const assignId = makeSectionIdAssigner(id);
  let position = 0;
  const convert = (s: Section): SectionEntry => {
    position += 1;
    return {
      id: assignId(slugify(s.heading), position),
      heading: s.heading,
      level: s.level,
      line_range: [s.line_range[0] + offset, s.line_range[1] + offset],
      wikilinks: s.wikilinks,
      children: s.children.map(convert),
    };
  };
  base.sections = parsed.map(convert);

  // Wikilinks before the first heading (or anywhere on a heading-free page)
  // belong to no section; capture them so linkTree still resolves them.
  const firstHeadingLine = parsed[0]?.line_range[0];
  const preamble = parseWikilinks(body)
    .filter((l) => firstHeadingLine === undefined || l.line < firstHeadingLine)
    .map((l) => l.target);
  if (preamble.length > 0) base.preamble_wikilinks = preamble;
  return base;
}
