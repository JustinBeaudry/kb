export interface Heading {
  text: string;
  level: number;
  line: number;
}

export interface Section {
  heading: string;
  level: number;
  line_range: [number, number];
  wikilinks: string[];
  children: Section[];
}

export interface Wikilink {
  target: string;
  heading?: string;
  blockRef?: string;
  display?: string;
  line: number;
}

export interface SectionEntry {
  id: string;
  heading: string;
  level: number;
  line_range: [number, number];
  wikilinks: string[];
  children: SectionEntry[];
}

export interface PageEntry {
  id: string;
  title: string;
  type?: string;
  tags: string[];
  aliases: string[];
  content_hash: string;
  size: number;
  mtime_ms: number;
  malformed?: boolean;
  sections: SectionEntry[];
  /** Raw targets appearing before the first heading (or anywhere on heading-free pages). */
  preamble_wikilinks?: string[];
  wikilinks: string[];
  unresolved_wikilinks: string[];
  backlinks: string[];
}

// v2: PageEntry gained preamble_wikilinks; the bump forces a one-time rebuild
// so unchanged pages on the stat fast path still gain their preamble links.
export const CACHE_SCHEMA_VERSION = "2";

export interface TreeCache {
  schema_version: typeof CACHE_SCHEMA_VERSION;
  built_at?: string;
  pages: PageEntry[];
  by_alias: Record<string, string>;
  by_tag: Record<string, string[]>;
}
