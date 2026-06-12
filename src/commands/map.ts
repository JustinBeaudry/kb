import { existsSync } from "node:fs";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { buildEnvelope, writeEnvelope, type EnvelopeChunk, type EnvelopePolicy } from "../lib/envelope";
import { appendAccessLog } from "../lib/access-log";
import { PathUnsafeError } from "../lib/path-safety";
import { loadOrBuildTree } from "../lib/map/cache";
import { selectCandidates } from "../lib/map/candidates";
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

interface NodeSummary {
  chunk: EnvelopeChunk;
  kind: "page" | "section";
}

function pageSummary(page: PageEntry): NodeSummary {
  const lastEnd = page.sections.length
    ? page.sections[page.sections.length - 1]!.line_range[1]
    : 1;
  const tagText = page.tags.length ? ` [${page.tags.join(", ")}]` : "";
  return {
    kind: "page",
    chunk: {
      source: page.id,
      line_range: [1, lastEnd],
      curation: "curated",
      text: `${page.title}${tagText}`,
      node_id: page.id,
      heading_path: [page.title],
      node_kind: "page",
    },
  };
}

function sectionSummaries(page: PageEntry): NodeSummary[] {
  const out: NodeSummary[] = [];
  const visit = (s: SectionEntry, ancestors: string[]): void => {
    const path = [...ancestors, s.heading];
    out.push({
      kind: "section",
      chunk: {
        source: page.id,
        line_range: s.line_range,
        curation: "heading-section",
        text: path.join(" > "),
        node_id: s.id,
        heading_path: path,
        node_kind: "section",
      },
    });
    for (const c of s.children) visit(c, path);
  };
  for (const s of page.sections) visit(s, [page.title]);
  return out;
}

function fullProjection(tree: TreeCache): NodeSummary[] {
  const out: NodeSummary[] = [];
  for (const page of tree.pages) {
    out.push(pageSummary(page));
    out.push(...sectionSummaries(page));
  }
  return out;
}

function candidateProjection(tree: TreeCache, ids: string[]): NodeSummary[] {
  const byPage = new Map(tree.pages.map((p) => [p.id, p]));
  const out: NodeSummary[] = [];
  for (const id of ids) {
    const hash = id.indexOf("#");
    if (hash < 0) {
      const page = byPage.get(id);
      if (page) out.push(pageSummary(page));
      continue;
    }
    const page = byPage.get(id.slice(0, hash));
    if (!page) continue;
    const section = sectionSummaries(page).find((s) => s.chunk.node_id === id);
    if (section) out.push(section);
  }
  return out;
}

interface Fitted {
  chunks: EnvelopeChunk[];
  tier: 1 | 2 | 3;
  truncated: boolean;
}

function wireSize(chunks: EnvelopeChunk[], policy: EnvelopePolicy): number {
  return new TextEncoder().encode(writeEnvelope(buildEnvelope({ policy, chunks }))).length;
}

/**
 * Reserve-then-fit with three degradation tiers: (1) full page+section
 * summaries, (2) page summaries only, (3) title-only page summaries with
 * suggestions reserved first and the tail dropped in order until it fits.
 */
function fitToBudget(summaries: NodeSummary[], budget: number, basePolicy: EnvelopePolicy): { fitted: Fitted; policy: EnvelopePolicy } {
  const tier1 = summaries.map((s) => s.chunk);
  if (wireSize(tier1, { ...basePolicy, map_tier: 1 }) <= budget) {
    return { fitted: { chunks: tier1, tier: 1, truncated: false }, policy: { ...basePolicy, map_tier: 1 } };
  }

  const tier2 = summaries.filter((s) => s.kind === "page").map((s) => s.chunk);
  if (wireSize(tier2, { ...basePolicy, map_tier: 2 }) <= budget) {
    return { fitted: { chunks: tier2, tier: 2, truncated: false }, policy: { ...basePolicy, map_tier: 2 } };
  }

  const tier3Policy: EnvelopePolicy = {
    ...basePolicy,
    map_tier: 3,
    suggestions: ["Try: kb map <query>", "Or raise the budget: kb map --budget <bytes>"],
  };
  const minimal = summaries
    .filter((s) => s.kind === "page")
    .map((s) => ({ ...s.chunk, text: s.chunk.heading_path?.[0] ?? s.chunk.text }));
  let kept = minimal.length;
  while (kept > 0 && wireSize(minimal.slice(0, kept), { ...tier3Policy, truncated: kept < minimal.length }) > budget) {
    kept -= 1;
  }
  const truncated = kept < minimal.length;
  return {
    fitted: { chunks: minimal.slice(0, kept), tier: 3, truncated },
    policy: truncated ? { ...tier3Policy, truncated: true } : tier3Policy,
  };
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
      process.stderr.write(`error: vault not found at ${vaultPath}\n`);
      process.exit(1);
    }

    const budget = resolveBudget(args.budget);
    const query = (args.query ?? "").trim();

    let tree: TreeCache;
    try {
      tree = await loadOrBuildTree(vaultPath);
    } catch (err) {
      if (err instanceof PathUnsafeError) {
        process.stderr.write(`error: ${err.message}\n`);
        process.exit(1);
      }
      throw err;
    }

    const basePolicy: EnvelopePolicy = {
      trust: "curated",
      source_scope: "wiki",
      tree_root: "wiki/",
    };

    let summaries: NodeSummary[];
    if (query === "") {
      summaries = fullProjection(tree);
    } else {
      const hints = await qmdSearchHints(query);
      const set = await selectCandidates(tree, query, { vaultPath, qmdHints: hints });
      const ordered = [
        ...set.exact,
        ...set.tagged,
        ...set.heading,
        ...set.neighborhood,
        ...set.backlink,
        ...set.lexical,
        ...(set.qmd ?? []),
      ];
      summaries = candidateProjection(tree, ordered);
    }

    let wire: string;
    if (summaries.length === 0) {
      const policy: EnvelopePolicy = {
        ...basePolicy,
        no_results: true,
        suggestions: query === "" ? ["The wiki is empty — add pages under wiki/"] : [`Try: kb recall ${query}`],
      };
      wire = writeEnvelope(buildEnvelope({ policy, chunks: [] }));
    } else {
      const { fitted, policy } = fitToBudget(summaries, budget, basePolicy);
      wire = writeEnvelope(buildEnvelope({ policy, chunks: fitted.chunks }));
    }
    process.stdout.write(wire);

    try {
      await appendAccessLog({
        vaultPath,
        command: "map",
        query,
        pages_returned: tree.pages.length,
        bytes_returned: new TextEncoder().encode(wire).length,
        exit_code: 0,
      });
    } catch {
      // logging must never fail the command
    }
  },
});
