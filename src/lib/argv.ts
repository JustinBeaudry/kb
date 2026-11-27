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

/**
 * Splits the vault-path short option written as `-p=value` into `-p value`.
 * citty 0.2 parses with node's util.parseArgs, which reads `-p=value` as the
 * value "=value"; kb has always accepted the inline form, so it is normalized
 * before parsing. Only `-p` is rewritten: it is kb's one value-taking short
 * alias, and leaving every other token alone keeps a string option's value
 * intact when that value happens to look like `-x=...` (for example
 * `--query -x=foo`). A token that is itself the value of a preceding vault-path
 * flag, and tokens after a `--` terminator, are left alone too.
 */
export function normalizeShortInlineValues(args: string[]): string[] {
  const out: string[] = [];
  for (let i = 0; i < args.length; i++) {
    const arg = args[i]!;
    if (arg === "--") {
      out.push(...args.slice(i));
      break;
    }
    if (arg === "-p" || arg === "--vault-path" || arg === "--vaultPath") {
      out.push(arg);
      if (i + 1 < args.length) out.push(args[++i]!);
      continue;
    }
    if (arg.startsWith("-p=")) out.push("-p", arg.slice(3));
    else out.push(arg);
  }
  return out;
}
