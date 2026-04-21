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
  const parsed = JSON.parse(body) as Envelope;
  return parsed;
}
