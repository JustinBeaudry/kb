import { createReadStream } from "node:fs";
import { decodeTranscriptLine } from "./transcript";

export interface Excerpt {
  head: string;
  tail: string;
}

const CODEPOINT_LIMIT = 1024;

/**
 * Extracts head (first ~1024 codepoints) and tail (last ~1024 codepoints)
 * of user/assistant text from a supported JSONL transcript. Streaming —
 * memory-bounded regardless of transcript size.
 */
export async function extractExcerpt(path: string): Promise<Excerpt> {
  let head = "";
  let headCodepoints = 0;
  let tail = "";
  let totalCodepoints = 0;
  let fullParts: string[] | null = [];
  let seenTurn = false;

  const stream = createReadStream(path, { encoding: "utf-8" });
  let buffer = "";

  try {
    for await (const chunk of stream) {
      buffer += typeof chunk === "string" ? chunk : chunk.toString("utf-8");
      let newlineIdx: number;
      while ((newlineIdx = buffer.indexOf("\n")) !== -1) {
        const line = buffer.slice(0, newlineIdx);
        buffer = buffer.slice(newlineIdx + 1);
        ({ head, headCodepoints, tail, totalCodepoints, fullParts, seenTurn } =
          processLine(line, {
            head,
            headCodepoints,
            tail,
            totalCodepoints,
            fullParts,
            seenTurn,
          }));
      }
    }
    if (buffer.length > 0) {
      ({ head, headCodepoints, tail, totalCodepoints, fullParts, seenTurn } =
        processLine(buffer, {
          head,
          headCodepoints,
          tail,
          totalCodepoints,
          fullParts,
          seenTurn,
        }));
    }
  } catch (err) {
    if (err instanceof Error && "code" in err && err.code === "ENOENT") {
      return { head: "", tail: "" };
    }
    throw err;
  }

  if (fullParts !== null) {
    return { head: fullParts.join("\n"), tail: "" };
  }

  return {
    head,
    tail: sliceFromEnd(tail, CODEPOINT_LIMIT),
  };
}

interface ExcerptState {
  head: string;
  headCodepoints: number;
  tail: string;
  totalCodepoints: number;
  fullParts: string[] | null;
  seenTurn: boolean;
}

function processLine(line: string, state: ExcerptState): ExcerptState {
  const { turn } = decodeTranscriptLine(line);
  return turn ? appendTurn(state, turn.text) : state;
}

function appendTurn(state: ExcerptState, text: string): ExcerptState {
  const separator = state.seenTurn ? "\n" : "";
  const segment = `${separator}${text}`;
  const segmentCodepoints = [...segment].length;
  const nextTotal = state.totalCodepoints + segmentCodepoints;

  let nextHead = state.head;
  let nextHeadCodepoints = state.headCodepoints;
  if (nextHeadCodepoints < CODEPOINT_LIMIT) {
    const remaining = CODEPOINT_LIMIT - nextHeadCodepoints;
    nextHead += sliceCodepoints(segment, 0, remaining);
    nextHeadCodepoints = [...nextHead].length;
  }

  let nextFullParts = state.fullParts;
  if (nextFullParts !== null) {
    if (nextTotal <= CODEPOINT_LIMIT * 2) {
      nextFullParts = [...nextFullParts, text];
    } else {
      nextFullParts = null;
    }
  }

  return {
    head: nextHead,
    headCodepoints: nextHeadCodepoints,
    tail: sliceFromEnd(`${state.tail}${segment}`, CODEPOINT_LIMIT * 2),
    totalCodepoints: nextTotal,
    fullParts: nextFullParts,
    seenTurn: true,
  };
}

function sliceCodepoints(s: string, start: number, end: number): string {
  const out: string[] = [];
  let i = 0;
  for (const ch of s) {
    if (i >= start && i < end) out.push(ch);
    i++;
    if (i >= end) break;
  }
  return out.join("");
}

function sliceFromEnd(s: string, limit: number): string {
  const cps: string[] = [];
  for (const ch of s) cps.push(ch);
  if (cps.length <= limit) return cps.join("");
  return cps.slice(cps.length - limit).join("");
}
