import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { setSummaryPinned } from "../lib/summarizer";

export default defineCommand({
  meta: { name: "summaries", description: "Manage cached session summaries" },
  args: {
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
  },
  run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());
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

// Raw-argv view minus the vault-path flag, which citty owns: without the
// skip, its value token would be mistaken for the action/session positionals.
function commandArgs(command: string): string[] {
  const index = process.argv.indexOf(command);
  if (index === -1) return [];
  const out: string[] = [];
  const rest = process.argv.slice(index + 1);
  for (let i = 0; i < rest.length; i++) {
    const arg = rest[i]!;
    if (arg === "--vault-path" || arg === "--vaultPath" || arg === "-p") {
      i += 1;
      continue;
    }
    if (arg.startsWith("--vault-path=") || arg.startsWith("--vaultPath=")) continue;
    out.push(arg);
  }
  return out;
}
