import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { summarizeAll, summarizeSession } from "../lib/summarizer";

export default defineCommand({
  meta: { name: "summarize", description: "Generate or return a cached session summary" },
  async run() {
    const vaultPath = resolveVaultPath(process.cwd());
    const args = commandArgs("summarize");
    const flags = parseFlags(args);

    try {
      if (flags.all) {
        await summarizeAll(
          vaultPath,
          { force: flags.force, destructive: flags.destructive },
          (line) => console.log(line)
        );
        return;
      }

      if (!flags.session) throw new Error("usage: cairn summarize [--json] [--force] <session>");
      const result = await summarizeSession(vaultPath, flags.session, {
        force: flags.force,
        destructive: flags.destructive,
      });

      if (flags.json) {
        console.log(JSON.stringify(result));
      } else {
        console.log(result.path);
      }
    } catch (err) {
      console.error(err instanceof Error ? err.message : String(err));
      process.exit(1);
    }
  },
});

interface ParsedFlags {
  all: boolean;
  json: boolean;
  force: boolean;
  destructive: boolean;
  session: string | null;
}

function parseFlags(args: string[]): ParsedFlags {
  const parsed: ParsedFlags = {
    all: false,
    json: false,
    force: false,
    destructive: false,
    session: null,
  };

  for (const arg of args) {
    if (arg === "--all") parsed.all = true;
    else if (arg === "--json") parsed.json = true;
    else if (arg === "--force") parsed.force = true;
    else if (arg === "--destructive") parsed.destructive = true;
    else parsed.session = arg;
  }

  return parsed;
}

function commandArgs(command: string): string[] {
  const index = process.argv.indexOf(command);
  return index === -1 ? [] : process.argv.slice(index + 1);
}
