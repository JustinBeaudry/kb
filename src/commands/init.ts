import { defineCommand } from "citty";
import { resolveVaultPath, checkVaultState, scaffoldVault } from "../lib/vault";
import { registerHooks } from "../lib/settings";
import { join } from "node:path";
import { homedir } from "node:os";

export default defineCommand({
  meta: { name: "init", description: "Initialize a Cairn vault and register hooks" },
  args: {
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
    settingsPath: { type: "string", description: "Path to Claude Code settings.json (for testing)" },
  },
  run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());
    const settingsPath = args.settingsPath ?? join(homedir(), ".claude", "settings.json");
    const state = checkVaultState(vaultPath);

    if (state === "obsidian") {
      console.error(`Error: ${vaultPath} appears to be an existing Obsidian vault.\nUse --vault-path to specify a different location.`);
      process.exit(1);
    }
    if (state === "occupied") {
      console.error(`Error: ${vaultPath} already exists and wasn't created by Cairn.\nUse --vault-path to specify a different location.`);
      process.exit(1);
    }
    if (state === "cairn") {
      console.log(`Cairn vault already initialized at ${vaultPath}.`);
      registerHooks(settingsPath, vaultPath);
      console.log("Hooks verified in settings.json.");
      return;
    }

    const result = scaffoldVault(vaultPath);
    registerHooks(settingsPath, vaultPath);

    console.log(`\nCairn vault initialized at ${vaultPath}\n`);
    if (result.created.length > 0) {
      console.log("Created:");
      for (const item of result.created) console.log(`  + ${item}`);
    }
    if (result.skipped.length > 0) {
      console.log("Skipped (already exists):");
      for (const item of result.skipped) console.log(`  - ${item}`);
    }
    console.log("\nHooks registered in settings.json.");
    console.log("\nNext: drop a file in ~/cairn/raw/ and ask Claude to ingest it.");
  },
});
