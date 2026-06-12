import { describe, it, expect } from "bun:test";
import { parseHeadings, parseSections, parseWikilinks, slugify } from "../src/lib/markdown";

describe("parseHeadings", () => {
  it("returns headings with levels and 1-indexed lines", () => {
    const body = "# Title\n\n## First\ntext\n## Second\n### Nested\nmore\n";
    const headings = parseHeadings(body);
    expect(headings).toEqual([
      { text: "Title", level: 1, line: 1 },
      { text: "First", level: 2, line: 3 },
      { text: "Second", level: 2, line: 5 },
      { text: "Nested", level: 3, line: 6 },
    ]);
  });

  it("returns empty array for empty body", () => {
    expect(parseHeadings("")).toEqual([]);
  });

  it("picks up ATX headings inside fenced code blocks (documented v1 limitation)", () => {
    const body = "## Real\n```\n## Inside Fence\n```\n";
    const headings = parseHeadings(body);
    expect(headings.map((h) => h.text)).toEqual(["Real", "## Inside Fence".replace(/^#+\s*/, "")]);
  });
});

describe("parseSections", () => {
  it("nests by level; last section's range ends at EOF", () => {
    const body = "# Title\nintro\n## A\na-text\n### A1\na1-text\n### A2\na2-text\n## B\nb-text\n";
    const sections = parseSections(body);
    expect(sections.length).toBe(1);
    const title = sections[0]!;
    expect(title.heading).toBe("Title");
    expect(title.line_range).toEqual([1, 11]);
    expect(title.children.map((s) => s.heading)).toEqual(["A", "B"]);
    const a = title.children[0]!;
    expect(a.line_range).toEqual([3, 8]);
    expect(a.children.map((s) => s.heading)).toEqual(["A1", "A2"]);
    expect(a.children[0]!.line_range).toEqual([5, 6]);
    expect(a.children[1]!.line_range).toEqual([7, 8]);
    const b = title.children[1]!;
    expect(b.line_range).toEqual([9, 11]);
  });

  it("body starting at H3 yields top-level sections at H3", () => {
    const body = "### Deep Start\ntext\n### Another\n";
    const sections = parseSections(body);
    expect(sections.map((s) => s.heading)).toEqual(["Deep Start", "Another"]);
    expect(sections[0]!.level).toBe(3);
  });

  it("empty body returns empty array", () => {
    expect(parseSections("")).toEqual([]);
  });

  it("records wikilink targets within each section's range", () => {
    const body = "## A\nsee [[foo]]\n## B\nsee [[bar|Bar]]\n";
    const sections = parseSections(body);
    expect(sections[0]!.wikilinks).toEqual(["foo"]);
    expect(sections[1]!.wikilinks).toEqual(["bar"]);
  });
});

describe("parseWikilinks", () => {
  it("captures bare targets", () => {
    const links = parseWikilinks("see [[foo]]\n");
    expect(links).toEqual([{ target: "foo", line: 1 }]);
  });

  it("captures display text", () => {
    const links = parseWikilinks("[[foo|Foo Display]]");
    expect(links).toEqual([{ target: "foo", display: "Foo Display", line: 1 }]);
  });

  it("captures heading anchors", () => {
    const links = parseWikilinks("[[file#Heading|T]]");
    expect(links).toEqual([{ target: "file", heading: "Heading", display: "T", line: 1 }]);
  });

  it("captures block refs", () => {
    const links = parseWikilinks("[[file#^block1]]");
    expect(links).toEqual([{ target: "file", blockRef: "block1", line: 1 }]);
  });

  it("captures path-style targets verbatim", () => {
    const links = parseWikilinks("[[raw/foo.md]]");
    expect(links).toEqual([{ target: "raw/foo.md", line: 1 }]);
  });

  it("captures traversal-shaped targets verbatim without resolving", () => {
    const links = parseWikilinks("[[../etc/passwd]]");
    expect(links).toEqual([{ target: "../etc/passwd", line: 1 }]);
  });

  it("records the 1-indexed line of each link", () => {
    const links = parseWikilinks("first\n[[a]]\n\n[[b]]\n");
    expect(links.map((l) => [l.target, l.line])).toEqual([
      ["a", 2],
      ["b", 4],
    ]);
  });
});

describe("slugify", () => {
  it("lowercases and kebab-cases", () => {
    expect(slugify("Getting Started")).toBe("getting-started");
  });

  it("strips markdown formatting characters", () => {
    expect(slugify("**Bold**")).toBe("bold");
  });

  it("collapses repeated dashes and trims edges", () => {
    expect(slugify("a -- b")).toBe("a-b");
    expect(slugify("  spaced  ")).toBe("spaced");
  });

  it("returns identical slugs for duplicate headings (caller disambiguates)", () => {
    expect(slugify("Setup")).toBe(slugify("Setup"));
  });

  it("strips non-ASCII to remain stable", () => {
    expect(slugify("Caché Notes")).toBe("cach-notes");
  });
});
