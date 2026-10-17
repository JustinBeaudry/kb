import { existsSync, readFileSync, realpathSync } from "node:fs";
import { join } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { continuation, fitLineChunk, resolveRetrievalBudget, resolveRetrievalOffset } from "../lib/retrieval";
import { appendAccessLog } from "../lib/access-log";
import {
  assertGenuineScopeDir,
  assertSafeFilename,
  isWithin,
  PathUnsafeError,
} from "../lib/path-safety";

function normalizePageName(page: string): string {
  return page.endsWith(".md") ? page : `${page}.md`;
}

export default defineCommand({
  meta: { name: "get", description: "Fetch a curated wiki page" },
  args: {
    page: { type: "positional", description: "Wiki page name (without path)", required: true },
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
    budget: { type: "string", description: "Complete output byte budget (overrides KB_RETRIEVAL_BUDGET)" },
    "start-line": { type: "string", description: "First absolute source line to return (default 1)" },
  },
  async run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());
    const budget = resolveRetrievalBudget(args.budget);
    if ("error" in budget) {
      process.stderr.write(`error: ${budget.error}\n`);
      process.exit(1);
    }
    const start = resolveRetrievalOffset(args["start-line"], "start-line", 1);
    if ("error" in start) {
      process.stderr.write(`error: ${start.error}\n`);
      process.exit(1);
    }

    if (!existsSync(vaultPath)) {
      process.stderr.write(`error: vault not found at ${vaultPath}\n`);
      process.exit(1);
    }

    const pageArg = args.page;
    try {
      assertSafeFilename(pageArg);
    } catch (err) {
      if (err instanceof PathUnsafeError) {
        process.stderr.write(`error: ${err.message}\n`);
        process.exit(1);
      }
      throw err;
    }

    const wikiDir = join(vaultPath, "wiki");
    try {
      assertGenuineScopeDir(wikiDir, vaultPath);
    } catch (err) {
      if (err instanceof PathUnsafeError) {
        process.stderr.write(`error: ${err.message}\n`);
        process.exit(1);
      }
      throw err;
    }

    const filename = normalizePageName(pageArg);
    const target = join(wikiDir, filename);

    if (!existsSync(target)) {
      process.stderr.write(`error: page not found: ${filename}\n`);
      process.exit(1);
    }

    const realTarget = realpathSync(target);
    const realWiki = realpathSync(wikiDir);
    if (!isWithin(realTarget, realWiki)) {
      process.stderr.write(`error: page not found: ${filename}\n`);
      process.exit(1);
    }

    const body = readFileSync(realTarget, "utf-8");
    const lines = body.split("\n");
    if (start.value > lines.length) {
      process.stderr.write(`error: --start-line exceeds the page's ${lines.length} source lines\n`);
      process.exit(1);
    }
    const fitted = fitLineChunk({
      budget: budget.value,
      policy: { trust: "curated", source_scope: "wiki" },
      chunk: {
        source: `wiki/${filename}`,
        line_range: [start.value, lines.length],
        curation: "curated",
        text: lines.slice(start.value - 1).join("\n"),
      },
      next: (line) => continuation("get", {
        options: ["--start-line", String(line)],
        positionals: [pageArg],
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
        command: "get",
        query: pageArg,
        pages_returned: 1,
        bytes_returned: new TextEncoder().encode(wire).length,
        exit_code: 0,
      });
    } catch {
      // logging must never fail the command
    }
  },
});
