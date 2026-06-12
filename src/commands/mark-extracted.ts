import { defineCommand } from "citty";
import { appendWriteAudit } from "../lib/access-log";
import { markExtracted } from "../lib/session-state";
import { resolveVaultPath } from "../lib/vault";

export default defineCommand({
  meta: {
    name: "mark-extracted",
    description: "Mark a session manifest as extracted (sets extracted: true)",
  },
  args: {
    filename: {
      type: "positional",
      description: "Manifest filename inside sessions/ (no subpaths)",
      required: true,
    },
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
  },
  async run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());
    const audit = async (exit_code: number) => {
      try {
        await appendWriteAudit({
          vaultPath,
          command: "mark-extracted",
          target: args.filename,
          exit_code,
        });
      } catch {
        // Logging never fails the command.
      }
    };
    try {
      markExtracted(vaultPath, args.filename);
    } catch (err) {
      await audit(1);
      console.error(
        `mark-extracted: ${err instanceof Error ? err.message : String(err)}`
      );
      process.exit(1);
    }
    await audit(0);
    console.log(`marked extracted: ${args.filename}`);
  },
});
