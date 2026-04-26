import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { buildEnvelope, writeEnvelope, type EnvelopeChunk } from "../lib/envelope";
import { appendAccessLog } from "../lib/access-log";

export default defineCommand({
  meta: { name: "list-topics", description: "List curated topic headings from the vault index" },
  args: {
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
  },
  async run({ args }) {
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

    const wire = writeEnvelope(
      buildEnvelope({
        policy: { trust: "curated", source_scope: "wiki" },
        chunks,
      })
    );
    process.stdout.write(wire);

    try {
      await appendAccessLog({
        vaultPath,
        command: "list-topics",
        query: "",
        pages_returned: chunks.length,
        bytes_returned: new TextEncoder().encode(wire).length,
        exit_code: 0,
      });
    } catch {
      // logging must never fail the command
    }
  },
});
