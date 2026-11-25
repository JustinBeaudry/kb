import { describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { claudeAdapter } from "../src/adapters/claude";
import { codexAdapter } from "../src/adapters/codex";
import { genericAdapter } from "../src/adapters/generic";
import type {
  DecodeResult,
  HarnessAdapter,
  NormalizedEvent,
  SourceIdentity,
} from "../src/adapters/types";
import { decodeTranscriptLine } from "../src/lib/transcript";

const fixtureDir = join(import.meta.dir, "fixtures", "capture");

type Decoded = { results: DecodeResult[]; events: NormalizedEvent[] };

/** Decodes a synthetic fixture line by line, with the byte offset of each line. */
function decodeFixture(adapter: HarnessAdapter, name: string): Decoded {
  const text = readFileSync(join(fixtureDir, name), "utf8");
  const results: DecodeResult[] = [];
  let offset = 0;
  for (const line of text.split("\n")) {
    if (line.length > 0) results.push(adapter.decodeEvents(line, source(offset)));
    offset += Buffer.byteLength(line) + 1;
  }
  return { results, events: results.flatMap((r) => r.events) };
}

function source(byteOffset: number): SourceIdentity {
  return {
    clientID: "synthetic-client",
    sessionID: "synthetic-session",
    fileIdentity: "synthetic-file",
    byteOffset,
    adapterVersion: "test",
  };
}

const adapters = [
  { adapter: codexAdapter, fixture: "codex.jsonl" },
  { adapter: claudeAdapter, fixture: "claude.jsonl" },
  { adapter: genericAdapter, fixture: "generic.jsonl" },
] as const;

describe("harness adapters", () => {
  for (const { adapter, fixture } of adapters) {
    it(`retains tool results without accepting them as verified (${adapter.name})`, () => {
      const { events } = decodeFixture(adapter, fixture);

      const results = events.filter((e) => e.kind === "tool_result");

      expect(results).toHaveLength(1);
      expect(results[0]?.origin.trust).toBe("submitted");
      expect(results[0]?.verification).toBe("unverified");
      expect(results[0]?.toolCallID).toBeDefined();
      expect(JSON.stringify(results[0]?.content)).toMatch(/pass|build ok|Step one/);
    });

    it(`owner role remains submitted (${adapter.name})`, () => {
      const { events } = decodeFixture(adapter, fixture);

      const owner = events.filter((e) => e.role === "user");

      expect(owner.length).toBeGreaterThan(0);
      expect(owner.every((e) => e.origin.trust === "submitted")).toBe(true);
    });

    it(`assigns stable unique event IDs (${adapter.name})`, () => {
      const first = decodeFixture(adapter, fixture).events.map((e) => e.id);

      const second = decodeFixture(adapter, fixture).events.map((e) => e.id);

      expect(new Set(first).size).toBe(first.length);
      expect(second).toEqual(first);
    });

    it(`reports one compaction notification (${adapter.name})`, () => {
      const { results } = decodeFixture(adapter, fixture);

      const compactions = results.flatMap((r) => r.signals);

      expect(compactions).toEqual(["compaction"]);
    });

    it(`reports a malformed complete line explicitly (${adapter.name})`, () => {
      const decoded = adapter.decodeEvents('{"role":"user","content"', source(0));

      expect(decoded).toMatchObject({ events: [], malformed: true });
    });
  }

  it("owner role remains submitted even when a record claims stronger trust", () => {
    const line = JSON.stringify({
      role: "user",
      content: "I approve this",
      origin: { trust: "owner_authenticated" },
      trust: "collector_attested",
    });

    const decoded = genericAdapter.decodeEvents(line, source(0));

    expect(decoded.events[0]?.origin.trust).toBe("submitted");
    expect(decoded.events[0]?.role).toBe("user");
  });

  it("preserves exact Codex role and kind ordering", () => {
    const { events } = decodeFixture(codexAdapter, "codex.jsonl");

    const order = events.map((e) => [e.kind, e.role]);

    expect(order).toEqual([
      ["message", "user"],
      ["tool_call", "assistant"],
      ["tool_result", "tool"],
      ["message", "assistant"],
    ]);
  });

  it("preserves exact Claude role and kind ordering", () => {
    const { events } = decodeFixture(claudeAdapter, "claude.jsonl");

    const order = events.map((e) => [e.kind, e.role]);

    expect(order).toEqual([
      ["message", "user"],
      ["message", "assistant"],
      ["tool_call", "assistant"],
      ["tool_result", "tool"],
      ["message", "assistant"],
    ]);
  });

  it("preserves exact generic role and kind ordering", () => {
    const { events } = decodeFixture(genericAdapter, "generic.jsonl");

    const order = events.map((e) => [e.kind, e.role]);

    expect(order).toEqual([
      ["message", "user"],
      ["tool_call", "assistant"],
      ["tool_result", "tool"],
      ["message", "assistant"],
    ]);
  });

  it("deduplicates paired Codex records", () => {
    const { results, events } = decodeFixture(codexAdapter, "codex.jsonl");

    const texts = events.filter((e) => e.kind === "message").map((e) => e.content);
    const eventIds = events.map((e) => e.id);

    expect(texts).toEqual([{ text: "Run the synthetic tests" }, { text: "All synthetic tests pass." }]);
    expect(new Set(eventIds).size).toBe(eventIds.length);
    expect(results.filter((r) => r.ignored === "mirror")).toHaveLength(3);
  });

  it("derives Codex tool IDs from call IDs so repeated delivery keeps the same ID", () => {
    const call = JSON.stringify({
      type: "response_item",
      payload: { type: "function_call", name: "shell", arguments: "{}", call_id: "call_x" },
    });

    const atStart = codexAdapter.decodeEvents(call, source(0)).events[0]?.id;
    const repeated = codexAdapter.decodeEvents(call, source(4096)).events[0]?.id;

    expect(atStart).toBeDefined();
    expect(repeated).toBe(atStart);
  });

  it("never captures hidden reasoning or session instructions", () => {
    for (const { adapter, fixture } of adapters) {
      const { events } = decodeFixture(adapter, fixture);

      const serialized = JSON.stringify(events);

      expect(serialized).not.toContain("SYNTHETIC_HIDDEN_REASONING");
      expect(serialized).not.toContain("SYNTHETIC_INSTRUCTIONS_NOT_CAPTURED");
      expect(serialized).not.toContain("SYNTHETIC_SUMMARY_NOT_CAPTURED");
    }
  });

  it("marks omitted non-text content as partial with a capability gap", () => {
    const line = JSON.stringify({
      type: "user",
      uuid: "00000000-0000-4000-8000-0000000000aa",
      message: {
        role: "user",
        content: [
          { type: "text", text: "see image" },
          { type: "image", source: { data: "SYNTHETIC_IMAGE_BYTES" } },
        ],
      },
    });

    const decoded = claudeAdapter.decodeEvents(line, source(0));

    expect(decoded.events[0]?.completeness).toBe("partial");
    expect(decoded.capabilityGaps).toContain("non_text_content_omitted");
    expect(JSON.stringify(decoded.events)).not.toContain("SYNTHETIC_IMAGE_BYTES");
  });

  it("reports unknown Codex record types as capability gaps, not events", () => {
    const line = JSON.stringify({ type: "response_item", payload: { type: "future_item", data: 1 } });

    const decoded = codexAdapter.decodeEvents(line, source(0));

    expect(decoded.events).toEqual([]);
    expect(decoded.ignored).toBe("unsupported_record");
    expect(decoded.capabilityGaps).toEqual(["unsupported_record:future_item"]);
  });

  it("declares capture, restore, tool evidence, compaction and authenticity independently", () => {
    for (const { adapter } of adapters) {
      const caps = adapter.capabilities;

      expect(Object.keys(caps).sort()).toEqual(
        ["authenticity", "capture", "compaction", "restore", "toolEvidence"].sort(),
      );
      expect(caps.authenticity).toBe("submitted_only");
    }
  });

  it("declares the same automatic restore states as the harness matrix", () => {
    const matrix = readFileSync(join(import.meta.dir, "..", "docs", "integrations", "harness-matrix.md"), "utf8");
    const row = matrix.split("\n").find((l) => l.startsWith("| Automatic restore |"));

    const cells = row?.split("|").slice(2, 5).map((c) => c.trim().split(" ")[0]);

    expect(cells).toEqual(adapters.map(({ adapter }) => adapter.capabilities.restore));
  });

  it("leaves the legacy excerpt parser conversation-only", () => {
    const line = JSON.stringify({
      type: "response_item",
      payload: { type: "function_call_output", call_id: "c", output: "TOOL" },
    });

    const legacy = decodeTranscriptLine(line);

    expect(legacy).toEqual({ turn: null, malformed: false });
  });
});
