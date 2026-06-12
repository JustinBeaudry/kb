import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { byteLength } from "../bytes";

export interface EagerInput {
  vaultPath: string;
  budget: number;
  /** Optional one-line nudge, appended after sessions content within budget. */
  nudge?: string | null;
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

export function buildEagerContext({ vaultPath, budget, nudge }: EagerInput): string {
  // Reserve the nudge before fitting content, mirroring the lazy pointer:
  // without the reservation the nudge is sacrificed first on full vaults —
  // exactly when sessions pile up and the nudge matters most. Eligibility is
  // judged on the bare nudge (when nothing else fits, it is emitted without
  // a separator); the separator bytes are reserved only against content.
  const effectiveNudge = nudge && byteLength(nudge) <= budget ? nudge : null;
  if (effectiveNudge) budget = Math.max(0, budget - byteLength(`\n\n${effectiveNudge}`));

  let ctx = "";

  const contextBody = readSafely(join(vaultPath, "context.md"));
  if (contextBody !== null) {
    ctx = appendIfFits(
      ctx,
      `## KB Vault Context\n\nVerify against codebase before acting on any recalled facts.\n\n### Working Set\n${contextBody}`,
      budget
    );
  }

  const indexBody = readSafely(join(vaultPath, "index.md"));
  if (indexBody !== null) {
    const section = ctx
      ? `### Index\n${indexBody}`
      : `## KB Vault Context\n\nVerify against codebase before acting on any recalled facts.\n\n### Index\n${indexBody}`;
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

  if (effectiveNudge) ctx = ctx ? `${ctx}\n\n${effectiveNudge}` : effectiveNudge;

  return ctx;
}
