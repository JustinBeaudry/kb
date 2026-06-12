import { defineCommand } from "citty";
import { resolveVaultPath, checkVaultState, scaffoldVault } from "../lib/vault";
import { isQmdOnPath, QMD_INSTALL_HINT } from "../lib/qmd";

export default defineCommand({
  meta: { name: "init", description: "Initialize a KB vault" },
  args: {
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
  },
  run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());
    const state = checkVaultState(vaultPath);

    if (state === "obsidian") {
      console.error(
        `Error: ${vaultPath} appears to be an existing Obsidian vault.\n` +
          `Use --vault-path to specify a different location.`
      );
      process.exit(1);
    }
    if (state === "occupied") {
      console.error(
        `Error: ${vaultPath} already exists and wasn't created by KB.\n` +
          `Use --vault-path to specify a different location.`
      );
      process.exit(1);
    }
    if (state === "kb") {
      console.log(`KB vault already initialized at ${vaultPath}.`);
      return;
    }

    const result = scaffoldVault(vaultPath);

    console.log(`\nKB vault initialized at ${vaultPath}\n`);
    if (result.created.length > 0) {
      console.log("Created:");
      for (const item of result.created) console.log(`  + ${item}`);
    }
    if (result.skipped.length > 0) {
      console.log("Skipped (already exists):");
      for (const item of result.skipped) console.log(`  - ${item}`);
    }
    console.log("\nNext steps:");
    console.log("  1. Install the plugin: claude plugin install kb");
    console.log("  2. Drop a file in ~/kb/raw/ and ask Claude to ingest it.");

    if (!isQmdOnPath()) {
      console.log("");
      console.log(QMD_INSTALL_HINT);
    }
  },
});
