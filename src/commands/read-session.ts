import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { runSensitiveRead } from "../lib/sensitive-read";

export default defineCommand({
  meta: {
    name: "read-session",
    description: "Read a bounded excerpt from sessions/ with explicit approval",
  },
  args: {
    filename: { type: "positional", description: "File inside sessions/", required: true },
    vaultPath: { type: "string", description: "Path to the vault directory", alias: ["p"] },
    approve: { type: "boolean", description: "Skip interactive prompt (non-interactive approval)" },
    lines: { type: "string", description: "Max lines to return (clamped to hard cap)" },
    bytes: { type: "string", description: "Max bytes to return (clamped to hard cap)" },
  },
  async run({ args }) {
    const vaultPath = args.vaultPath ?? resolveVaultPath(process.cwd());
    await runSensitiveRead({
      vaultPath,
      scope: "sessions",
      filename: args.filename,
      approve: Boolean(args.approve),
      lines: args.lines ? Number(args.lines) : undefined,
      bytes: args.bytes ? Number(args.bytes) : undefined,
    });
  },
});
