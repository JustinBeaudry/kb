import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  readManifest,
  writeManifest,
  validateManifest,
  shortSessionId,
  readSummaryFrontmatter,
  writeSummaryFrontmatter,
  type SessionManifest,
  type SessionSummaryFrontmatter,
} from "../src/lib/manifest";

describe("shortSessionId", () => {
  it("returns the first 8 hex chars of a UUID", () => {
    expect(shortSessionId("123e4567-e89b-12d3-a456-426614174000")).toBe("123e4567");
  });

  it("returns the first 8 hex chars for an unhyphenated uuid", () => {
    expect(shortSessionId("abcdef0123456789abcdef0123456789")).toBe("abcdef01");
  });

  it("is deterministic", () => {
    const id = "123e4567-e89b-12d3-a456-426614174000";
    expect(shortSessionId(id)).toBe(shortSessionId(id));
  });

  it("rejects session ids shorter than 8 hex chars", () => {
    expect(() => shortSessionId("abc")).toThrow(/session_id/);
  });
});

function baseManifest(): SessionManifest {
  return {
    session_id: "123e4567-e89b-12d3-a456-426614174000",
    timestamp: "2026-04-19T22:57:31Z",
    transcript_path: "/Users/test/.claude/projects/foo/bar.jsonl",
    transcript_hash: "a".repeat(64),
    transcript_size: 12345,
    git_head: "b".repeat(40),
    branch: "main",
    files_changed: [
      { path: "src/foo.ts", action: "modified" },
      { path: "src/bar.ts", action: "created" },
    ],
    excerpt: { head: "hello", tail: "world" },
  };
}

describe("readManifest / writeManifest", () => {
  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "cairn-manifest-"));
  });
  afterEach(() => rmSync(dir, { recursive: true, force: true }));

  it("round-trips a full manifest with all required fields", () => {
    const path = join(dir, "2026-04-19T22-57-31-123e4567.md");
    const manifest = baseManifest();
    writeManifest(path, manifest);

    const loaded = readManifest(path);
    expect(loaded).toEqual(manifest);
  });

  it("preserves optional fields (entire_checkpoint, excerpt_incomplete, manifest_hash)", () => {
    const path = join(dir, "manifest.md");
    const manifest: SessionManifest = {
      ...baseManifest(),
      entire_checkpoint: "bd7b85159016",
      excerpt_incomplete: true,
      manifest_hash: "c".repeat(64),
    };
    writeManifest(path, manifest);
    const loaded = readManifest(path);
    expect(loaded).toEqual(manifest);
  });

  it("preserves pass-through legacy keys (extracted, decisions, tags)", () => {
    const path = join(dir, "manifest.md");
    const manifest: SessionManifest = {
      ...baseManifest(),
      extracted: false,
      tags: ["a", "b"],
      decisions: [{ choice: "x", reason: "y" }],
      open_threads: ["thread-1"],
    };
    writeManifest(path, manifest);
    const loaded = readManifest(path);
    expect(loaded.extracted).toBe(false);
    expect(loaded.tags).toEqual(["a", "b"]);
    expect(loaded.decisions).toEqual([{ choice: "x", reason: "y" }]);
    expect(loaded.open_threads).toEqual(["thread-1"]);
  });

  it("permits null transcript fields for migrated-legacy manifests", () => {
    const path = join(dir, "manifest.md");
    const manifest: SessionManifest = {
      ...baseManifest(),
      transcript_path: null,
      transcript_hash: null,
      transcript_size: null,
      excerpt: { head: "", tail: "" },
    };
    writeManifest(path, manifest);
    const loaded = readManifest(path);
    expect(loaded.transcript_path).toBeNull();
    expect(loaded.transcript_hash).toBeNull();
    expect(loaded.excerpt).toEqual({ head: "", tail: "" });
  });

  it("writes an empty body so manifests are pointer-only", () => {
    const path = join(dir, "manifest.md");
    writeManifest(path, baseManifest());
    const raw = readFileSync(path, "utf-8");
    expect(raw.split("---\n").length).toBe(3);
    expect(raw.trimEnd().endsWith("---")).toBe(true);
  });
});

describe("validateManifest", () => {
  it("accepts a complete manifest", () => {
    expect(() => validateManifest(baseManifest())).not.toThrow();
  });

  it("rejects when a required key is missing", () => {
    const m = baseManifest() as unknown as Record<string, unknown>;
    delete m.session_id;
    expect(() => validateManifest(m)).toThrow(/session_id/);
  });

  it("rejects when files_changed is not an array", () => {
    const m = { ...baseManifest(), files_changed: "not-an-array" as unknown };
    expect(() => validateManifest(m)).toThrow(/files_changed/);
  });

  it("rejects when excerpt lacks head and tail", () => {
    const m = { ...baseManifest(), excerpt: { head: "only" } as unknown };
    expect(() => validateManifest(m)).toThrow(/excerpt/);
  });
});

describe("readSummaryFrontmatter / writeSummaryFrontmatter", () => {
  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "cairn-summary-"));
  });
  afterEach(() => rmSync(dir, { recursive: true, force: true }));

  it("round-trips a summary with all optional fields", () => {
    const path = join(dir, "summary.md");
    const frontmatter: SessionSummaryFrontmatter = {
      manifest_hash: "h".repeat(64),
      transcript_hash: "t".repeat(64),
      generated_at: "2026-04-19T23:00:00Z",
      degraded: true,
      chunked: true,
      user_edited: false,
      truncated_turns: 2,
    };
    const body = "## Summary\n\nThis is the summary.\n";
    writeSummaryFrontmatter(path, frontmatter, body);

    const { data, body: loadedBody } = readSummaryFrontmatter(path);
    expect(data).toEqual(frontmatter);
    expect(loadedBody).toBe(body);
  });

  it("round-trips a minimal summary (no optional flags)", () => {
    const path = join(dir, "summary.md");
    const frontmatter: SessionSummaryFrontmatter = {
      manifest_hash: "h".repeat(64),
      transcript_hash: "t".repeat(64),
      generated_at: "2026-04-19T23:00:00Z",
    };
    writeSummaryFrontmatter(path, frontmatter, "body\n");
    const { data } = readSummaryFrontmatter(path);
    expect(data.manifest_hash).toBe("h".repeat(64));
    expect(data.degraded).toBeUndefined();
    expect(data.chunked).toBeUndefined();
  });
});
