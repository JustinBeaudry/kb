export interface TranscriptTurn {
  role: "user" | "assistant";
  text: string;
}

export interface DecodedTranscriptRecord {
  turn: TranscriptTurn | null;
  malformed: boolean;
}

/** Decode conversation text only. Tool, metadata, and mirrored event records are ignored. */
export function decodeTranscriptLine(line: string): DecodedTranscriptRecord {
  if (line.trim().length === 0) return { turn: null, malformed: false };

  let record: unknown;
  try {
    record = JSON.parse(line);
  } catch {
    return { turn: null, malformed: true };
  }
  if (!isRecord(record)) return { turn: null, malformed: true };

  if (record.type === "human" || record.type === "user" || record.type === "assistant") {
    const role = record.type === "assistant" ? "assistant" : "user";
    return decodeContent(role, isRecord(record.message) ? record.message.content : undefined);
  }

  if (record.type === "response_item") {
    if (!isRecord(record.payload) || record.payload.type !== "message") {
      return { turn: null, malformed: false };
    }
    if (typeof record.payload.role !== "string") return { turn: null, malformed: true };
    if (record.payload.role === "user" || record.payload.role === "assistant") {
      return decodeContent(record.payload.role, record.payload.content);
    }
    return { turn: null, malformed: false };
  }

  if (
    (record.type === "message" || (record.type === undefined && "role" in record)) &&
    typeof record.role !== "string"
  ) {
    return { turn: null, malformed: true };
  }

  // Generic records are deliberately narrow: a metadata record carrying a
  // role/content field is not itself a conversation message.
  if (
    (record.type === undefined || record.type === "message") &&
    (record.role === "user" || record.role === "assistant")
  ) {
    return decodeContent(record.role, record.content);
  }
  return { turn: null, malformed: false };
}

function decodeContent(role: TranscriptTurn["role"], content: unknown): DecodedTranscriptRecord {
  if (typeof content === "string") {
    return { turn: content.trim().length > 0 ? { role, text: content } : null, malformed: false };
  }
  if (!Array.isArray(content)) return { turn: null, malformed: true };

  const text: string[] = [];
  let malformed = false;
  for (const part of content) {
    if (!isRecord(part) || typeof part.type !== "string") {
      malformed = true;
      continue;
    }
    if (part.type !== "text" && part.type !== "input_text" && part.type !== "output_text") continue;
    if (typeof part.text !== "string") {
      malformed = true;
    } else if (part.text.trim().length > 0) {
      text.push(part.text);
    }
  }
  return { turn: text.length > 0 ? { role, text: text.join("\n") } : null, malformed };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
