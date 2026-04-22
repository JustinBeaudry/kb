import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { setSummaryPinned } from "../lib/summarizer";

export default defineCommand({
  meta: { name: "summaries", description: "Manage cached session summaries" },
  run() {
    const vaultPath = resolveVaultPath(process.cwd());
    const [action, session] = commandArgs("summaries");

    try {
      if ((action !== "pin" && action !== "unpin") || !session) {
        throw new Error("usage: cairn summaries pin|unpin <session>");
      }
      const path = setSummaryPinned(vaultPath, session, action === "pin");
      console.log(path);
    } catch (err) {
      console.error(err instanceof Error ? err.message : String(err));
      process.exit(1);
    }
  },
});

function commandArgs(command: string): string[] {
  const index = process.argv.indexOf(command);
  return index === -1 ? [] : process.argv.slice(index + 1);
}
