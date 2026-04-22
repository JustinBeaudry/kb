import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { parseFrontmatter, serializeFrontmatter } from "./frontmatter";
import type { FileChange } from "./git";

export interface SessionExcerpt {
  head: string;
  tail: string;
}

export interface SessionManifest {
  session_id: string;
  timestamp: string;
  transcript_path: string | null;
  transcript_hash: string | null;
  transcript_size: number | null;
  git_head: string | null;
  branch: string | null;
  files_changed: FileChange[];
  excerpt: SessionExcerpt;

  entire_checkpoint?: string;
  excerpt_incomplete?: boolean;
  manifest_hash?: string | null;

  extracted?: boolean;
  tags?: string[];
  decisions?: Array<Record<string, unknown>>;
  open_threads?: Array<string | Record<string, unknown>>;
  status?: string;
}

export interface SessionSummaryFrontmatter {
  manifest_hash: string | null;
  transcript_hash: string | null;
  generated_at: string;
  degraded?: boolean;
  chunked?: boolean;
  user_edited?: boolean;
  truncated_turns?: number;
}

const REQUIRED_MANIFEST_KEYS: ReadonlyArray<keyof SessionManifest> = [
  "session_id",
  "timestamp",
  "transcript_path",
  "transcript_hash",
  "transcript_size",
  "git_head",
  "branch",
  "files_changed",
  "excerpt",
];

export function shortSessionId(uuid: string): string {
  const stripped = uuid.replace(/-/g, "");
  if (stripped.length < 8) {
    throw new Error(
      `session_id is too short to shorten: expected at least 8 hex chars, got ${stripped.length}`
    );
  }
  return stripped.slice(0, 8);
}

function sortForHash(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(sortForHash);
  if (value === null || typeof value !== "object") return value;

  const sorted: Record<string, unknown> = {};
  for (const key of Object.keys(value).sort()) {
    sorted[key] = sortForHash((value as Record<string, unknown>)[key]);
  }
  return sorted;
}

export function computeManifestHash(manifest: SessionManifest): string {
  const {
    excerpt: _excerpt,
    excerpt_incomplete: _excerptIncomplete,
    manifest_hash: _manifestHash,
    extracted: _extracted,
    tags: _tags,
    decisions: _decisions,
    open_threads: _openThreads,
    status: _status,
    ...hashable
  } = manifest;

  return createHash("sha256")
    .update(JSON.stringify(sortForHash(hashable)))
    .digest("hex");
}

export function validateManifest(
  data: unknown
): asserts data is SessionManifest {
  if (data === null || typeof data !== "object") {
    throw new Error("manifest must be an object");
  }
  const obj = data as Record<string, unknown>;

  for (const key of REQUIRED_MANIFEST_KEYS) {
    if (!(key in obj)) {
      throw new Error(`manifest is missing required key: ${key}`);
    }
  }

  if (!Array.isArray(obj.files_changed)) {
    throw new Error("manifest.files_changed must be an array");
  }

  const excerpt = obj.excerpt as Record<string, unknown> | null;
  if (
    !excerpt ||
    typeof excerpt !== "object" ||
    typeof excerpt.head !== "string" ||
    typeof excerpt.tail !== "string"
  ) {
    throw new Error("manifest.excerpt must be an object with string head and tail");
  }
}

export function readManifest(path: string): SessionManifest {
  const content = readFileSync(path, "utf-8");
  const { data } = parseFrontmatter<SessionManifest>(content);
  validateManifest(data);
  return data;
}

export function writeManifest(path: string, manifest: SessionManifest): void {
  validateManifest(manifest);
  const content = serializeSessionManifest(manifest);
  writeFileSync(path, content);
}

export function serializeSessionManifest(manifest: SessionManifest): string {
  validateManifest(manifest);
  return serializeFrontmatter(manifest as unknown as Record<string, unknown>, "");
}

export function readSummaryFrontmatter(path: string): {
  data: SessionSummaryFrontmatter;
  body: string;
} {
  const content = readFileSync(path, "utf-8");
  const { data, body } = parseFrontmatter<SessionSummaryFrontmatter>(content);
  return { data, body };
}

export function writeSummaryFrontmatter(
  path: string,
  frontmatter: SessionSummaryFrontmatter,
  body: string
): void {
  const content = serializeFrontmatter(
    frontmatter as unknown as Record<string, unknown>,
    body
  );
  writeFileSync(path, content);
}
