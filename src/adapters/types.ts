import { createHash } from "node:crypto";
import type { Json } from "../capture/types";

/**
 * Harness adapters decode one exposed transcript line into normalized events.
 * They are pure: no file, network or credential access. Every decoded event is
 * submitted evidence; a role label or a field inside the record never makes
 * it collector-attested or owner-authenticated.
 */
export type SourceIdentity = {
  clientID: string;
  sessionID: string;
  /** Opaque identity of the source file generation (e.g. device:inode). */
  fileIdentity: string;
  /** Byte offset of the decoded line within that generation. */
  byteOffset: number;
  adapterVersion: string;
};

export type EventRole = "user" | "assistant" | "system" | "developer" | "tool";

export type NormalizedEvent = {
  /** Stable across repeated decoding and, where the harness exposes a native
   * record ID, across repeated delivery at another offset. */
  id: string;
  kind: "message" | "tool_call" | "tool_result";
  /** The role the transcript claims; never an authenticated identity. */
  role: EventRole;
  origin: { clientID: string; trust: "submitted" };
  /** Tool output is retained as evidence but never accepted as verified. */
  verification: "unverified";
  completeness: "complete" | "partial";
  observedAt?: string;
  sourceLocator: string;
  toolCallID?: string;
  toolName?: string;
  content: Json;
};

export type IgnoredReason =
  | "blank"
  | "metadata"
  | "mirror"
  | "hidden_reasoning"
  | "compaction_summary"
  | "unsupported_record";

export type Signal = "compaction";

export type DecodeResult = {
  events: NormalizedEvent[];
  ignored?: IgnoredReason;
  malformed: boolean;
  capabilityGaps: string[];
  signals: Signal[];
};

export type CapabilityState = "supported" | "unsupported" | "unverified";

/** Capabilities are reported independently; see docs/integrations/harness-matrix.md. */
export type HarnessCapabilities = {
  /** Capture through the explicitly registered watcher. */
  capture: CapabilityState;
  /** Automatic restoration into the harness; client-driven restore is harness-independent. */
  restore: CapabilityState;
  toolEvidence: CapabilityState;
  compaction: CapabilityState;
  /** No current harness provides an isolated attestation boundary. */
  authenticity: "submitted_only";
};

export interface HarnessAdapter {
  readonly name: "codex" | "claude" | "generic";
  readonly version: string;
  readonly capabilities: HarnessCapabilities;
  decodeEvents(line: string, source: SourceIdentity): DecodeResult;
}

export type Draft = Omit<NormalizedEvent, "id" | "origin" | "verification" | "sourceLocator"> & {
  /** Harness-native identity of this part (record UUID, call ID) when exposed. */
  nativeID?: string;
};

export function result(partial: Partial<DecodeResult> = {}): DecodeResult {
  return { events: [], malformed: false, capabilityGaps: [], signals: [], ...partial };
}

export const ignore = (reason: IgnoredReason, gaps: string[] = []): DecodeResult =>
  result({ ignored: reason, capabilityGaps: gaps });

export const malformed = (): DecodeResult => result({ malformed: true });

/** Finalizes drafts: assigns stable IDs, a locator and the fixed submitted origin. */
export function finalize(adapter: string, line: string, source: SourceIdentity, drafts: Draft[]): NormalizedEvent[] {
  const end = source.byteOffset + Buffer.byteLength(line);
  return drafts.map(({ nativeID, ...draft }, index) => {
    const anchor = nativeID ?? `${source.fileIdentity}@${source.byteOffset}#${index}`;
    const id =
      "evt_" +
      createHash("sha256")
        .update(JSON.stringify([adapter, source.clientID, source.sessionID, anchor, draft.kind]))
        .digest("hex")
        .slice(0, 40);
    return {
      ...draft,
      id,
      origin: { clientID: source.clientID, trust: "submitted" },
      verification: "unverified",
      sourceLocator: `${source.fileIdentity}#${source.byteOffset}-${end}`,
    };
  });
}

export function parseRecord(line: string): Record<string, unknown> | null | "blank" {
  if (line.trim().length === 0) return "blank";
  try {
    const value: unknown = JSON.parse(line);
    return isRecord(value) ? value : null;
  } catch {
    return null;
  }
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export const timestamp = (value: unknown): string | undefined =>
  typeof value === "string" && !Number.isNaN(Date.parse(value)) ? value : undefined;

/** Converts tool payloads to JSON without trusting their shape. */
export function json(value: unknown): Json {
  if (value === undefined) return null;
  try {
    // SAFETY: a JSON round trip yields only JSON-representable values.
    return JSON.parse(JSON.stringify(value)) as Json;
  } catch {
    return null;
  }
}

const TEXT_PARTS = new Set(["text", "input_text", "output_text"]);

/** Text-only message content; non-text parts are omitted and reported. */
export function textContent(content: unknown): { text: string; partial: boolean; malformed: boolean } | null {
  if (typeof content === "string") return { text: content, partial: false, malformed: false };
  if (!Array.isArray(content)) return null;
  const texts: string[] = [];
  let partial = false;
  let bad = false;
  for (const part of content) {
    if (!isRecord(part) || typeof part.type !== "string") {
      bad = true;
      continue;
    }
    if (!TEXT_PARTS.has(part.type)) {
      partial = true;
      continue;
    }
    if (typeof part.text !== "string") bad = true;
    else texts.push(part.text);
  }
  return { text: texts.join("\n"), partial: partial || bad, malformed: bad };
}
