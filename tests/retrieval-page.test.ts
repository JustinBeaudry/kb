import { describe, expect, it } from "bun:test";
import { parseEnvelope, type EnvelopeChunk, type EnvelopePolicy } from "../src/lib/envelope";
import { fitChunkPage, type RetrievalContinuation, type RetrievalResult } from "../src/lib/retrieval";

const policy: EnvelopePolicy = { trust: "curated", source_scope: "wiki" };
const next = (offset: number): RetrievalContinuation => ({ command: "recall", args: ["--offset", String(offset)] });
const bytes = (value: string): number => new TextEncoder().encode(value).length;

function chunk(index: number): EnvelopeChunk {
  return {
    source: `wiki/page-${String(index).padStart(2, "0")}.md`,
    line_range: [index + 1, index + 1],
    curation: "curated",
    text: `match ${index}: ${"evidence ".repeat(20)}`,
  };
}

function successful(result: RetrievalResult | undefined): Exclude<RetrievalResult, { error: string }> {
  expect(result).toBeDefined();
  if (result === undefined || "error" in result) {
    throw new Error(result === undefined ? "No retrieval result" : result.error);
  }
  return result;
}

describe("lazy retrieval pages", () => {
  for (const offset of [0, 40]) {
    it(`consumes only the skipped offset ${offset}, a bounded page, and its lookahead`, () => {
      let consumed = 0;
      function* results(): Generator<EnvelopeChunk> {
        while (true) {
          consumed++;
          if (consumed > offset + 3) throw new Error("Read beyond the page and its lookahead");
          yield chunk(consumed - 1);
        }
      }
      let fitted: RetrievalResult | undefined;

      expect(() => {
        fitted = fitChunkPage({ chunks: results(), offset, budget: 900, policy, next });
      }).not.toThrow();

      const result = successful(fitted);
      expect(consumed).toBe(offset + 3);
      expect(result.chunks).toEqual([chunk(offset), chunk(offset + 1)]);
      expect(bytes(result.wire)).toBeLessThanOrEqual(900);
      const envelope = parseEnvelope(result.wire);
      expect(envelope.chunks).toEqual(result.chunks);
      expect(envelope.policy.truncated).toBe(true);
      expect(envelope.policy.continuation).toEqual({ command: "recall", args: ["--offset", String(offset + 2)] });
    });
  }

  it("returns a complete small result even when continuation metadata would not fit", () => {
    function* results(): Generator<EnvelopeChunk> {
      yield chunk(0);
      yield chunk(1);
    }

    const result = successful(fitChunkPage({ chunks: results(), offset: 0, budget: 750, policy, next }));

    expect(result.chunks).toEqual([chunk(0), chunk(1)]);
    expect(bytes(result.wire)).toBeLessThanOrEqual(750);
    expect(parseEnvelope(result.wire).policy.truncated).toBeUndefined();
    expect(parseEnvelope(result.wire).policy.continuation).toBeUndefined();
  });

  it("returns the final ordered result without a continuation", () => {
    function* results(): Generator<EnvelopeChunk> {
      yield chunk(0);
      yield chunk(1);
      yield chunk(2);
    }

    const result = successful(fitChunkPage({ chunks: results(), offset: 2, budget: 900, policy, next }));

    expect(result.chunks).toEqual([chunk(2)]);
    expect(parseEnvelope(result.wire).policy.truncated).toBeUndefined();
    expect(parseEnvelope(result.wire).policy.continuation).toBeUndefined();
  });

  for (const count of [0, 2]) {
    it(`returns an empty final page when offset equals the ${count} available results`, () => {
      function* results(): Generator<EnvelopeChunk> {
        for (let index = 0; index < count; index++) yield chunk(index);
      }

      const result = successful(fitChunkPage({ chunks: results(), offset: count, budget: 900, policy, next }));

      expect(result.chunks).toEqual([]);
      expect(parseEnvelope(result.wire).policy.continuation).toBeUndefined();
    });
  }

  it("reports the available count when an offset exceeds a finite producer", () => {
    function* results(): Generator<EnvelopeChunk> {
      yield chunk(0);
      yield chunk(1);
    }

    const result = fitChunkPage({ chunks: results(), offset: 3, budget: 900, policy, next });

    expect(result).toEqual({ error: "--offset exceeds the 2 available results" });
  });

  it("rejects an impossible budget for an empty producer", () => {
    function* results(): Generator<EnvelopeChunk> {}

    const result = fitChunkPage({ chunks: results(), offset: 0, budget: 1, policy, next });

    expect(result).toHaveProperty("error");
    if (!("error" in result)) throw new Error("Expected the empty envelope to exceed one byte");
    expect(result.error).toMatch(/retry with --budget \d+/);
  });

  it("gives a usable budget remedy for an oversized whole result", () => {
    const oversized: EnvelopeChunk = { ...chunk(0), text: "雪🧭\\\"".repeat(400) };
    function* results(): Generator<EnvelopeChunk> {
      yield oversized;
      yield chunk(1);
    }

    const result = fitChunkPage({ chunks: results(), offset: 0, budget: 900, policy, next });

    expect(result).toHaveProperty("error");
    if (!("error" in result)) throw new Error("Expected an oversized-result error");
    expect(result.error).toContain("wiki/page-00.md:1-1");
    const remedy = /retry with --budget (\d+)/.exec(result.error);
    expect(remedy).not.toBeNull();
    const budget = Number(remedy![1]);
    expect(budget).toBeGreaterThan(900);
    const retried = successful(fitChunkPage({ chunks: results(), offset: 0, budget, policy, next }));
    expect(retried.chunks[0]).toEqual(oversized);
    expect(bytes(retried.wire)).toBeLessThanOrEqual(budget);
  });
});
