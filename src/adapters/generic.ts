import {
  finalize,
  ignore,
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
 * kb's documented generic JSONL contract for clients without a native adapter:
 *   {"role":"user"|"assistant"|"system"|"developer","content":...,"id"?:...}
 *   {"type":"message", ...same fields}
 *   {"role":"tool","tool_call_id":...,"content":...}
 *   {"type":"tool_call","id":...,"name":...,"arguments":...}
 *   {"type":"tool_result","call_id":...,"output":...,"is_error"?:boolean}
 *   {"type":"compaction"}
 * Any trust/origin field a record carries is ignored.
 */
export const GENERIC_ADAPTER_VERSION = "kb-generic-jsonl/1";

const ROLES: readonly EventRole[] = ["user", "assistant", "system", "developer"];

const id = (value: unknown): string | undefined => (typeof value === "string" && value ? value : undefined);

export function decodeEvents(line: string, source: SourceIdentity): DecodeResult {
  const record = parseRecord(line);
  if (record === "blank") return ignore("blank");
  if (!record) return malformed();
  const at = timestamp(record.timestamp);
  const done = (draft: Draft, partial: Partial<DecodeResult> = {}) =>
    result({ ...partial, events: finalize("generic", line, source, [draft]) });

  if (record.type === "compaction") return result({ ignored: "metadata", signals: ["compaction"] });

  if (record.type === "tool_call") {
    const callID = id(record.id);
    const name = typeof record.name === "string" ? record.name : "unknown";
    return done({
      kind: "tool_call",
      role: "assistant",
      completeness: callID ? "complete" : "partial",
      observedAt: at,
      nativeID: callID ? `call:${callID}` : undefined,
      toolCallID: callID,
      toolName: name,
      content: { name, arguments: json(record.arguments) },
    });
  }

  const isTool = record.type === "tool_result" || (record.type === undefined && record.role === "tool");
  if (isTool) {
    const callID = id(record.call_id) ?? id(record.tool_call_id);
    return done({
      kind: "tool_result",
      role: "tool",
      completeness: callID ? "complete" : "partial",
      observedAt: at,
      nativeID: callID ? `result:${callID}` : undefined,
      toolCallID: callID,
      content: { output: json(record.output ?? record.content), is_error: record.is_error === true },
    });
  }

  if (record.type !== undefined && record.type !== "message") return ignore("metadata");
  if (!("role" in record)) return ignore("metadata");
  if (typeof record.role !== "string") return malformed();
  const role = ROLES.find((r) => r === record.role);
  if (!role) return ignore("unsupported_record", [`unsupported_role:${record.role}`]);
  const text = textContent(record.content);
  if (!text) return malformed();
  return done(
    {
      kind: "message",
      role,
      completeness: text.partial ? "partial" : "complete",
      observedAt: at,
      nativeID: id(record.id),
      content: { text: text.text },
    },
    {
      malformed: text.malformed,
      capabilityGaps: text.partial && !text.malformed ? ["non_text_content_omitted"] : [],
    },
  );
}

export const genericAdapter: HarnessAdapter = {
  name: "generic",
  version: GENERIC_ADAPTER_VERSION,
  capabilities: {
    capture: "supported",
    restore: "unsupported",
    toolEvidence: "supported",
    compaction: "supported",
    authenticity: "submitted_only",
  },
  decodeEvents,
};
