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
    console.log(`  1. Set KB_VAULT to ${JSON.stringify(vaultPath)} in your agent's environment.`);
    console.log("  2. Give your agent skills/kb/SKILL.md from this repository and read the vault's KB.md.");
    console.log("  3. Use kb map to discover knowledge, then kb get-node to retrieve evidence.");
    console.log("  Optional Claude Code integration: claude plugin marketplace add JustinBeaudry/kb; claude plugin install kb@kb");

    if (!isQmdOnPath()) {
      console.log("");
      console.log(QMD_INSTALL_HINT);
    }
  },
});
