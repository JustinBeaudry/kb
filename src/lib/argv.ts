/**
 * Raw-argv view of a subcommand's tokens minus the vault-path flag, which
 * citty owns: without the skip, the flag's value token would be mistaken for
 * a positional by commands that hand-parse their arguments.
 */
export function commandArgs(command: string, argv: string[] = process.argv): string[] {
  const index = argv.indexOf(command);
  if (index === -1) return [];
  const out: string[] = [];
  const rest = argv.slice(index + 1);
  for (let i = 0; i < rest.length; i++) {
    const arg = rest[i]!;
    if (arg === "--vault-path" || arg === "--vaultPath" || arg === "-p") {
      i += 1;
      continue;
    }
    if (arg.startsWith("--vault-path=") || arg.startsWith("--vaultPath=") || arg.startsWith("-p=")) {
      continue;
    }
    out.push(arg);
  }
  return out;
}
