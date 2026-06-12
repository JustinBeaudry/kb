import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { byteLength } from "../bytes";

export { byteLength };

const POINTER_BUDGET = 500;
const MAX_CATEGORIES = 8;
const HEADING_RE = /^##\s+(.+?)\s*$/gm;

function extractCategories(indexMd: string): string[] {
  const out: string[] = [];
  const matches = indexMd.matchAll(HEADING_RE);
  for (const m of matches) {
    const name = m[1]?.trim();
    if (name) out.push(name);
  }
  return out;
}

export interface PointerInput {
  vaultPath: string;
  /** Optional one-line nudge, reserved inside POINTER_BUDGET before topic fitting. */
  nudge?: string | null;
}

function readIndexSafely(indexPath: string): string {
  try {
    return readFileSync(indexPath, "utf-8");
  } catch {
    return "";
  }
}

export function buildPointerPayload({ vaultPath, nudge }: PointerInput): string {
  const indexPath = join(vaultPath, "index.md");
  const categories: string[] = existsSync(indexPath)
    ? extractCategories(readIndexSafely(indexPath))
    : [];

  const header =
    "## KB Vault\n" +
    "Curated memory available. Do not read sessions/ or raw/ directly.\n" +
    "Run `kb map <query>` to find knowledge; `kb recall <query>` only as plain-text fallback.";

  // The nudge is reserved before topic fitting: topics shrink to make room so
  // the payload keeps the POINTER_BUDGET invariant.
  const suffix = nudge ? `\n${nudge}` : "";

  if (categories.length === 0) return `${header}${suffix}`;

  let rendered = `${header}${suffix}`;
  for (let n = Math.min(categories.length, MAX_CATEGORIES); n >= 1; n--) {
    const topics = categories.slice(0, n).join(", ");
    const candidate = `${header}\n\nTopics: ${topics}.${suffix}`;
    if (byteLength(candidate) <= POINTER_BUDGET) {
      rendered = candidate;
      break;
    }
  }

  return rendered;
}
