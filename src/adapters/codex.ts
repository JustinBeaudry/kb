import { createHash } from "node:crypto";
import {
  finalize,
  ignore,
  isRecord,
  json,
  malformed,
  parseRecord,
  result,
  textContent,
  timestamp,
  type DecodeResult,
  type Draft,
  type EventRole,
  type HarnessAdapter,
  type SourceIdentity,
} from "./types";

/**
 * Codex rollout JSONL. `response_item` records are canonical; `event_msg`
 * records mirror them for the UI and are ignored, so each message and tool
 * exchange is captured once. Qualified against synthetic fixtures only.
 */
export const CODEX_ADAPTER_VERSION = "codex-rollout/1";

const ROLES: readonly EventRole[] = ["user", "assistant", "system", "developer"];
const CALLS = new Set(["function_call", "custom_tool_call", "local_shell_call"]);
const OUTPUTS = new Set(["function_call_output", "custom_tool_call_output"]);
const METADATA = new Set(["session_meta", "turn_context"]);

export function decodeEvents(line: string, source: SourceIdentity): DecodeResult {
  const record = parseRecord(line);
  if (record === "blank") return ignore("blank");
  if (!record) return malformed();
  const at = timestamp(record.timestamp);
  const done = (drafts: Draft[], partial: Partial<DecodeResult> = {}) =>
    result({ ...partial, events: finalize("codex", line, source, drafts) });

  if (typeof record.type !== "string") return malformed();
  if (METADATA.has(record.type)) return ignore("metadata");
  if (record.type === "compacted") return result({ ignored: "compaction_summary", signals: ["compaction"] });
  if (record.type === "event_msg") {
    if (isRecord(record.payload) && record.payload.type === "context_compacted")
      return result({ ignored: "mirror", signals: ["compaction"] });
    return ignore("mirror");
  }
  if (record.type !== "response_item") return ignore("unsupported_record", [`unsupported_record:${record.type}`]);

  const p = record.payload;
  if (!isRecord(p) || typeof p.type !== "string") return malformed();
  if (p.type === "reasoning") return ignore("hidden_reasoning");

  if (p.type === "message") {
    if (typeof p.role !== "string") return malformed();
    const role = ROLES.find((r) => r === p.role);
    if (!role) return ignore("unsupported_record", [`unsupported_role:${p.role}`]);
    const text = textContent(p.content);
    if (!text) return malformed();
    return done(
      [
        {
          kind: "message",
          role,
          completeness: text.partial ? "partial" : "complete",
          observedAt: at,
          // Codex messages carry no native ID. Session (hashed in finalize),
          // record timestamp, role and text survive a copy into a rotated file,
          // so copied history keeps its ID. Without a timestamp, fall back to
          // the source offset.
          nativeID: at
            ? `msg:${at}:${role}:${createHash("sha256").update(text.text).digest("hex")}`
            : undefined,
          content: { text: text.text },
        },
      ],
      {
        malformed: text.malformed,
        capabilityGaps: text.partial && !text.malformed ? ["non_text_content_omitted"] : [],
      },
    );
  }

  if (CALLS.has(p.type)) {
    const callID = typeof p.call_id === "string" && p.call_id ? p.call_id : undefined;
    const name = typeof p.name === "string" ? p.name : p.type;
    return done([
      {
        kind: "tool_call",
        role: "assistant",
        completeness: callID ? "complete" : "partial",
        observedAt: at,
        nativeID: callID ? `call:${callID}` : undefined,
        toolCallID: callID,
        toolName: name,
        content: { name, arguments: json(p.arguments ?? p.input ?? p.action) },
      },
    ]);
  }

  if (OUTPUTS.has(p.type)) {
    const callID = typeof p.call_id === "string" && p.call_id ? p.call_id : undefined;
    return done([
      {
        kind: "tool_result",
        role: "tool",
        completeness: callID ? "complete" : "partial",
        observedAt: at,
        nativeID: callID ? `result:${callID}` : undefined,
        toolCallID: callID,
        content: { output: json(p.output) },
      },
    ]);
  }

  return ignore("unsupported_record", [`unsupported_record:${p.type}`]);
}

export const codexAdapter: HarnessAdapter = {
  name: "codex",
  version: CODEX_ADAPTER_VERSION,
  capabilities: {
    capture: "supported",
    restore: "unsupported",
    toolEvidence: "supported",
    compaction: "supported",
    authenticity: "submitted_only",
  },
  decodeEvents,
};
