import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createHash } from "node:crypto";
import { sha256File } from "../src/lib/hash";

describe("sha256File", () => {
  let dir: string;

  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "cairn-hash-"));
  });

  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it("returns the sha256 digest of a small file as a hex string", async () => {
    const path = join(dir, "small.txt");
    const content = "hello cairn\n";
    writeFileSync(path, content);

    const expected = createHash("sha256").update(content).digest("hex");
    const actual = await sha256File(path);

    expect(actual).toBe(expected);
  });

  it("returns null for a missing file rather than throwing", async () => {
    const path = join(dir, "does-not-exist.txt");
    const result = await sha256File(path);
    expect(result).toBeNull();
  });

  it("returns a stable digest on a large streamed file", async () => {
    const path = join(dir, "large.bin");
    // 1.5 MB of repeating content to exercise multi-chunk streaming
    const chunk = Buffer.alloc(64 * 1024, 0x61); // 64 KB of 'a'
    const parts: Buffer[] = [];
    for (let i = 0; i < 24; i++) parts.push(chunk);
    const content = Buffer.concat(parts);
    writeFileSync(path, content);

    const expected = createHash("sha256").update(content).digest("hex");
    const actual = await sha256File(path);

    expect(actual).toBe(expected);
  });

  it("handles an empty file", async () => {
    const path = join(dir, "empty.txt");
    writeFileSync(path, "");

    const expected = createHash("sha256").update("").digest("hex");
    const actual = await sha256File(path);

    expect(actual).toBe(expected);
  });
});
