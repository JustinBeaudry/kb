import { resolve } from "node:path";
import { byteLength } from "./bytes";
import { buildEnvelope, writeEnvelope, type EnvelopeChunk, type EnvelopePolicy } from "./envelope";

const DEFAULT_RETRIEVAL_BUDGET = 16 * 1024;

type NumberResult = { value: number } | { error: string };
export type RetrievalResult = { wire: string; chunks: EnvelopeChunk[] } | { error: string };

export interface RetrievalContinuation {
  command: string;
  args: string[];
}

function integer(value: string): number | null {
  if (!/^\d+$/.test(value)) return null;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) ? parsed : null;
}

/** Explicit CLI input is strict; invalid environment defaults are ignored. */
export function resolveRetrievalBudget(flag: string | undefined, legacyEnv?: string): NumberResult {
  if (flag !== undefined) {
    const value = integer(flag);
    return value !== null && value > 0
      ? { value }
      : { error: "--budget must be a positive whole number of bytes" };
  }
  for (const raw of [process.env.KB_RETRIEVAL_BUDGET, legacyEnv]) {
    if (raw === undefined) continue;
    const value = integer(raw);
    if (value !== null && value > 0) return { value };
  }
  return { value: DEFAULT_RETRIEVAL_BUDGET };
}

export function resolveRetrievalOffset(flag: string | undefined, name: string, minimum: number): NumberResult {
  if (flag === undefined) return { value: minimum };
  const value = integer(flag);
  return value !== null && value >= minimum
    ? { value }
    : { error: `--${name} must be a whole number greater than or equal to ${minimum}` };
}

export function continuation(
  command: string,
  args: { options: string[]; positionals: string[] },
  vaultPath: string,
  budget: number
): RetrievalContinuation {
  return {
    command,
    args: [
      ...args.options,
      "--budget", String(budget),
      "-p", resolve(vaultPath),
      ...(args.positionals.length > 0 ? ["--", ...args.positionals] : []),
    ],
  };
}

export function retrievalWire(chunks: EnvelopeChunk[], policy: EnvelopePolicy): string {
  return writeEnvelope(buildEnvelope({ policy, chunks }));
}

export function checkWireBudget(wire: string, budget: number): string | null {
  const size = byteLength(wire);
  return size <= budget ? null : `output budget is too small; retry with --budget ${size}`;
}

interface FitInput {
  budget: number;
  policy: EnvelopePolicy;
  next: (offset: number) => RetrievalContinuation;
}

/** Keep exact complete source lines; metadata and framing count toward the cap. */
export function fitLineChunk(input: FitInput & { chunk: EnvelopeChunk }): RetrievalResult {
  const { chunk, budget, policy, next } = input;
  const full = retrievalWire([chunk], policy);
  if (byteLength(full) <= budget) return { wire: full, chunks: [chunk] };

  const lines = chunk.text.split("\n");
  const slice = (count: number): EnvelopeChunk => ({
    ...chunk,
    line_range: [chunk.line_range[0], chunk.line_range[0] + count - 1],
    text: lines.slice(0, count).join("\n"),
  });
  const wireFor = (count: number): string => retrievalWire([slice(count)], {
    ...policy,
    truncated: true,
    continuation: next(chunk.line_range[0] + count),
  });
  let lo = 0;
  let hi = lines.length - 1;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (byteLength(wireFor(mid)) <= budget) lo = mid;
    else hi = mid - 1;
  }
  if (lo === 0) {
    // Leave room for extra digits in the replacement budget's continuation.
    const required = byteLength(lines.length === 1 ? full : wireFor(1)) + 64;
    return { error: `line ${chunk.line_range[0]} in ${chunk.source} cannot fit the output budget; retry with --budget ${required}` };
  }
  return { wire: wireFor(lo), chunks: [slice(lo)] };
}

/** Consume only a bounded page and lookahead, retaining an exact replay offset. */
export function fitChunkPage(input: FitInput & { chunks: Iterable<EnvelopeChunk>; offset: number }): RetrievalResult {
  const { chunks, offset, budget, policy, next } = input;
  const page: EnvelopeChunk[] = [];
  const wireFor = (included: EnvelopeChunk[]): string => retrievalWire(included, {
    ...policy,
    truncated: true,
    continuation: next(offset + included.length),
  });
  let consumed = 0;
  let fittedCount = 0;
  let fittedWire: string | undefined;
  let fullWire = retrievalWire(page, policy);

  for (const chunk of chunks) {
    if (consumed++ < offset) continue;
    page.push(chunk);
    fullWire = retrievalWire(page, policy);
    if (byteLength(fullWire) > budget) {
      if (fittedWire !== undefined) {
        return { wire: fittedWire, chunks: page.slice(0, fittedCount) };
      }
      const first = page[0]!;
      // The producer need not be exhausted to provide a conservative remedy.
      // Allow for the replacement budget's extra digits in its continuation.
      const required = byteLength(wireFor([first])) + 64;
      return { error: `result at ${first.source}:${first.line_range.join("-")} cannot fit the output budget; retry with --budget ${required}` };
    }

    const candidateWire = wireFor(page);
    if (byteLength(candidateWire) <= budget) {
      fittedCount = page.length;
      fittedWire = candidateWire;
    }
    // Continue while the full response fits: the producer may end here, in
    // which case continuation metadata must not displace complete results.
  }

  if (consumed < offset) return { error: `--offset exceeds the ${consumed} available results` };
  const error = checkWireBudget(fullWire, budget);
  return error === null ? { wire: fullWire, chunks: page } : { error };
}
