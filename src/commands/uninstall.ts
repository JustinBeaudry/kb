import { defineCommand } from "citty";
import { removeHooks } from "../lib/settings";
import { resolveVaultPath } from "../lib/vault";
import { join } from "node:path";
import { homedir } from "node:os";
import { createInterface } from "node:readline";

async function confirm(message: string): Promise<boolean> {
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  return new Promise((resolve) => {
    rl.question(`${message} [y/N] `, (answer) => {
      rl.close();
      resolve(answer.toLowerCase() === "y");
    });
  });
}

export default defineCommand({
  meta: { name: "uninstall", description: "Remove Cairn hooks (preserves vault)" },
  args: {
    force: { type: "boolean", description: "Skip confirmation prompt", alias: ["f"], default: false },
    settingsPath: { type: "string", description: "Path to Claude Code settings.json (for testing)" },
  },
  async run({ args }) {
    const settingsPath = args.settingsPath ?? join(homedir(), ".claude", "settings.json");
    const vaultPath = resolveVaultPath(process.cwd());

    if (!args.force) {
      console.log("This will:");
      console.log("  - Remove Cairn hooks from Claude Code settings");
      console.log(`  - Preserve your vault at ${vaultPath}`);
      console.log("");
      const ok = await confirm("Continue?");
      if (!ok) { console.log("Cancelled."); return; }
    }

    removeHooks(settingsPath);
    console.log("\nCairn hooks removed from settings.json.");
    console.log(`Vault preserved at ${vaultPath}. Delete manually if desired.`);
  },
});
