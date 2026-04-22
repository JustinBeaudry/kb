import { describe, it, expect } from "bun:test";
import { parseFrontmatter, serializeFrontmatter } from "../src/lib/frontmatter";

describe("parseFrontmatter", () => {
  it("parses frontmatter and body from a well-formed document", () => {
    const input = `---
title: Hello
tags:
  - one
  - two
count: 3
---

# Heading

Body paragraph.
`;
    const { data, body } = parseFrontmatter(input);
    expect(data).toEqual({ title: "Hello", tags: ["one", "two"], count: 3 });
    expect(body).toBe("# Heading\n\nBody paragraph.\n");
  });

  it("returns empty data and the whole content when no frontmatter is present", () => {
    const input = "# Heading\n\nBody paragraph.\n";
    const { data, body } = parseFrontmatter(input);
    expect(data).toEqual({});
    expect(body).toBe(input);
  });

  it("returns empty data and empty body on an empty string", () => {
    const { data, body } = parseFrontmatter("");
    expect(data).toEqual({});
    expect(body).toBe("");
  });

  it("treats a document with only frontmatter and no body as having an empty body", () => {
    const input = `---
title: Just Frontmatter
---
`;
    const { data, body } = parseFrontmatter(input);
    expect(data).toEqual({ title: "Just Frontmatter" });
    expect(body).toBe("");
  });

  it("throws with a readable message on malformed YAML", () => {
    const input = `---
title: Good
tags: [unclosed
---

Body.
`;
    expect(() => parseFrontmatter(input)).toThrow(/frontmatter/i);
  });

  it("preserves unknown fields on round-trip", () => {
    const input = `---
custom_field: 42
nested:
  key: value
---

Body.
`;
    const { data, body } = parseFrontmatter(input);
    const out = serializeFrontmatter(data, body);
    const parsed = parseFrontmatter(out);
    expect(parsed.data).toEqual(data);
    expect(parsed.body).toBe(body);
  });
});

describe("serializeFrontmatter", () => {
  it("emits standard --- delimiters and a trailing newline", () => {
    const out = serializeFrontmatter({ title: "Hi" }, "Body\n");
    expect(out).toMatch(/^---\n[\s\S]*\n---\n\n?Body\n$/);
  });

  it("emits only the body when data is empty", () => {
    const out = serializeFrontmatter({}, "Body only.\n");
    expect(out).toBe("Body only.\n");
  });

  it("round-trips null and boolean values", () => {
    const data = { a: null, b: true, c: false };
    const out = serializeFrontmatter(data, "");
    const parsed = parseFrontmatter(out);
    expect(parsed.data).toEqual(data);
  });
});
