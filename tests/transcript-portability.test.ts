import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { extractExcerpt } from "../src/lib/excerpt";

interface TranscriptCase {
  name: string;
  records: unknown[];
  expected: string;
}

const cases = [
  {
    name: "legacy human and assistant messages",
    records: [
      { type: "human", message: { content: "LEGACY_USER" } },
      { type: "assistant", message: { content: [{ type: "text", text: "LEGACY_ASSISTANT" }] } },
    ],
    expected: "LEGACY_USER\nLEGACY_ASSISTANT",
  },
  {
    name: "Claude user messages with text blocks",
    records: [
      { type: "user", message: { role: "user", content: [{ type: "text", text: "CLAUDE_USER" }] } },
      { type: "assistant", message: { role: "assistant", content: "CLAUDE_ASSISTANT" } },
    ],
    expected: "CLAUDE_USER\nCLAUDE_ASSISTANT",
  },
  {
    name: "Codex response_item messages",
    records: [
      {
        type: "response_item",
        payload: { type: "message", role: "user", content: [{ type: "input_text", text: "CODEX_USER" }] },
      },
      {
        type: "response_item",
        payload: { type: "message", role: "assistant", content: [{ type: "output_text", text: "CODEX_ASSISTANT" }] },
      },
    ],
    expected: "CODEX_USER\nCODEX_ASSISTANT",
  },
  {
    name: "generic role/content messages",
    records: [
      { role: "user", content: "GENERIC_USER" },
      { type: "message", role: "assistant", content: [{ type: "text", text: "GENERIC_ASSISTANT" }] },
    ],
    expected: "GENERIC_USER\nGENERIC_ASSISTANT",
  },
] satisfies TranscriptCase[];

describe("portable transcript excerpts", () => {
  let root: string;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "kb-transcript-portability-"));
  });

  afterEach(() => {
    rmSync(root, { recursive: true, force: true });
  });

  for (const fixture of cases) {
    it(`preserves ordered user and assistant text from ${fixture.name}`, async () => {
      const path = join(root, "transcript.jsonl");
      writeFileSync(path, fixture.records.map((record) => JSON.stringify(record)).join("\n"));

      const excerpt = await extractExcerpt(path);

      expect(excerpt).toEqual({ head: fixture.expected, tail: "" });
    });
  }

  it("ignores metadata, tools, and Codex event duplicates", async () => {
    const path = join(root, "transcript.jsonl");
    const records = [
      { type: "session_meta", payload: { instructions: "METADATA_SHOULD_NOT_APPEAR" } },
      { type: "event_msg", payload: { type: "user_message", message: "USER_ONCE" } },
      {
        type: "response_item",
        payload: { type: "message", role: "user", content: [{ type: "input_text", text: "USER_ONCE" }] },
      },
      { type: "response_item", payload: { type: "function_call", arguments: "TOOL_CALL_SHOULD_NOT_APPEAR" } },
      { type: "response_item", payload: { type: "function_call_output", output: "TOOL_OUTPUT_SHOULD_NOT_APPEAR" } },
      { type: "event_msg", payload: { type: "agent_message", message: "ASSISTANT_ONCE" } },
      {
        type: "response_item",
        payload: { type: "message", role: "assistant", content: [{ type: "output_text", text: "ASSISTANT_ONCE" }] },
      },
      { role: "tool", content: "GENERIC_TOOL_SHOULD_NOT_APPEAR" },
      { role: "system", content: "SYSTEM_SHOULD_NOT_APPEAR" },
    ];
    writeFileSync(path, records.map((record) => JSON.stringify(record)).join("\n") + "\n");

    const excerpt = await extractExcerpt(path);

    expect(excerpt).toEqual({ head: "USER_ONCE\nASSISTANT_ONCE", tail: "" });
  });

  it("keeps conversational text while excluding non-text content blocks", async () => {
    const path = join(root, "transcript.jsonl");
    writeFileSync(path, JSON.stringify({
      type: "user",
      message: {
        content: [
          { type: "text", text: "BEFORE_TOOL" },
          { type: "tool_result", content: "TOOL_RESULT_SHOULD_NOT_APPEAR" },
          { type: "image", source: { data: "IMAGE_SHOULD_NOT_APPEAR" } },
          { type: "text", text: "AFTER_TOOL" },
        ],
      },
    }));

    const excerpt = await extractExcerpt(path);

    expect(excerpt).toEqual({ head: "BEFORE_TOOL\nAFTER_TOOL", tail: "" });
  });

  it("preserves Unicode codepoints at the excerpt boundaries for generic messages", async () => {
    const path = join(root, "transcript.jsonl");
    writeFileSync(path, [
      JSON.stringify({ role: "user", content: "🌊".repeat(1100) }),
      JSON.stringify({ role: "assistant", content: "🪷".repeat(1100) }),
    ].join("\n"));

    const excerpt = await extractExcerpt(path);

    expect(excerpt.head).toBe("🌊".repeat(1024));
    expect(excerpt.tail).toBe("🪷".repeat(1024));
  });
});
