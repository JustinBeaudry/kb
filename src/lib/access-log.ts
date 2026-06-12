import { createHash } from "node:crypto";
import { join } from "node:path";
import { appendMinimalJsonl } from "./log-writer";

export type AccessLogCommand =
  | "recall"
  | "get"
  | "list-topics"
  | "read-raw"
  | "read-session"
  | "sessions";

export type WriteAuditCommand = "mark-extracted";

// Write commands have no query and return no pages/bytes, so they get their
// own entry shape instead of zero-filled read fields.
export interface WriteAuditEntry {
  timestamp: string;
  command: WriteAuditCommand;
  target_hash: string;
  exit_code: number;
}

// access-log.jsonl carries both shapes; consumers discriminate on `command`
// (read commands -> AccessLogEntry, write commands -> WriteAuditEntry).
export type AuditEntry = AccessLogEntry | WriteAuditEntry;

export interface WriteAuditInput {
  vaultPath: string;
  command: WriteAuditCommand;
  target: string;
  exit_code: number;
}

export interface AccessLogEntry {
  timestamp: string;
  command: AccessLogCommand;
  query_hash: string;
  query_len: number;
  pages_returned: number;
  bytes_returned: number;
  exit_code: number;
}

export interface AccessLogInput {
  vaultPath: string;
  command: AccessLogCommand;
  query: string;
  pages_returned: number;
  bytes_returned: number;
  exit_code: number;
}

export function hashQuery(query: string): string {
  return createHash("sha256").update(query).digest("hex").slice(0, 32);
}

export async function appendAccessLog(input: AccessLogInput): Promise<void> {
  const entry: AccessLogEntry = {
    timestamp: new Date().toISOString(),
    command: input.command,
    query_hash: hashQuery(input.query),
    query_len: input.query.length,
    pages_returned: input.pages_returned,
    bytes_returned: input.bytes_returned,
    exit_code: input.exit_code,
  };
  await appendMinimalJsonl(entry as unknown as Record<string, unknown>, {
    logPath: join(input.vaultPath, ".kb", "access-log.jsonl"),
    lockPath: join(input.vaultPath, ".kb", "access-log.jsonl.lock"),
  });
}

export async function appendWriteAudit(input: WriteAuditInput): Promise<void> {
  const entry: WriteAuditEntry = {
    timestamp: new Date().toISOString(),
    command: input.command,
    target_hash: hashQuery(input.target),
    exit_code: input.exit_code,
  };
  await appendMinimalJsonl(entry as unknown as Record<string, unknown>, {
    logPath: join(input.vaultPath, ".kb", "access-log.jsonl"),
    lockPath: join(input.vaultPath, ".kb", "access-log.jsonl.lock"),
  });
}
