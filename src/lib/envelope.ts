import { randomBytes } from "node:crypto";

export type Curation = "curated" | "raw-excerpt" | "session-excerpt";

export interface EnvelopeChunk {
  source: string;
  line_range: [number, number];
  curation: Curation;
  text: string;
}

export interface EnvelopePolicy {
  trust?: "curated" | "raw";
  source_scope?: "wiki" | "raw" | "sessions";
  no_results?: boolean;
  suggestions?: string[];
  [key: string]: unknown;
}

export interface Envelope {
  schema_version: "1";
  nonce: string;
  policy: EnvelopePolicy;
  chunks: EnvelopeChunk[];
}

export interface BuildEnvelopeInput {
  policy: EnvelopePolicy;
  chunks: EnvelopeChunk[];
}

export function buildEnvelope(input: BuildEnvelopeInput): Envelope {
  return {
    schema_version: "1",
    nonce: randomBytes(16).toString("hex"),
    policy: input.policy,
    chunks: input.chunks,
  };
}

export function writeEnvelope(envelope: Envelope): string {
  const body = JSON.stringify(envelope);
  const bytes = new TextEncoder().encode(body).length;
  return `${bytes}\n${body}`;
}

export function emitEnvelope(envelope: Envelope): void {
  process.stdout.write(writeEnvelope(envelope));
}

export class EnvelopeVersionError extends Error {
  constructor(public readonly version: unknown) {
    super(`envelope: unsupported schema_version: ${String(version)}`);
    this.name = "EnvelopeVersionError";
  }
}

function assertValidShape(parsed: unknown): asserts parsed is Envelope {
  if (!parsed || typeof parsed !== "object") {
    throw new Error("envelope: body is not an object");
  }
  const p = parsed as Record<string, unknown>;
  if (p.schema_version !== "1") {
    throw new EnvelopeVersionError(p.schema_version);
  }
  if (typeof p.nonce !== "string" || !/^[0-9a-f]{32}$/.test(p.nonce)) {
    throw new Error("envelope: nonce must be 32-hex string");
  }
  if (!p.policy || typeof p.policy !== "object") {
    throw new Error("envelope: policy must be an object");
  }
  if (!Array.isArray(p.chunks)) {
    throw new Error("envelope: chunks must be an array");
  }
  for (const [i, chunk] of p.chunks.entries()) {
    if (!chunk || typeof chunk !== "object") {
      throw new Error(`envelope: chunks[${i}] is not an object`);
    }
    const c = chunk as Record<string, unknown>;
    if (typeof c.source !== "string") throw new Error(`envelope: chunks[${i}].source missing`);
    if (typeof c.text !== "string") throw new Error(`envelope: chunks[${i}].text missing`);
    if (
      c.curation !== "curated" &&
      c.curation !== "raw-excerpt" &&
      c.curation !== "session-excerpt"
    ) {
      throw new Error(`envelope: chunks[${i}].curation invalid`);
    }
    if (
      !Array.isArray(c.line_range) ||
      c.line_range.length !== 2 ||
      typeof c.line_range[0] !== "number" ||
      typeof c.line_range[1] !== "number"
    ) {
      throw new Error(`envelope: chunks[${i}].line_range must be [number, number]`);
    }
  }
}

export function parseEnvelope(wire: string): Envelope {
  const nl = wire.indexOf("\n");
  if (nl < 0) throw new Error("envelope: missing length prefix");
  const prefix = wire.slice(0, nl);
  if (!/^\d+$/.test(prefix)) throw new Error(`envelope: malformed prefix: ${prefix}`);
  const declared = Number(prefix);
  const body = wire.slice(nl + 1);
  const actual = new TextEncoder().encode(body).length;
  if (actual !== declared) {
    throw new Error(`envelope: length mismatch (declared=${declared} actual=${actual})`);
  }
  const parsed: unknown = JSON.parse(body);
  assertValidShape(parsed);
  return parsed;
}
