import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { buildEnvelope, emitEnvelope, type EnvelopeChunk } from "../lib/envelope";

const MAX_MATCHES = 20;
const CONTEXT_LINES = 3;
const MAX_FILE_BYTES = 256 * 1024;

function walkWiki(wikiDir: string, onFile: (path: string) => void): void {
  for (const entry of readdirSync(wikiDir)) {
    if (entry.startsWith(".")) continue;
    const full = join(wikiDir, entry);
    const stat = statSync(full);
    if (stat.isDirectory()) {
      walkWiki(full, onFile);
    } else if (entry.endsWith(".md")) {
      onFile(full);
    }
  }
}

function grepFile(path: string, needle: string): Array<{ lineStart: number; lineEnd: number; snippet: string }> {
  const stat = statSync(path);
  if (stat.size > MAX_FILE_BYTES) return [];
  const lines = readFileSync(path, "utf-8").split("\n");
  const needleLc = needle.toLowerCase();
  const hits: Array<{ lineStart: number; lineEnd: number; snippet: string }> = [];
  for (let i = 0; i < lines.length; i++) {
    if (lines[i]!.toLowerCase().includes(needleLc)) {
      const start = Math.max(0, i - CONTEXT_LINES);
      const end = Math.min(lines.length - 1, i + CONTEXT_LINES);
      hits.push({
        lineStart: start + 1,
        lineEnd: end + 1,
        snippet: lines.slice(start, end + 1).join("\n"),
      });
    }
  }
  return hits;
}

export default defineCommand({
  meta: { name: "recall", description: "Search curated wiki pages" },
  args: {
    query: { type: "positional", description: "Search query", required: true },
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
  },
  run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());

    if (!existsSync(vaultPath)) {
      process.stderr.write(`error: vault not found at ${vaultPath}\n`);
      process.exit(1);
    }

    const wikiDir = join(vaultPath, "wiki");
    if (!existsSync(wikiDir)) {
      process.stderr.write(`error: wiki directory missing at ${wikiDir}\n`);
      process.exit(1);
    }

    const chunks: EnvelopeChunk[] = [];
    const query = args.query;

    walkWiki(wikiDir, (path) => {
      if (chunks.length >= MAX_MATCHES) return;
      const hits = grepFile(path, query);
      const rel = relative(vaultPath, path);
      for (const h of hits) {
        if (chunks.length >= MAX_MATCHES) break;
        chunks.push({
          source: rel,
          line_range: [h.lineStart, h.lineEnd],
          curation: "curated",
          text: h.snippet,
        });
      }
    });

    const policy =
      chunks.length === 0
        ? {
            trust: "curated" as const,
            source_scope: "wiki" as const,
            no_results: true,
            suggestions: ["Try: cairn list-topics"],
          }
        : { trust: "curated" as const, source_scope: "wiki" as const };

    emitEnvelope(buildEnvelope({ policy, chunks }));
  },
});
