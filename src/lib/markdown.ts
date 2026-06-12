import type { Heading, Section, Wikilink } from "./map/types";

const HEADING_RE = /^(#{1,6})\s+(.+?)\s*$/;
const WIKILINK_RE = /\[\[([^\]\n]+?)\]\]/g;

export function parseHeadings(body: string): Heading[] {
  if (body === "") return [];
  const headings: Heading[] = [];
  const lines = body.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const m = HEADING_RE.exec(lines[i]!);
    if (m) {
      headings.push({ text: m[2]!, level: m[1]!.length, line: i + 1 });
    }
  }
  return headings;
}

export function parseWikilinks(body: string): Wikilink[] {
  if (body === "") return [];
  const links: Wikilink[] = [];
  const lines = body.split("\n");
  for (let i = 0; i < lines.length; i++) {
    for (const m of lines[i]!.matchAll(WIKILINK_RE)) {
      const inner = m[1]!;
      const pipe = inner.indexOf("|");
      const ref = (pipe >= 0 ? inner.slice(0, pipe) : inner).trim();
      const display = pipe >= 0 ? inner.slice(pipe + 1).trim() : undefined;
      const hash = ref.indexOf("#");
      const link: Wikilink = {
        target: hash >= 0 ? ref.slice(0, hash) : ref,
        line: i + 1,
      };
      if (hash >= 0) {
        const anchor = ref.slice(hash + 1);
        if (anchor.startsWith("^")) {
          link.blockRef = anchor.slice(1);
        } else {
          link.heading = anchor;
        }
      }
      if (display !== undefined) link.display = display;
      links.push(link);
    }
  }
  return links;
}

export function parseSections(body: string): Section[] {
  if (body === "") return [];
  const headings = parseHeadings(body);
  if (headings.length === 0) return [];
  const totalLines = body.split("\n").length;
  const links = parseWikilinks(body);

  // End line for each heading's section: the line before the next heading at
  // the same or a shallower level, else end of file.
  const sections: Section[] = [];
  const stack: Section[] = [];
  for (let i = 0; i < headings.length; i++) {
    const h = headings[i]!;
    let end = totalLines;
    for (let j = i + 1; j < headings.length; j++) {
      if (headings[j]!.level <= h.level) {
        end = headings[j]!.line - 1;
        break;
      }
    }
    const section: Section = {
      heading: h.text,
      level: h.level,
      line_range: [h.line, end],
      wikilinks: links
        .filter((l) => l.line >= h.line && l.line <= end)
        .map((l) => l.target),
      children: [],
    };
    while (stack.length > 0 && stack[stack.length - 1]!.level >= h.level) {
      stack.pop();
    }
    if (stack.length === 0) {
      sections.push(section);
    } else {
      stack[stack.length - 1]!.children.push(section);
    }
    stack.push(section);
  }
  return sections;
}

export function slugify(heading: string): string {
  return heading
    .toLowerCase()
    .replace(/\s+/g, "-")
    .replace(/[^a-z0-9-]/g, "")
    .replace(/-{2,}/g, "-")
    .replace(/^-+|-+$/g, "");
}
