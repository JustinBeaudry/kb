import { join } from "node:path";
import { readWikiFileNoFollow } from "../wiki-read";
import { pagesById, walkSections } from "./traverse";
import type { TreeCache } from "./types";

export interface CandidateSet {
  exact: string[];
  tagged: string[];
  heading: string[];
  neighborhood: string[];
  backlink: string[];
  lexical: string[];
  qmd?: string[];
}

export interface SelectCandidatesOptions {
  vaultPath: string;
  limit?: number;
  /** Page-ID hints from an optional qmd installation; null/absent disables the bucket. */
  qmdHints?: string[] | null;
}

const DEFAULT_LIMIT = 30;

/**
 * Cheap deterministic pre-filter that reduces the LLM's candidate space.
 * Buckets fill in priority order — exact title/alias, tag, heading substring,
 * wikilink neighborhood, backlinks, lexical body fallback, qmd hints — with a
 * global cap and cross-bucket dedup.
 */
export function selectCandidates(
  tree: TreeCache,
  query: string,
  opts: SelectCandidatesOptions
): CandidateSet {
  const limit = opts.limit ?? DEFAULT_LIMIT;
  const q = query.trim().toLowerCase();
  const seen = new Set<string>();
  let admitted = 0;
  const admit = (bucket: string[], id: string): void => {
    if (admitted >= limit || seen.has(id)) return;
    seen.add(id);
    bucket.push(id);
    admitted += 1;
  };

  const set: CandidateSet = {
    exact: [],
    tagged: [],
    heading: [],
    neighborhood: [],
    backlink: [],
    lexical: [],
  };

  // Exact title or alias (case-insensitive) — the zero-LLM fast path.
  for (const page of tree.pages) {
    if (page.title.toLowerCase() === q) admit(set.exact, page.id);
  }
  for (const [alias, pageId] of Object.entries(tree.by_alias)) {
    if (alias.toLowerCase() === q) admit(set.exact, pageId);
  }

  // Tag match (single-token queries).
  for (const [tag, pageIds] of Object.entries(tree.by_tag)) {
    if (tag.toLowerCase() === q) {
      for (const id of pageIds) admit(set.tagged, id);
    }
  }

  // Heading substring match — emits section node IDs.
  const matchedPages = new Set<string>([...set.exact, ...set.tagged]);
  for (const page of tree.pages) {
    walkSections(page, (s) => {
      if (s.heading.toLowerCase().includes(q)) {
        admit(set.heading, s.id);
        matchedPages.add(page.id);
      }
    });
  }

  // One-hop wikilink neighborhood and backlinks of matched pages.
  const byId = pagesById(tree.pages);
  for (const id of [...matchedPages].sort()) {
    const page = byId.get(id);
    if (!page) continue;
    for (const target of page.wikilinks) admit(set.neighborhood, target);
  }
  for (const id of [...matchedPages].sort()) {
    const page = byId.get(id);
    if (!page) continue;
    for (const source of page.backlinks) admit(set.backlink, source);
  }

  // Lexical body fallback, only while the cap has room.
  if (admitted < limit && q !== "") {
    for (const page of tree.pages) {
      if (admitted >= limit) break;
      if (seen.has(page.id)) continue;
      const content = readWikiFileNoFollow(join(opts.vaultPath, page.id));
      if (content !== null && content.toLowerCase().includes(q)) {
        admit(set.lexical, page.id);
      }
    }
  }

  if (opts.qmdHints != null) {
    set.qmd = [];
    for (const hint of opts.qmdHints) {
      admit(set.qmd, hint);
    }
  }

  return set;
}
