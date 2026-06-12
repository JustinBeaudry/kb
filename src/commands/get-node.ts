import { existsSync } from "node:fs";
import { join } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { buildEnvelope, writeEnvelope, type EnvelopeChunk } from "../lib/envelope";
import { appendAccessLog } from "../lib/access-log";
import { PathUnsafeError } from "../lib/path-safety";
import { readWikiFileNoFollow } from "../lib/wiki-read";
import { loadOrBuildTree, invalidateTree } from "../lib/map/cache";
import { resolveTarget } from "../lib/map/builder";
import { isValidNodeId, parseNodeId } from "../lib/map/node-id";
import type { PageEntry, SectionEntry, TreeCache } from "../lib/map/types";

const FOLLOW_CAP = 5;
const FOLLOW_PREVIEW_CHARS = 200;

interface SectionHit {
  section: SectionEntry;
  siblings: SectionEntry[];
  ancestors: string[];
}

function findSection(page: PageEntry, id: string): SectionHit | null {
  const search = (siblings: SectionEntry[], ancestors: string[]): SectionHit | null => {
    for (const s of siblings) {
      if (s.id === id) return { section: s, siblings, ancestors };
      const hit = search(s.children, [...ancestors, s.heading]);
      if (hit) return hit;
    }
    return null;
  };
  return search(page.sections, [page.title]);
}

function sectionChunk(
  page: PageEntry,
  section: SectionEntry,
  ancestors: string[],
  lines: string[]
): EnvelopeChunk {
  const [start, end] = section.line_range;
  return {
    source: page.id,
    line_range: section.line_range,
    curation: "heading-section",
    text: lines.slice(start - 1, end).join("\n"),
    node_id: section.id,
    heading_path: [...ancestors, section.heading],
    node_kind: "section",
  };
}

function fail(message: string): never {
  process.stderr.write(`error: ${message}\n`);
  process.exit(1);
}

export default defineCommand({
  meta: { name: "get-node", description: "Fetch exact page or section content by node ID" },
  args: {
    id: { type: "positional", description: "Node ID (wiki/page.md or wiki/page.md#section)", required: true },
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
    neighbors: { type: "boolean", description: "Include previous/next sibling sections" },
    "follow-wikilinks": { type: "string", description: `Also preview up to N wikilinked pages (max ${FOLLOW_CAP})` },
  },
  async run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());

    if (!existsSync(vaultPath)) {
      fail(`vault not found at ${vaultPath}`);
    }

    const id = args.id;
    // Full grammar + path-safety validation before any cache lookup or read.
    if (!isValidNodeId(id)) {
      fail(`invalid node id: ${id}`);
    }
    const { page: pageId, section: sectionPart } = parseNodeId(id);

    const followRaw = Number.parseInt(args["follow-wikilinks"] ?? "0", 10);
    const followMax = Number.isFinite(followRaw) ? Math.min(Math.max(followRaw, 0), FOLLOW_CAP) : 0;

    let tree: TreeCache;
    try {
      tree = await loadOrBuildTree(vaultPath);
    } catch (err) {
      if (err instanceof PathUnsafeError) fail(err.message);
      throw err;
    }

    let page = tree.pages.find((p) => p.id === pageId);
    let hit = page && sectionPart !== undefined ? findSection(page, id) : null;
    if (!page || (sectionPart !== undefined && !hit)) {
      // Stale-cache retry: rebuild once to catch edits landing between
      // command start and ID resolution, then give up.
      await invalidateTree(vaultPath);
      tree = await loadOrBuildTree(vaultPath);
      page = tree.pages.find((p) => p.id === pageId);
      hit = page && sectionPart !== undefined ? findSection(page, id) : null;
      if (!page || (sectionPart !== undefined && !hit)) {
        fail(`unknown node: ${id}`);
      }
    }

    const content = readWikiFileNoFollow(join(vaultPath, pageId));
    if (content === null) {
      fail(`unknown node: ${id}`);
    }
    const lines = content.split("\n");

    const chunks: EnvelopeChunk[] = [];
    const navTrace: string[] = [id];

    let followSources: string[];
    if (sectionPart === undefined) {
      chunks.push({
        source: page.id,
        line_range: [1, lines.length],
        curation: "curated",
        text: content,
        node_id: page.id,
        heading_path: [page.title],
        node_kind: "page",
      });
      followSources = page.wikilinks;
    } else {
      const { section, siblings, ancestors } = hit!;
      chunks.push(sectionChunk(page, section, ancestors, lines));
      if (args.neighbors) {
        const idx = siblings.indexOf(section);
        const prev = siblings[idx - 1];
        const next = siblings[idx + 1];
        if (prev) chunks.push(sectionChunk(page, prev, ancestors, lines));
        if (next) chunks.push(sectionChunk(page, next, ancestors, lines));
      }
      const pageIds = new Set(tree.pages.map((p) => p.id));
      followSources = [];
      for (const raw of section.wikilinks) {
        const resolved = resolveTarget(pageIds, tree.by_alias, raw);
        if (resolved !== null && resolved !== page.id && !followSources.includes(resolved)) {
          followSources.push(resolved);
        }
      }
    }

    for (const target of followSources.slice(0, followMax)) {
      // nav_trace carries only grammar-valid IDs — never raw vault content.
      if (!isValidNodeId(target)) continue;
      const targetPage = tree.pages.find((p) => p.id === target);
      if (!targetPage) continue;
      const targetContent = readWikiFileNoFollow(join(vaultPath, target));
      if (targetContent === null) continue;
      const preview = targetContent.slice(0, FOLLOW_PREVIEW_CHARS);
      chunks.push({
        source: target,
        line_range: [1, preview.split("\n").length],
        curation: "curated",
        text: preview,
        node_id: target,
        heading_path: [targetPage.title],
        node_kind: "page",
      });
      navTrace.push(target);
    }

    const wire = writeEnvelope(
      buildEnvelope({
        policy: { trust: "curated", source_scope: "wiki", nav_trace: navTrace },
        chunks,
      })
    );
    process.stdout.write(wire);

    try {
      await appendAccessLog({
        vaultPath,
        command: "get-node",
        query: id,
        pages_returned: chunks.length,
        bytes_returned: new TextEncoder().encode(wire).length,
        exit_code: 0,
      });
    } catch {
      // logging must never fail the command
    }
  },
});
