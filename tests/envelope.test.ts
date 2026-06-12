import { describe, it, expect } from "bun:test";
import { buildEnvelope, parseEnvelope, writeEnvelope, type Envelope } from "../src/lib/envelope";

describe("envelope — buildEnvelope", () => {
  it("produces a schema_version, nonce, policy, and chunks", () => {
    const env = buildEnvelope({
      policy: { trust: "curated", source_scope: "wiki" },
      chunks: [],
    });
    expect(env.schema_version).toBe("2");
    expect(env.nonce).toMatch(/^[0-9a-f]{32}$/);
    expect(env.policy).toEqual({ trust: "curated", source_scope: "wiki" });
    expect(env.chunks).toEqual([]);
  });

  it("unique nonce per call", () => {
    const a = buildEnvelope({ policy: {}, chunks: [] });
    const b = buildEnvelope({ policy: {}, chunks: [] });
    expect(a.nonce).not.toBe(b.nonce);
  });

  it("passes chunks through with provenance", () => {
    const env = buildEnvelope({
      policy: { trust: "curated", source_scope: "wiki" },
      chunks: [
        { source: "wiki/auth.md", line_range: [1, 10], curation: "curated", text: "hello" },
      ],
    });
    expect(env.chunks.length).toBe(1);
    expect(env.chunks[0]!.source).toBe("wiki/auth.md");
    expect(env.chunks[0]!.line_range).toEqual([1, 10]);
  });
});

describe("envelope — writeEnvelope / parseEnvelope roundtrip", () => {
  it("length-prefixed output parses back to equal envelope", () => {
    const original: Envelope = {
      schema_version: "2",
      nonce: "f".repeat(32),
      policy: { trust: "curated" },
      chunks: [
        { source: "wiki/a.md", line_range: [1, 3], curation: "curated", text: "hi" },
      ],
    };
    const wire = writeEnvelope(original);
    const parsed = parseEnvelope(wire);
    expect(parsed).toEqual(original);
  });

  it("first line of wire format is decimal byte count of the JSON body", () => {
    const env: Envelope = {
      schema_version: "2",
      nonce: "0".repeat(32),
      policy: {},
      chunks: [],
    };
    const wire = writeEnvelope(env);
    const idx = wire.indexOf("\n");
    const prefix = wire.slice(0, idx);
    const body = wire.slice(idx + 1);
    const len = Number(prefix);
    expect(Number.isFinite(len)).toBe(true);
    expect(new TextEncoder().encode(body).length).toBe(len);
  });

  it("parse rejects malformed prefix", () => {
    expect(() => parseEnvelope("not-a-number\n{}")).toThrow();
  });

  it("parse rejects mismatched length", () => {
    const body = JSON.stringify({ schema_version: "2", nonce: "f".repeat(32), policy: {}, chunks: [] });
    const wrong = `${new TextEncoder().encode(body).length + 10}\n${body}`;
    expect(() => parseEnvelope(wrong)).toThrow();
  });

  it("parse rejects schema_version 1", () => {
    const body = JSON.stringify({ schema_version: "1", nonce: "f".repeat(32), policy: {}, chunks: [] });
    const wire = `${new TextEncoder().encode(body).length}\n${body}`;
    expect(() => parseEnvelope(wire)).toThrow(/schema_version/);
  });

  it("parse rejects schema_version 3", () => {
    const body = JSON.stringify({ schema_version: "3", nonce: "f".repeat(32), policy: {}, chunks: [] });
    const wire = `${new TextEncoder().encode(body).length}\n${body}`;
    expect(() => parseEnvelope(wire)).toThrow(/schema_version/);
  });

  it("parse rejects missing nonce", () => {
    const body = JSON.stringify({ schema_version: "2", policy: {}, chunks: [] });
    const wire = `${new TextEncoder().encode(body).length}\n${body}`;
    expect(() => parseEnvelope(wire)).toThrow(/nonce/);
  });

  it("parse rejects non-hex nonce", () => {
    const body = JSON.stringify({ schema_version: "2", nonce: "not-hex", policy: {}, chunks: [] });
    const wire = `${new TextEncoder().encode(body).length}\n${body}`;
    expect(() => parseEnvelope(wire)).toThrow(/nonce/);
  });

  it("parse rejects chunks with invalid curation", () => {
    const body = JSON.stringify({
      schema_version: "2",
      nonce: "f".repeat(32),
      policy: {},
      chunks: [
        { source: "x", line_range: [1, 1], curation: "poisoned", text: "" },
      ],
    });
    const wire = `${new TextEncoder().encode(body).length}\n${body}`;
    expect(() => parseEnvelope(wire)).toThrow(/curation/);
  });

  it("parse rejects non-object policy", () => {
    const body = JSON.stringify({ schema_version: "2", nonce: "f".repeat(32), policy: "x", chunks: [] });
    const wire = `${new TextEncoder().encode(body).length}\n${body}`;
    expect(() => parseEnvelope(wire)).toThrow(/policy/);
  });
});

describe("envelope v2 — structural extensions", () => {
  function wireFor(chunk: Record<string, unknown>): string {
    const body = JSON.stringify({
      schema_version: "2",
      nonce: "f".repeat(32),
      policy: {},
      chunks: [chunk],
    });
    return `${new TextEncoder().encode(body).length}\n${body}`;
  }

  it("accepts curation heading-section", () => {
    const env = parseEnvelope(
      wireFor({ source: "wiki/a.md", line_range: [3, 9], curation: "heading-section", text: "s" })
    );
    expect(env.chunks[0]!.curation).toBe("heading-section");
  });

  it("still accepts curation session-excerpt", () => {
    const env = parseEnvelope(
      wireFor({ source: "sessions/x.md", line_range: [1, 2], curation: "session-excerpt", text: "s" })
    );
    expect(env.chunks[0]!.curation).toBe("session-excerpt");
  });

  it("accepts chunks with node_id, heading_path, and node_kind", () => {
    const env = parseEnvelope(
      wireFor({
        source: "wiki/a.md",
        line_range: [3, 9],
        curation: "heading-section",
        text: "s",
        node_id: "wiki/a.md#setup",
        heading_path: ["A", "Setup"],
        node_kind: "section",
      })
    );
    expect(env.chunks[0]!.node_id).toBe("wiki/a.md#setup");
    expect(env.chunks[0]!.heading_path).toEqual(["A", "Setup"]);
    expect(env.chunks[0]!.node_kind).toBe("section");
  });

  it("accepts chunks without the optional structural fields", () => {
    const env = parseEnvelope(
      wireFor({ source: "wiki/a.md", line_range: [1, 1], curation: "curated", text: "s" })
    );
    expect(env.chunks[0]!.node_id).toBeUndefined();
  });

  it("rejects non-string node_id", () => {
    expect(() =>
      parseEnvelope(
        wireFor({ source: "x", line_range: [1, 1], curation: "curated", text: "", node_id: 7 })
      )
    ).toThrow(/node_id/);
  });

  it("rejects heading_path containing non-strings", () => {
    expect(() =>
      parseEnvelope(
        wireFor({ source: "x", line_range: [1, 1], curation: "curated", text: "", heading_path: ["a", 2] })
      )
    ).toThrow(/heading_path/);
  });

  it("rejects node_kind outside page|section", () => {
    expect(() =>
      parseEnvelope(
        wireFor({ source: "x", line_range: [1, 1], curation: "curated", text: "", node_kind: "blob" })
      )
    ).toThrow(/node_kind/);
  });

  it("accepts policy with tree_root and nav_trace", () => {
    const body = JSON.stringify({
      schema_version: "2",
      nonce: "f".repeat(32),
      policy: { tree_root: "wiki/", nav_trace: ["wiki/a.md", "wiki/b.md#x"] },
      chunks: [],
    });
    const wire = `${new TextEncoder().encode(body).length}\n${body}`;
    const env = parseEnvelope(wire);
    expect(env.policy.tree_root).toBe("wiki/");
    expect(env.policy.nav_trace).toEqual(["wiki/a.md", "wiki/b.md#x"]);
  });
});
