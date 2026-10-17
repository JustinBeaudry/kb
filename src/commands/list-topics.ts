import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import type { EnvelopeChunk } from "../lib/envelope";
import { continuation, fitChunkPage, resolveRetrievalBudget, resolveRetrievalOffset } from "../lib/retrieval";
import { appendAccessLog } from "../lib/access-log";

export default defineCommand({
  meta: { name: "list-topics", description: "List curated topic headings from the vault index" },
  args: {
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
    budget: { type: "string", description: "Complete output byte budget (overrides KB_RETRIEVAL_BUDGET)" },
    offset: { type: "string", description: "Skip this many topic results (default 0)" },
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

    const indexPath = join(vaultPath, "index.md");
    const chunks: EnvelopeChunk[] = [];

    if (existsSync(indexPath)) {
      const body = readFileSync(indexPath, "utf-8");
      const lines = body.split("\n");
      for (let i = 0; i < lines.length; i++) {
        const match = /^##\s+(.+?)\s*$/.exec(lines[i]!);
        if (!match) continue;
        const heading = match[1]!.trim();
        if (!heading) continue;
        const lineNumber = i + 1;
        chunks.push({
          source: "index.md",
          line_range: [lineNumber, lineNumber],
          curation: "curated",
          text: heading,
        });
      }
    }

    const fitted = fitChunkPage({
      budget: budget.value,
      offset: offset.value,
      policy: { trust: "curated", source_scope: "wiki" },
      chunks,
      next: (position) => continuation("list-topics", {
        options: ["--offset", String(position)],
        positionals: [],
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
        command: "list-topics",
        query: "",
        pages_returned: fitted.chunks.length,
        bytes_returned: new TextEncoder().encode(wire).length,
        exit_code: 0,
      });
    } catch {
      // logging must never fail the command
    }
  },
});
