import { createHash } from "node:crypto";
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  readFileSync,
  readdirSync,
  renameSync,
  rmSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { basename, join } from "node:path";
import { parseFrontmatter } from "./frontmatter";
import { writeManifest, writeSummaryFrontmatter, type SessionManifest } from "./manifest";
import { MIGRATION_JOURNAL } from "./constants";
import { withLogLock, withMigrationLock } from "./lockfile";
import type { FileChange } from "./git";

export type SessionClass =
  | "legacy-error"
  | "legacy-well-formed"
  | "already-migrated"
  | "unknown";

export interface MigrationEntry {
  file: string;
  class: SessionClass;
  action: string;
  state: "classified" | "done";
}

export interface MigrationPlan {
  entries: MigrationEntry[];
  sessionsListingHash: string;
}

const LEGACY_ERROR_STRINGS = ["Prompt is too long"];

export function buildMigrationPlan(vaultPath: string): MigrationPlan {
  const sessionsDir = join(vaultPath, "sessions");
  const files = readdirSync(sessionsDir)
    .filter((entry) => entry.endsWith(".md"))
    .map((entry) => entry.normalize("NFC"))
    .sort();

  const entries = files.map((file) => {
    const klass = classifySessionFile(join(sessionsDir, file));
    return {
      file,
      class: klass,
      action: actionForClass(klass),
      state: "classified" as const,
    };
  });

  return { entries, sessionsListingHash: hashListing(files) };
}

export async function applyMigration(
  vaultPath: string,
  assumeYes: boolean,
  confirm: () => Promise<boolean> = async () => true
): Promise<MigrationPlan> {
  return withMigrationLock(vaultPath, async () => {
    ensureMigrationDirs(vaultPath);
    const journalPath = join(vaultPath, MIGRATION_JOURNAL);
    const plan = existsSync(journalPath)
      ? readJournal(journalPath, vaultPath)
      : buildMigrationPlan(vaultPath);

    if (!existsSync(journalPath)) writeJournal(journalPath, plan);
    if (!assumeYes && !(await confirm())) return plan;

    for (const entry of plan.entries) {
      if (entry.state === "done") continue;
      await applyEntry(vaultPath, entry);
      entry.state = "done";
      writeJournal(journalPath, plan);
    }

    rmSync(journalPath, { force: true });
    return plan;
  });
}

export function classifySessionFile(path: string): SessionClass {
  const raw = readFileSync(path, "utf-8");
  const { data, body } = parseFrontmatter<Record<string, unknown>>(raw);
  const trimmedBody = body.trim().normalize("NFC");

  if (LEGACY_ERROR_STRINGS.includes(trimmedBody)) return "legacy-error";
  if ("transcript_hash" in data && "manifest_hash" in data) return "already-migrated";
  if (Object.keys(data).length > 0 && /^## Summary\b/m.test(body)) {
    return "legacy-well-formed";
  }
  return "unknown";
}

function actionForClass(klass: SessionClass): string {
  switch (klass) {
    case "legacy-error":
      return "move to sessions/.trash";
    case "legacy-well-formed":
      return "convert to manifest + cached summary";
    case "already-migrated":
      return "skip";
    case "unknown":
      return "skip with warning";
  }
}

async function applyEntry(vaultPath: string, entry: MigrationEntry): Promise<void> {
  const path = join(vaultPath, "sessions", entry.file);
  if (entry.class === "legacy-error") {
    moveFile(path, join(vaultPath, "sessions", ".trash", entry.file));
    await stripLogEntry(vaultPath, entry.file);
    return;
  }

  if (entry.class === "legacy-well-formed") {
    convertWellFormed(vaultPath, path, entry.file);
  }
}

function convertWellFormed(vaultPath: string, path: string, file: string): void {
  const raw = readFileSync(path, "utf-8");
  const { data, body } = parseFrontmatter<Record<string, unknown>>(raw);
  const sessionId = stringField(data.session_id) ?? basename(file, ".md");
  const timestamp = timestampFromFile(file);
  const shortId = shortIdFromSession(sessionId, file);
  const newName = `${filenameTimestamp(timestamp)}-${shortId}.md`;
  const manifestPath = join(vaultPath, "sessions", newName);
  const summaryPath = join(vaultPath, "sessions", "summaries", newName);

  const manifest: SessionManifest = {
    session_id: sessionId,
    timestamp,
    transcript_path: null,
    transcript_hash: null,
    transcript_size: null,
    git_head: null,
    branch: stringField(data.branch),
    files_changed: fileChangesField(data.files_changed),
    excerpt: { head: "", tail: "" },
    manifest_hash: null,
  };
  const extracted = booleanField(data.extracted);
  const decisions = arrayField(data.decisions) as Array<Record<string, unknown>> | undefined;
  const openThreads = arrayField(data.open_threads) as Array<string | Record<string, unknown>> | undefined;
  const tags = arrayField(data.tags) as string[] | undefined;
  const status = stringField(data.status);

  if (extracted !== undefined) manifest.extracted = extracted;
  if (decisions) manifest.decisions = decisions;
  if (openThreads) manifest.open_threads = openThreads;
  if (tags) manifest.tags = tags;
  if (status) manifest.status = status;
  const entireCheckpoint = stringField(data.entire_checkpoint);
  if (entireCheckpoint) manifest.entire_checkpoint = entireCheckpoint;

  writeManifest(manifestPath, manifest);
  writeSummaryFrontmatter(
    summaryPath,
    {
      manifest_hash: null,
      transcript_hash: null,
      generated_at: new Date().toISOString(),
      user_edited: true,
    },
    body.trimStart()
  );
  unlinkSync(path);
}

function stringField(value: unknown): string | null {
  return typeof value === "string" && value.length > 0 ? value : null;
}

function booleanField(value: unknown): boolean | undefined {
  return typeof value === "boolean" ? value : undefined;
}

function arrayField(value: unknown): unknown[] | undefined {
  return Array.isArray(value) ? value : undefined;
}

function fileChangesField(value: unknown): FileChange[] {
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is FileChange => {
    if (typeof item !== "object" || item === null) return false;
    const record = item as Record<string, unknown>;
    return typeof record.path === "string" && typeof record.action === "string";
  });
}

function timestampFromFile(file: string): string {
  const match = file.match(/^(\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2})/);
  if (!match) return new Date().toISOString();
  return `${match[1]!.replace(/T(\d{2})-(\d{2})-(\d{2})/, "T$1:$2:$3")}.000Z`;
}

function filenameTimestamp(timestamp: string): string {
  return timestamp.slice(0, 19).replace(/:/g, "-");
}

function shortIdFromSession(sessionId: string, file: string): string {
  const hex = sessionId.replace(/[^a-fA-F0-9]/g, "").toLowerCase();
  if (hex.length >= 8) return hex.slice(0, 8);
  const fallback = basename(file, ".md").replace(/[^a-zA-Z0-9]/g, "").toLowerCase();
  return (fallback || "session").slice(0, 8).padEnd(8, "0");
}

async function stripLogEntry(vaultPath: string, file: string): Promise<void> {
  const logPath = join(vaultPath, "log.md");
  if (!existsSync(logPath)) return;
  const marker = logMarkerFromSessionFile(file);
  if (!marker) return;

  await withLogLock(vaultPath, async () => {
    const kept = readFileSync(logPath, "utf-8")
      .split("\n")
      .filter((line) => !logLineMatchesSessionMarker(line, marker))
      .join("\n");
    writeFileSync(logPath, kept.endsWith("\n") ? kept : `${kept}\n`);
  });
}

function logMarkerFromSessionFile(file: string): string | null {
  const match = basename(file, ".md").match(/-([a-zA-Z0-9]{8,})$/);
  return match?.[1] ?? null;
}

function logLineMatchesSessionMarker(line: string, marker: string): boolean {
  if (!line.startsWith("## [") || !line.includes("] session |")) return false;
  const fields = line.split("|").map((field) => field.trim());
  return fields.at(-1) === marker;
}

function ensureMigrationDirs(vaultPath: string): void {
  mkdirSync(join(vaultPath, "sessions", ".trash"), { recursive: true });
  mkdirSync(join(vaultPath, "sessions", "summaries"), { recursive: true });
  mkdirSync(join(vaultPath, ".cairn"), { recursive: true });
}

function moveFile(from: string, to: string): void {
  try {
    renameSync(from, to);
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code !== "EXDEV") throw err;
    copyFileSync(from, to);
    unlinkSync(from);
  }
}

function hashListing(files: string[]): string {
  return createHash("sha256").update(files.join("\n")).digest("hex");
}

function readJournal(path: string, vaultPath: string): MigrationPlan {
  const parsed = JSON.parse(readFileSync(path, "utf-8")) as MigrationPlan;
  if (!Array.isArray(parsed.entries) || typeof parsed.sessionsListingHash !== "string") {
    throw new Error("migration journal is corrupt; delete it only after manual vault inspection");
  }

  for (const entry of parsed.entries) {
    if (entry.state === "done") continue;
    const originalPath = join(vaultPath, "sessions", entry.file);
    if (!existsSync(originalPath)) {
      throw new Error(
        `migration journal cannot resume because ${entry.file} is missing; manual intervention needed`
      );
    }
  }

  return parsed;
}

function writeJournal(path: string, plan: MigrationPlan): void {
  const tmp = `${path}.tmp-${process.pid}-${Date.now()}`;
  writeFileSync(tmp, JSON.stringify(plan, null, 2));
  renameSync(tmp, path);
}
