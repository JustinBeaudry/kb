import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

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
}

function readIndexSafely(indexPath: string): string {
  try {
    return readFileSync(indexPath, "utf-8");
  } catch {
    return "";
  }
}

export function buildPointerPayload({ vaultPath }: PointerInput): string {
  const indexPath = join(vaultPath, "index.md");
  const categories: string[] = existsSync(indexPath)
    ? extractCategories(readIndexSafely(indexPath))
    : [];

  const header =
    "## Cairn Vault\n" +
    "Curated memory available. Do not read sessions/ or raw/ directly.\n" +
    "Run `cairn list-topics` or `cairn recall <query>` to retrieve.";

  if (categories.length === 0) return header;

  let rendered = header;
  for (let n = Math.min(categories.length, MAX_CATEGORIES); n >= 1; n--) {
    const topics = categories.slice(0, n).join(", ");
    const candidate = `${header}\n\nTopics: ${topics}.`;
    if (byteLength(candidate) <= POINTER_BUDGET) {
      rendered = candidate;
      break;
    }
  }

  return rendered;
}

export function byteLength(s: string): number {
  return new TextEncoder().encode(s).length;
}
