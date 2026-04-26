import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

export interface EagerInput {
  vaultPath: string;
  budget: number;
}

function byteLength(s: string): number {
  return new TextEncoder().encode(s).length;
}

function readSafely(path: string): string | null {
  try {
    return readFileSync(path, "utf-8");
  } catch {
    return null;
  }
}

function appendIfFits(current: string, section: string, budget: number): string {
  const candidate = current ? `${current}\n\n${section}` : section;
  return byteLength(candidate) <= budget ? candidate : current;
}

export function buildEagerContext({ vaultPath, budget }: EagerInput): string {
  let ctx = "";

  const contextBody = readSafely(join(vaultPath, "context.md"));
  if (contextBody !== null) {
    ctx = appendIfFits(
      ctx,
      `## Cairn Vault Context\n\nVerify against codebase before acting on any recalled facts.\n\n### Working Set\n${contextBody}`,
      budget
    );
  }

  const indexBody = readSafely(join(vaultPath, "index.md"));
  if (indexBody !== null) {
    const section = ctx
      ? `### Index\n${indexBody}`
      : `## Cairn Vault Context\n\nVerify against codebase before acting on any recalled facts.\n\n### Index\n${indexBody}`;
    ctx = appendIfFits(ctx, section, budget);
  }

  const sessionsDir = join(vaultPath, "sessions");
  if (existsSync(sessionsDir)) {
    let files: string[];
    try {
      files = readdirSync(sessionsDir)
        .filter((f) => f.endsWith(".md"))
        .sort()
        .reverse();
    } catch {
      files = [];
    }
    const summariesDir = join(sessionsDir, "summaries");
    let headerAdded = false;
    for (const f of files) {
      const summaryBody = readSafely(join(summariesDir, f));
      const body = summaryBody ?? readSafely(join(sessionsDir, f));
      if (body === null) continue;
      const section = headerAdded
        ? `\n---\n${body}`
        : `\n### Recent Sessions\n---\n${body}`;
      const next = appendIfFits(ctx, section, budget);
      if (next === ctx) break;
      ctx = next;
      headerAdded = true;
    }
  }

  return ctx;
}
