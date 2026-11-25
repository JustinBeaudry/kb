import {
  finalize,
  ignore,
  isRecord,
  json,
  malformed,
  parseRecord,
  result,
  timestamp,
  type DecodeResult,
  type Draft,
  type HarnessAdapter,
  type SourceIdentity,
} from "./types";

/**
 * Claude Code session JSONL. Each record carries a UUID, which anchors stable
 * event IDs so a record delivered twice (resume copies, hook replays) keeps its
 * ID. Thinking blocks are hidden reasoning and never captured. Qualified
 * against synthetic fixtures only.
 */
export const CLAUDE_ADAPTER_VERSION = "claude-code-jsonl/1";

const HIDDEN = new Set(["thinking", "redacted_thinking"]);

type ToolOutput = { output: string; partial: boolean };

/** Tool result content keeps text only; images and other blobs are omitted. */
function toolOutput(content: unknown): ToolOutput | null {
  if (typeof content === "string") return { output: content, partial: false };
  if (content === undefined || content === null) return { output: "", partial: false };
  if (!Array.isArray(content)) return null;
  const texts: string[] = [];
  let partial = false;
  for (const part of content) {
    if (isRecord(part) && part.type === "text" && typeof part.text === "string") texts.push(part.text);
    else partial = true;
  }
  return { output: texts.join("\n"), partial };
}

export function decodeEvents(line: string, source: SourceIdentity): DecodeResult {
  const record = parseRecord(line);
  if (record === "blank") return ignore("blank");
  if (!record || typeof record.type !== "string") return malformed();

  if (record.type === "system") {
    return record.subtype === "compact_boundary"
      ? result({ ignored: "metadata", signals: ["compaction"] })
      : ignore("metadata");
  }
  if (record.type !== "user" && record.type !== "assistant") return ignore("metadata");
  if (record.isMeta === true) return ignore("metadata");
  if (record.isCompactSummary === true) return ignore("compaction_summary");

  const message = record.message;
  if (!isRecord(message)) return malformed();
  const role = record.type;
  const uuid = typeof record.uuid === "string" && record.uuid ? record.uuid : undefined;
  const at = timestamp(record.timestamp);
  const anchor = (index: number) => (uuid ? `${uuid}#${index}` : undefined);

  if (typeof message.content === "string") {
    return result({
      events: finalize("claude", line, source, [
        { kind: "message", role, completeness: "complete", observedAt: at, nativeID: anchor(0), content: { text: message.content } },
      ]),
    });
  }
  if (!Array.isArray(message.content)) return malformed();

  const drafts: Draft[] = [];
  const gaps = new Set<string>();
  let bad = false;
  let hidden = 0;
  message.content.forEach((block: unknown, index: number) => {
    if (!isRecord(block) || typeof block.type !== "string") {
      bad = true;
      return;
    }
    if (HIDDEN.has(block.type)) {
      hidden++;
      return;
    }
    if (block.type === "text") {
      if (typeof block.text !== "string") {
        bad = true;
        return;
      }
      drafts.push({ kind: "message", role, completeness: "complete", observedAt: at, nativeID: anchor(index), content: { text: block.text } });
      return;
    }
    if (block.type === "tool_use") {
      const id = typeof block.id === "string" && block.id ? block.id : undefined;
      const name = typeof block.name === "string" ? block.name : "unknown";
      drafts.push({
        kind: "tool_call",
        role: "assistant",
        completeness: id ? "complete" : "partial",
        observedAt: at,
        nativeID: id ? `call:${id}` : anchor(index),
        toolCallID: id,
        toolName: name,
        content: { name, arguments: json(block.input) },
      });
      return;
    }
    if (block.type === "tool_result") {
      const id = typeof block.tool_use_id === "string" && block.tool_use_id ? block.tool_use_id : undefined;
      const output = toolOutput(block.content);
      if (!output) {
        bad = true;
        return;
      }
      if (output.partial) gaps.add("non_text_content_omitted");
      drafts.push({
        kind: "tool_result",
        role: "tool",
        completeness: id && !output.partial ? "complete" : "partial",
        observedAt: at,
        nativeID: id ? `result:${id}` : anchor(index),
        toolCallID: id,
        content: { output: output.output, is_error: block.is_error === true },
      });
      return;
    }
    gaps.add("non_text_content_omitted");
  });

  // Omitted non-text parts make the surrounding conversational message partial.
  const omitted = gaps.has("non_text_content_omitted") || bad;
  const events = finalize(
    "claude",
    line,
    source,
    drafts.map((d): Draft => (omitted && d.kind === "message" ? { ...d, completeness: "partial" } : d)),
  );
  if (events.length === 0 && !bad && hidden > 0) return ignore("hidden_reasoning");
  return result({ events, malformed: bad, capabilityGaps: [...gaps] });
}

export const claudeAdapter: HarnessAdapter = {
  name: "claude",
  version: CLAUDE_ADAPTER_VERSION,
  capabilities: {
    capture: "supported",
    restore: "unverified",
    toolEvidence: "supported",
    compaction: "supported",
    authenticity: "submitted_only",
  },
  decodeEvents,
};
