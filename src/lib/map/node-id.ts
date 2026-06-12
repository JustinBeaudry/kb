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
 * Every emitted ID is tracked so an ordinal can never collide with a natural
 * slug like "setup-2" (or a positional ID with a literal "section-1" heading).
 */
export function makeSectionIdAssigner(pageId: string): (slug: string, position: number) => string {
  const counts = new Map<string, number>();
  const emitted = new Set<string>();
  return (slug: string, position: number): string => {
    const base = slug === "" ? `section-${position}` : slug;
    let count = (counts.get(base) ?? 0) + 1;
    let part = count === 1 ? base : `${base}-${count}`;
    while (emitted.has(part)) {
      count += 1;
      part = `${base}-${count}`;
    }
    counts.set(base, count);
    emitted.add(part);
    return `${pageId}#${part}`;
  };
}
