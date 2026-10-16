import { existsSync } from "node:fs";
import { join, resolve } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import type { EnvelopeChunk, EnvelopePolicy } from "../lib/envelope";
import {
  continuation,
  fitLineChunk,
  resolveRetrievalBudget,
  resolveRetrievalOffset,
  retrievalWire,
  type RetrievalResult,
} from "../lib/retrieval";
import { appendAccessLog } from "../lib/access-log";
import { byteLength } from "../lib/bytes";
import { PathUnsafeError } from "../lib/path-safety";
import { MAX_FILE_BYTES, readWikiFileNoFollow } from "../lib/wiki-read";
import { loadOrBuildTree, invalidateTree } from "../lib/map/cache";
import { resolveSectionLinks } from "../lib/map/builder";
import { isValidNodeId, parseNodeId } from "../lib/map/node-id";
import { findSectionById, pagesById, type SectionContext } from "../lib/map/traverse";
import type { PageEntry, SectionEntry, TreeCache } from "../lib/map/types";

const FOLLOW_CAP = 5;
const FOLLOW_PREVIEW_BYTES = 200;

interface Located {
  tree: TreeCache;
  byId: Map<string, PageEntry>;
  page: PageEntry;
  hit: SectionContext | null;
}

function sectionChunk(page: PageEntry, section: SectionEntry, ancestors: string[], lines: string[]): EnvelopeChunk {
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
    budget: { type: "string", description: "Complete output byte budget (overrides KB_RETRIEVAL_BUDGET)" },
    "start-line": { type: "string", description: "First absolute source line within the selected page or section" },
  },
  async run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());
    const budget = resolveRetrievalBudget(args.budget);
    if ("error" in budget) fail(budget.error);
    const requestedStart = resolveRetrievalOffset(args["start-line"], "start-line", 1);
    if ("error" in requestedStart) fail(requestedStart.error);

    if (!existsSync(vaultPath)) {
      fail(`vault not found at ${vaultPath}`);
    }

    const id = args.id;
    // Full grammar + path-safety validation before any cache lookup or read.
    const parsed = parseNodeId(id);
    if (!parsed) {
      fail(`invalid node id: ${id}`);
    }
    const { page: pageId, section: sectionPart } = parsed;

    const followRaw = Number.parseInt(args["follow-wikilinks"] ?? "0", 10);
    const followMax = Number.isFinite(followRaw) ? Math.min(Math.max(followRaw, 0), FOLLOW_CAP) : 0;

    const locate = async (): Promise<Located | null> => {
      let tree: TreeCache;
      try {
        tree = await loadOrBuildTree(vaultPath);
      } catch (err) {
        if (err instanceof PathUnsafeError) fail(err.message);
        throw err;
      }
      const byId = pagesById(tree.pages);
      const page = byId.get(pageId);
      if (!page) return null;
      const hit = sectionPart !== undefined ? findSectionById(page, id) : null;
      if (sectionPart !== undefined && !hit) return null;
      return { tree, byId, page, hit };
    };

    // Stale-cache retry: rebuild once to catch edits landing between command
    // start and ID resolution, then give up.
    let located = await locate();
    if (!located) {
      invalidateTree(vaultPath);
      located = await locate();
      if (!located) fail(`unknown node: ${id}`);
    }
    const { tree, byId, page, hit } = located;

    const content = readWikiFileNoFollow(join(vaultPath, pageId));
    if (content === null) {
      // The node is in the tree, so this is unreadable content (oversize or
      // I/O failure), not an unknown ID — give agents a distinct signal.
      fail(`node content unavailable (exceeds ${MAX_FILE_BYTES} bytes or unreadable): ${id}`);
    }
    const lines = content.split("\n");

    const chunks: EnvelopeChunk[] = [];
    const navTrace: string[] = [id];
    const partialPreviews: string[] = [];

    let followSources: string[];
    if (hit === null) {
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
      const { section, siblings, ancestors } = hit;
      chunks.push(sectionChunk(page, section, ancestors, lines));
      if (args.neighbors) {
        const idx = siblings.indexOf(section);
        const prev = siblings[idx - 1];
        const next = siblings[idx + 1];
        if (prev) chunks.push(sectionChunk(page, prev, ancestors, lines));
        if (next) chunks.push(sectionChunk(page, next, ancestors, lines));
      }
      followSources = resolveSectionLinks(new Set(byId.keys()), tree.by_alias, page.id, section);
    }

    const primary = chunks[0]!;
    const firstLine = args["start-line"] === undefined ? primary.line_range[0] : requestedStart.value;
    if (firstLine < primary.line_range[0] || firstLine > primary.line_range[1]) {
      fail(`--start-line must be inside the node's source range ${primary.line_range.join("-")}`);
    }
    primary.text = lines.slice(firstLine - 1, primary.line_range[1]).join("\n");
    primary.line_range = [firstLine, primary.line_range[1]];

    for (const target of followSources.slice(0, followMax)) {
      // nav_trace carries only grammar-valid IDs — never raw vault content.
      if (!isValidNodeId(target)) continue;
      const targetPage = byId.get(target);
      if (!targetPage) continue;
      const targetContent = readWikiFileNoFollow(join(vaultPath, target));
      if (targetContent === null) continue;
      // Previews retain whole source lines, including Unicode and escapes.
      // A long first line may be omitted by the shared output budget below.
      const targetLines = targetContent.split("\n");
      let previewEnd = 1;
      let previewBytes = byteLength(targetLines[0]!);
      while (previewEnd < targetLines.length) {
        const nextBytes = previewBytes + 1 + byteLength(targetLines[previewEnd]!);
        if (nextBytes > FOLLOW_PREVIEW_BYTES) break;
        previewBytes = nextBytes;
        previewEnd++;
      }
      const preview = targetLines.slice(0, previewEnd).join("\n");
      if (previewEnd < targetLines.length) partialPreviews.push(target);
      chunks.push({
        source: target,
        line_range: [1, previewEnd],
        curation: "curated",
        text: preview,
        node_id: target,
        heading_path: [targetPage.title],
        node_kind: "page",
      });
      navTrace.push(target);
    }

    const policyFor = (count: number): EnvelopePolicy => {
      const includedIds = new Set(chunks.slice(0, count).map((chunk) => chunk.node_id));
      const omittedIds = [...new Set([
        ...partialPreviews,
        ...chunks.slice(count).flatMap((chunk) => chunk.node_id ? [chunk.node_id] : []),
      ])];
      const policy: EnvelopePolicy = {
        trust: "curated",
        source_scope: "wiki",
        nav_trace: navTrace.filter((nodeId) => includedIds.has(nodeId)),
      };
      if (omittedIds.length > 0) {
        const quotedVault = `'${resolve(vaultPath).replaceAll("'", "'\\''")}'`;
        policy.truncated = true;
        policy.omitted_nodes = omittedIds;
        policy.suggestions = [`Fetch omitted or partial nodes with: kb get-node <node-id> -p ${quotedVault}`];
      }
      return policy;
    };

    // At most two neighbors and five linked previews: try complete optional
    // chunks in request order before spending any of the primary's lines.
    let fitted: RetrievalResult | undefined;
    for (let count = chunks.length; count >= 1; count--) {
      const included = chunks.slice(0, count);
      const wire = retrievalWire(included, policyFor(count));
      if (byteLength(wire) <= budget.value) {
        fitted = { wire, chunks: included };
        break;
      }
    }
    if (fitted === undefined) {
      const optionalArgs: string[] = [];
      if (args.neighbors) optionalArgs.push("--neighbors");
      if (followMax > 0) optionalArgs.push("--follow-wikilinks", String(followMax));
      fitted = fitLineChunk({
        budget: budget.value,
        policy: policyFor(1),
        chunk: primary,
        next: (line) => continuation("get-node", {
          options: ["--start-line", String(line), ...optionalArgs],
          positionals: [id],
        }, vaultPath, budget.value),
      });
    }
    if ("error" in fitted) fail(fitted.error);
    const wire = fitted.wire;
    process.stdout.write(wire);

    try {
      await appendAccessLog({
        vaultPath,
        command: "get-node",
        query: id,
        pages_returned: fitted.chunks.length,
        bytes_returned: byteLength(wire),
        exit_code: 0,
      });
    } catch {
      // logging must never fail the command
    }
  },
});
