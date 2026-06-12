import type { PageEntry, SectionEntry } from "./types";

export interface SectionContext {
  section: SectionEntry;
  /** Heading path from the page title down to (not including) this section. */
  ancestors: string[];
  /** The sibling array this section belongs to (for neighbor lookups). */
  siblings: SectionEntry[];
}

/** Depth-first walk over a page's section tree with ancestry context. */
export function walkSections(
  page: PageEntry,
  fn: (section: SectionEntry, ancestors: string[], siblings: SectionEntry[]) => void
): void {
  const visit = (siblings: SectionEntry[], ancestors: string[]): void => {
    for (const s of siblings) {
      fn(s, ancestors, siblings);
      visit(s.children, [...ancestors, s.heading]);
    }
  };
  visit(page.sections, [page.title]);
}

export function findSectionById(page: PageEntry, id: string): SectionContext | null {
  let hit: SectionContext | null = null;
  walkSections(page, (section, ancestors, siblings) => {
    if (hit === null && section.id === id) {
      hit = { section, ancestors, siblings };
    }
  });
  return hit;
}

export function pagesById(pages: PageEntry[]): Map<string, PageEntry> {
  return new Map(pages.map((p) => [p.id, p]));
}
