import { existsSync } from "node:fs";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { buildEnvelope, writeEnvelope, type EnvelopeChunk, type EnvelopePolicy } from "../lib/envelope";
import { appendAccessLog } from "../lib/access-log";
import { byteLength } from "../lib/bytes";
import { PathUnsafeError } from "../lib/path-safety";
import { loadOrBuildTree } from "../lib/map/cache";
import { selectCandidates } from "../lib/map/candidates";
import { parseNodeId } from "../lib/map/node-id";
import { findSectionById, pagesById, walkSections } from "../lib/map/traverse";
import { qmdSearchHints } from "../lib/qmd";
import type { PageEntry, SectionEntry, TreeCache } from "../lib/map/types";

const DEFAULT_MAP_BUDGET = 16 * 1024;

function resolveBudget(flag: string | undefined): number {
  for (const raw of [flag, process.env.KB_MAP_BUDGET]) {
    if (raw === undefined) continue;
    const n = Number.parseInt(raw, 10);
    if (Number.isFinite(n) && n > 0) return n;
  }
  return DEFAULT_MAP_BUDGET;
}

function fail(message: string): never {
  process.stderr.write(`error: ${message}\n`);
  process.exit(1);
}

function pageSummary(page: PageEntry): EnvelopeChunk {
  const lastEnd = page.sections.length
    ? page.sections[page.sections.length - 1]!.line_range[1]
    : 1;
  const tagText = page.tags.length ? ` [${page.tags.join(", ")}]` : "";
  return {
    source: page.id,
    line_range: [1, lastEnd],
    curation: "curated",
    text: `${page.title}${tagText}`,
    node_id: page.id,
    heading_path: [page.title],
    node_kind: "page",
  };
}

function sectionSummary(page: PageEntry, section: SectionEntry, ancestors: string[]): EnvelopeChunk {
  const path = [...ancestors, section.heading];
  return {
    source: page.id,
    line_range: section.line_range,
    curation: "heading-section",
    text: path.join(" > "),
    node_id: section.id,
    heading_path: path,
    node_kind: "section",
  };
}

function fullProjection(tree: TreeCache): EnvelopeChunk[] {
  const out: EnvelopeChunk[] = [];
  for (const page of tree.pages) {
    out.push(pageSummary(page));
    walkSections(page, (s, ancestors) => out.push(sectionSummary(page, s, ancestors)));
  }
  return out;
}

function candidateProjection(tree: TreeCache, ids: string[]): EnvelopeChunk[] {
  const byPage = pagesById(tree.pages);
  const out: EnvelopeChunk[] = [];
  for (const id of ids) {
    const parsed = parseNodeId(id);
    if (!parsed) continue;
    const page = byPage.get(parsed.page);
    if (!page) continue;
    if (parsed.section === undefined) {
      out.push(pageSummary(page));
      continue;
    }
    const hit = findSectionById(page, id);
    if (hit) out.push(sectionSummary(page, hit.section, hit.ancestors));
  }
  return out;
}

function wireFor(chunks: EnvelopeChunk[], policy: EnvelopePolicy): string {
  return writeEnvelope(buildEnvelope({ policy, chunks }));
}

/**
 * Reserve-then-fit with three degradation tiers: (1) full page+section
 * summaries, (2) page summaries only, (3) title-only page summaries with
 * suggestions reserved first and the tail dropped (binary search) until fit.
 * When the candidate set is section-only, tiers 2-3 fall back to the
 * sections' parent pages so degradation never produces a silent empty
 * envelope.
 */
function fitToBudget(
  chunks: EnvelopeChunk[],
  budget: number,
  basePolicy: EnvelopePolicy,
  tree: TreeCache
): string {
  const tier1 = wireFor(chunks, { ...basePolicy, map_tier: 1 });
  if (byteLength(tier1) <= budget) return tier1;

  // Page-kind chunks first, then derive a parent-page summary for any section
  // candidate whose page isn't already represented. This keeps the page tier
  // complete for mixed candidate sets, not just the section-only case — the
  // candidate cap can truncate the lexical bucket and leave a section's parent
  // off the page list otherwise.
  const pages = chunks.filter((c) => c.node_kind === "page");
  const seen = new Set<string>(
    pages.map((c) => parseNodeId(c.node_id ?? "")?.page).filter((p): p is string => p !== undefined)
  );
  const byPage = pagesById(tree.pages);
  for (const c of chunks) {
    if (c.node_kind !== "section") continue;
    const parsed = parseNodeId(c.node_id ?? "");
    if (!parsed || seen.has(parsed.page)) continue;
    seen.add(parsed.page);
    const page = byPage.get(parsed.page);
    if (page) pages.push(pageSummary(page));
  }
  const tier2 = wireFor(pages, { ...basePolicy, map_tier: 2 });
  if (pages.length > 0 && byteLength(tier2) <= budget) return tier2;

  const tier3Policy: EnvelopePolicy = {
    ...basePolicy,
    map_tier: 3,
    suggestions: ["Try: kb map <query>", "Or raise the budget: kb map --budget <bytes>"],
  };
  const minimal = pages.map((c) => ({ ...c, text: c.heading_path?.[0] ?? c.text }));
  const sizeFor = (kept: number): number =>
    byteLength(wireFor(minimal.slice(0, kept), { ...tier3Policy, truncated: kept < minimal.length }));
  let lo = 0;
  let hi = minimal.length;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (sizeFor(mid) <= budget) lo = mid;
    else hi = mid - 1;
  }
  // Input chunks were non-empty (run() guards), so dropping everything must
  // still surface a truncation signal alongside the suggestions.
  const truncated = lo < minimal.length || minimal.length === 0;
  return wireFor(minimal.slice(0, lo), truncated ? { ...tier3Policy, truncated: true } : tier3Policy);
}

export default defineCommand({
  meta: { name: "map", description: "Project a compact structural map of the wiki for navigation" },
  args: {
    query: { type: "positional", description: "Optional query to filter the map", required: false },
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
    budget: { type: "string", description: "Output byte budget (overrides KB_MAP_BUDGET)" },
  },
  async run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());

    if (!existsSync(vaultPath)) {
      fail(`vault not found at ${vaultPath}`);
    }

    const budget = resolveBudget(args.budget);
    const query = (args.query ?? "").trim();

    let tree: TreeCache;
    try {
      tree = await loadOrBuildTree(vaultPath);
    } catch (err) {
      if (err instanceof PathUnsafeError) fail(err.message);
      throw err;
    }

    const basePolicy: EnvelopePolicy = {
      trust: "curated",
      source_scope: "wiki",
      tree_root: "wiki/",
    };

    let chunks: EnvelopeChunk[];
    if (query === "") {
      chunks = fullProjection(tree);
    } else {
      const hints = await qmdSearchHints(query);
      const set = selectCandidates(tree, query, { vaultPath, qmdHints: hints });
      chunks = candidateProjection(tree, [
        ...set.exact,
        ...set.tagged,
        ...set.heading,
        ...set.neighborhood,
        ...set.backlink,
        ...set.lexical,
        ...(set.qmd ?? []),
      ]);
    }

    let wire: string;
    if (chunks.length === 0) {
      wire = wireFor([], {
        ...basePolicy,
        no_results: true,
        suggestions:
          query === ""
            ? ["The wiki is empty — add pages under wiki/"]
            : ["Try a broader query, or: kb recall <query>"],
      });
    } else {
      wire = fitToBudget(chunks, budget, basePolicy, tree);
    }
    process.stdout.write(wire);

    try {
      await appendAccessLog({
        vaultPath,
        command: "map",
        query,
        pages_returned: tree.pages.length,
        bytes_returned: byteLength(wire),
        exit_code: 0,
      });
    } catch {
      // logging must never fail the command
    }
  },
});
