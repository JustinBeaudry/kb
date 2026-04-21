import { describe, it, expect, afterEach } from "bun:test";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { mkdirSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import {
  isWithin,
  assertSafeFilename,
  assertGenuineScopeDir,
  PathUnsafeError,
} from "../src/lib/path-safety";

const dirs: string[] = [];
afterEach(() => {
  for (const d of dirs.splice(0)) {
    try {
      rmSync(d, { recursive: true, force: true });
    } catch {}
  }
});

function tmp(): string {
  const d = join(tmpdir(), `cairn-path-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  mkdirSync(d, { recursive: true });
  dirs.push(d);
  return d;
}

describe("isWithin", () => {
  it("returns true for same path", () => {
    expect(isWithin("/a/b", "/a/b")).toBe(true);
  });
  it("returns true for nested paths", () => {
    expect(isWithin("/a/b/c", "/a/b")).toBe(true);
  });
  it("returns false for parent", () => {
    expect(isWithin("/a", "/a/b")).toBe(false);
  });
  it("returns false for sibling with shared prefix", () => {
    expect(isWithin("/a/bar", "/a/b")).toBe(false);
  });
  it("returns false for unrelated paths", () => {
    expect(isWithin("/x/y", "/a/b")).toBe(false);
  });
});

describe("assertSafeFilename", () => {
  it("accepts plain filenames", () => {
    expect(() => assertSafeFilename("notes.md")).not.toThrow();
    expect(() => assertSafeFilename("sub/notes.md")).not.toThrow();
  });
  it("accepts filenames containing '..' as a substring mid-segment", () => {
    expect(() => assertSafeFilename("foo..bar.md")).not.toThrow();
  });
  it("rejects absolute paths", () => {
    expect(() => assertSafeFilename("/etc/passwd")).toThrow(PathUnsafeError);
  });
  it("rejects '..' segment", () => {
    expect(() => assertSafeFilename("../secret")).toThrow(PathUnsafeError);
    expect(() => assertSafeFilename("a/../b")).toThrow(PathUnsafeError);
  });
  it("rejects backslash-separated traversal", () => {
    expect(() => assertSafeFilename("..\\secret")).toThrow(PathUnsafeError);
  });
  it("rejects null byte", () => {
    expect(() => assertSafeFilename("foo\0bar")).toThrow(PathUnsafeError);
  });
});

describe("assertGenuineScopeDir", () => {
  it("accepts a real directory inside the vault", () => {
    const vault = tmp();
    mkdirSync(join(vault, "raw"));
    expect(() => assertGenuineScopeDir(join(vault, "raw"), vault)).not.toThrow();
  });
  it("rejects a symlinked scope directory", () => {
    const vault = tmp();
    const outside = tmp();
    symlinkSync(outside, join(vault, "raw"));
    expect(() => assertGenuineScopeDir(join(vault, "raw"), vault)).toThrow(PathUnsafeError);
  });
  it("rejects a missing scope directory", () => {
    const vault = tmp();
    expect(() => assertGenuineScopeDir(join(vault, "raw"), vault)).toThrow(PathUnsafeError);
  });
  it("rejects a scope path that is a regular file", () => {
    const vault = tmp();
    writeFileSync(join(vault, "raw"), "not a dir");
    expect(() => assertGenuineScopeDir(join(vault, "raw"), vault)).toThrow(PathUnsafeError);
  });
});
