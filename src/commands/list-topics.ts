import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { buildEnvelope, emitEnvelope, type EnvelopeChunk } from "../lib/envelope";

export default defineCommand({
  meta: { name: "list-topics", description: "List curated topic headings from the vault index" },
  args: {
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
  },
  run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());

    if (!existsSync(vaultPath)) {
      process.stderr.write(`error: vault not found at ${vaultPath}\n`);
      process.exit(1);
    }

    const indexPath = join(vaultPath, "index.md");
    const chunks: EnvelopeChunk[] = [];

    if (existsSync(indexPath)) {
      const body = readFileSync(indexPath, "utf-8");
      const lines = body.split("\n");
      const headingText = lines
        .map((line, i) => ({ line, i }))
        .filter(({ line }) => /^##\s+/.test(line))
        .map(({ line }) => line.replace(/^##\s+/, "").trim())
        .filter(Boolean)
        .join("\n");

      if (headingText) {
        chunks.push({
          source: "index.md",
          line_range: [1, lines.length],
          curation: "curated",
          text: headingText,
        });
      }
    }

    emitEnvelope(
      buildEnvelope({
        policy: { trust: "curated", source_scope: "wiki" },
        chunks,
      })
    );
  },
});
