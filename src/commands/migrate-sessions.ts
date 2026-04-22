import { createInterface } from "node:readline";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { applyMigration, buildMigrationPlan } from "../lib/migration";

export default defineCommand({
  meta: { name: "migrate-sessions", description: "Migrate legacy session summaries to manifests" },
  async run() {
    const vaultPath = resolveVaultPath(process.cwd());
    const args = commandArgs("migrate-sessions");
    const apply = args.includes("--apply");
    const yes = args.includes("--yes");

    try {
      if (!apply) {
        const plan = buildMigrationPlan(vaultPath);
        printPlan(plan.entries);
        return;
      }

      const plan = await applyMigration(vaultPath, yes, confirmMigration);
      printPlan(plan.entries);
    } catch (err) {
      console.error(err instanceof Error ? err.message : String(err));
      process.exit(1);
    }
  },
});

function printPlan(entries: Array<{ file: string; class: string; action: string; state: string }>): void {
  console.log("file\tclass\taction\tstate");
  for (const entry of entries) {
    console.log(`${entry.file}\t${entry.class}\t${entry.action}\t${entry.state}`);
  }
}

function commandArgs(command: string): string[] {
  const index = process.argv.indexOf(command);
  return index === -1 ? [] : process.argv.slice(index + 1);
}

async function confirmMigration(): Promise<boolean> {
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  return new Promise((resolve) => {
    rl.question("Apply session migration? [y/N] ", (answer) => {
      rl.close();
      resolve(answer.toLowerCase() === "y");
    });
  });
}
