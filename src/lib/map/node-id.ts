const PAGE_PART_RE = /^wiki\/(?:[A-Za-z0-9][A-Za-z0-9._ -]*\/)*[A-Za-z0-9][A-Za-z0-9._ -]*\.md$/;
const SECTION_PART_RE = /^[a-z0-9][a-z0-9-]*$/;

export interface ParsedNodeId {
  page: string;
  section?: string;
}

export function isValidNodeId(id: string): boolean {
  if (id.includes("\0") || id.includes("\\") || id.includes("..")) return false;
  const hash = id.indexOf("#");
  const page = hash >= 0 ? id.slice(0, hash) : id;
  if (!PAGE_PART_RE.test(page)) return false;
  if (hash >= 0) {
    const section = id.slice(hash + 1);
    if (!SECTION_PART_RE.test(section)) return false;
  }
  return true;
}

/** Parse a node ID, or return null when it fails the grammar. */
export function parseNodeId(id: string): ParsedNodeId | null {
  if (!isValidNodeId(id)) return null;
  const hash = id.indexOf("#");
  if (hash < 0) return { page: id };
  return { page: id.slice(0, hash), section: id.slice(hash + 1) };
}

/**
 * Assign per-page section IDs from slugs: first occurrence is canonical,
 * later collisions get ordinal suffixes (-2, -3, ...). Empty slugs fall back
 * to a positional section-<n> ID so every emitted ID conforms to the grammar.
 */
export function makeSectionIdAssigner(pageId: string): (slug: string, position: number) => string {
  const seen = new Map<string, number>();
  return (slug: string, position: number): string => {
    const base = slug === "" ? `section-${position}` : slug;
    const count = (seen.get(base) ?? 0) + 1;
    seen.set(base, count);
    return count === 1 ? `${pageId}#${base}` : `${pageId}#${base}-${count}`;
  };
}
