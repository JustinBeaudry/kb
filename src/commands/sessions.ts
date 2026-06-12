import { defineCommand } from "citty";
import { appendAccessLog } from "../lib/access-log";
import { listManifests, listUnprocessedManifests } from "../lib/session-state";
import { resolveVaultPath } from "../lib/vault";

export default defineCommand({
  meta: {
    name: "sessions",
    description: "List session manifest names (never content)",
  },
  args: {
    unprocessed: {
      type: "boolean",
      description: "Only manifests not yet marked extracted",
    },
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
  },
  async run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());
    let names: string[];
    try {
      names = args.unprocessed
        ? listUnprocessedManifests(vaultPath)
        : listManifests(vaultPath);
    } catch (err) {
      console.error(`sessions: ${err instanceof Error ? err.message : String(err)}`);
      process.exit(1);
    }
    for (const name of names) console.log(name);
    try {
      await appendAccessLog({
        vaultPath,
        command: "sessions",
        query: args.unprocessed ? "--unprocessed" : "",
        pages_returned: names.length,
        bytes_returned: 0,
        exit_code: 0,
      });
    } catch {
      // Logging never fails the command.
    }
  },
});
