import { existsSync, readFileSync, realpathSync } from "node:fs";
import { isAbsolute, join, resolve } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { buildEnvelope, emitEnvelope } from "../lib/envelope";

function normalizePageName(page: string): string {
  return page.endsWith(".md") ? page : `${page}.md`;
}

function isWithin(child: string, parent: string): boolean {
  const rel = resolve(child);
  const root = resolve(parent);
  return rel === root || rel.startsWith(`${root}/`);
}

export default defineCommand({
  meta: { name: "get", description: "Fetch a curated wiki page" },
  args: {
    page: { type: "positional", description: "Wiki page name (without path)", required: true },
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
  },
  run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());

    if (!existsSync(vaultPath)) {
      process.stderr.write(`error: vault not found at ${vaultPath}\n`);
      process.exit(1);
    }

    const pageArg = args.page;
    if (isAbsolute(pageArg) || pageArg.includes("..")) {
      process.stderr.write(`error: invalid page name: ${pageArg}\n`);
      process.exit(1);
    }

    const wikiDir = join(vaultPath, "wiki");
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
    emitEnvelope(
      buildEnvelope({
        policy: { trust: "curated", source_scope: "wiki" },
        chunks: [
          {
            source: `wiki/${filename}`,
            line_range: [1, lines.length],
            curation: "curated",
            text: body,
          },
        ],
      })
    );
  },
});
