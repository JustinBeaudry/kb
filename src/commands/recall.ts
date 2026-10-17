import { existsSync, realpathSync } from "node:fs";
import { join, relative } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import type { EnvelopeChunk, EnvelopePolicy } from "../lib/envelope";
import { continuation, fitChunkPage, resolveRetrievalBudget, resolveRetrievalOffset } from "../lib/retrieval";
import { appendAccessLog } from "../lib/access-log";
import { assertGenuineScopeDir, PathUnsafeError } from "../lib/path-safety";
import { readWikiFileNoFollow, walkWiki } from "../lib/wiki-read";

const CONTEXT_LINES = 3;

function* grepFile(
  path: string,
  needle: string
): Generator<{ lineStart: number; lineEnd: number; snippet: string }> {
  const contents = readWikiFileNoFollow(path);
  if (contents === null) return;
  const lines = contents.split("\n");
  const needleLc = needle.toLowerCase();
  for (let i = 0; i < lines.length; i++) {
    if (lines[i]!.toLowerCase().includes(needleLc)) {
      const start = Math.max(0, i - CONTEXT_LINES);
      const end = Math.min(lines.length - 1, i + CONTEXT_LINES);
      yield {
        lineStart: start + 1,
        lineEnd: end + 1,
        snippet: lines.slice(start, end + 1).join("\n"),
      };
    }
  }
}

export default defineCommand({
  meta: { name: "recall", description: "Search curated wiki pages" },
  args: {
    query: { type: "positional", description: "Search query", required: true },
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
    budget: { type: "string", description: "Complete output byte budget (overrides KB_RETRIEVAL_BUDGET)" },
    offset: { type: "string", description: "Skip this many ordered search results (default 0)" },
  },
  async run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());
    const budget = resolveRetrievalBudget(args.budget);
    if ("error" in budget) {
      process.stderr.write(`error: ${budget.error}\n`);
      process.exit(1);
    }
    const offset = resolveRetrievalOffset(args.offset, "offset", 0);
    if ("error" in offset) {
      process.stderr.write(`error: ${offset.error}\n`);
      process.exit(1);
    }

    if (!existsSync(vaultPath)) {
      process.stderr.write(`error: vault not found at ${vaultPath}\n`);
      process.exit(1);
    }

    const wikiDir = join(vaultPath, "wiki");
    if (!existsSync(wikiDir)) {
      process.stderr.write(`error: wiki directory missing at ${wikiDir}\n`);
      process.exit(1);
    }

    try {
      assertGenuineScopeDir(wikiDir, vaultPath);
    } catch (err) {
      if (err instanceof PathUnsafeError) {
        process.stderr.write(`error: ${err.message}\n`);
        process.exit(1);
      }
      throw err;
    }

    const wikiReal = realpathSync(wikiDir);
    const query = args.query;

    const paths: string[] = [];
    walkWiki(wikiDir, wikiReal, (path) => paths.push(path));
    function* matches(): Generator<EnvelopeChunk> {
      for (const path of paths.sort()) {
        const rel = relative(vaultPath, path);
        for (const hit of grepFile(path, query)) {
          yield {
            source: rel,
            line_range: [hit.lineStart, hit.lineEnd],
            curation: "curated",
            text: hit.snippet,
          };
        }
      }
    }

    const results = matches();
    const first = results.next();
    function* chunks(): Generator<EnvelopeChunk> {
      if (!first.done) yield first.value;
      yield* results;
    }
    const policy: EnvelopePolicy =
      first.done
        ? {
            trust: "curated",
            source_scope: "wiki",
            no_results: true,
            suggestions: ["Try: kb list-topics"],
          }
        : { trust: "curated", source_scope: "wiki" };

    const fitted = fitChunkPage({
      budget: budget.value,
      offset: offset.value,
      policy,
      chunks: chunks(),
      next: (position) => continuation("recall", {
        options: ["--offset", String(position)],
        positionals: [query],
      }, vaultPath, budget.value),
    });
    if ("error" in fitted) {
      process.stderr.write(`error: ${fitted.error}\n`);
      process.exit(1);
    }
    const wire = fitted.wire;
    process.stdout.write(wire);

    try {
      await appendAccessLog({
        vaultPath,
        command: "recall",
        query,
        pages_returned: fitted.chunks.length,
        bytes_returned: new TextEncoder().encode(wire).length,
        exit_code: 0,
      });
    } catch {
      // logging must never fail the command
    }
  },
});
