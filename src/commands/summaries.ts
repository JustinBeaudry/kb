import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { commandArgs } from "../lib/argv";
import { setSummaryPinned } from "../lib/summarizer";

export default defineCommand({
  meta: { name: "summaries", description: "Manage cached session summaries" },
  args: {
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
  },
  run({ args }) {
    // || (not ??): a valueless trailing flag parses as "", which must fall back.
    const vaultPath = args.vaultPath || resolveVaultPath(process.cwd());
    const [action, session] = commandArgs("summaries");

    try {
      if ((action !== "pin" && action !== "unpin") || !session) {
        throw new Error("usage: kb summaries pin|unpin <session>");
      }
      const path = setSummaryPinned(vaultPath, session, action === "pin");
      console.log(path);
    } catch (err) {
      console.error(err instanceof Error ? err.message : String(err));
      process.exit(1);
    }
  },
});

