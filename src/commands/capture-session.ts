import { appendFileSync, existsSync, mkdirSync, readdirSync, renameSync, statSync, writeFileSync } from "node:fs";
import { copyFileSync, unlinkSync } from "node:fs";
import { join } from "node:path";
import { defineCommand } from "citty";
import { resolveVaultPath } from "../lib/vault";
import { CAPTURE_ERRORS_LOG, VAULT_DIRS } from "../lib/constants";
import { extractExcerpt } from "../lib/excerpt";
import { currentBranch, filesChangedSince, headCommit, uncommittedChanges } from "../lib/git";
import { sha256File } from "../lib/hash";
import { withExclusiveLock, withLogLock } from "../lib/lockfile";
import {
  computeManifestHash,
  readManifest,
  serializeSessionManifest,
  shortSessionId,
  type SessionManifest,
} from "../lib/manifest";
import { getHeadCheckpointId } from "../lib/entire";
import type { FileChange } from "../lib/git";

interface HookInput {
  session_id?: unknown;
  transcript_path?: unknown;
}

const TRANSCRIPT_STABLE_MS = 500;

export default defineCommand({
  meta: {
    name: "capture-session",
    description: "Capture a Claude Code Stop event as a Cairn session manifest",
  },
  async run() {
    const vaultPath = resolveVaultPath(process.cwd());
    ensureCapturePaths(vaultPath);

    try {
      const input = await readHookInput();
      const sessionId = parseSessionId(input);
      const lockName = `${sessionId.replace(/[^a-zA-Z0-9_.-]/g, "_")}.lock`;
      const lockPath = join(vaultPath, ".cairn", "sessions", lockName);

      await withExclusiveLock(lockPath, async () => {
        if (manifestExistsForSession(vaultPath, sessionId)) return;

        const manifest = await buildManifest(vaultPath, sessionId, input);
        manifest.manifest_hash = computeManifestHash(manifest);

        const manifestPath = join(
          vaultPath,
          "sessions",
          `${filenameTimestamp(manifest.timestamp)}-${shortSessionId(sessionId)}.md`
        );
        writeTextAtomic(manifestPath, serializeSessionManifest(manifest));
        await appendSessionLog(vaultPath, manifest);
        console.log(manifestPath);
      });
    } catch (err) {
      appendCaptureError(vaultPath, err);
      console.error(err instanceof Error ? err.message : String(err));
      process.exit(1);
    }
  },
});

async function readHookInput(): Promise<HookInput> {
  const raw = await new Response(Bun.stdin.stream()).text();
  try {
    return JSON.parse(raw) as HookInput;
  } catch (cause) {
    throw new Error(`capture-session expected JSON on stdin: ${errorMessage(cause)}`);
  }
}

function parseSessionId(input: HookInput): string {
  if (typeof input.session_id !== "string" || input.session_id.trim().length === 0) {
    throw new Error("capture-session requires a non-empty session_id");
  }
  return input.session_id;
}

async function buildManifest(
  vaultPath: string,
  sessionId: string,
  input: HookInput
): Promise<SessionManifest> {
  const timestamp = new Date().toISOString();
  const transcriptPath =
    typeof input.transcript_path === "string" && input.transcript_path.length > 0
      ? input.transcript_path
      : null;
  const transcriptInfo = await readTranscriptInfo(transcriptPath);
  const gitHead = await headCommit(process.cwd());
  const branch = await currentBranch(process.cwd());
  const entireCheckpoint = await getHeadCheckpointId(process.cwd());
  const filesChanged = await filesChangedForCapture(entireCheckpoint, process.cwd());

  const manifest: SessionManifest = {
    session_id: sessionId,
    timestamp,
    transcript_path: transcriptPath,
    transcript_hash: transcriptInfo.hash,
    transcript_size: transcriptInfo.size,
    git_head: gitHead,
    branch,
    files_changed: filesChanged,
    excerpt: transcriptInfo.excerpt,
    extracted: false,
    decisions: [],
    open_threads: [],
    tags: [],
  };

  if (entireCheckpoint) manifest.entire_checkpoint = entireCheckpoint;
  if (transcriptInfo.incomplete) manifest.excerpt_incomplete = true;

  // Ensure the directory exists even in partially initialized vaults.
  mkdirSync(join(vaultPath, "sessions"), { recursive: true });

  return manifest;
}

async function readTranscriptInfo(path: string | null): Promise<{
  hash: string | null;
  size: number | null;
  excerpt: { head: string; tail: string };
  incomplete: boolean;
}> {
  if (!path || !existsSync(path)) {
    return { hash: null, size: null, excerpt: { head: "", tail: "" }, incomplete: false };
  }

  const incomplete = await waitForStableTranscript(path);
  const size = safeSize(path);
  const [hash, excerpt] = await Promise.all([sha256File(path), extractExcerpt(path)]);
  return { hash, size, excerpt, incomplete };
}

async function waitForStableTranscript(path: string): Promise<boolean> {
  const stableMs = parsePositiveInt(
    process.env.CAIRN_TRANSCRIPT_STABLE_MS,
    TRANSCRIPT_STABLE_MS
  );
  const first = safeMtimeMs(path);
  if (first === null) return false;
  await sleep(stableMs);
  const second = safeMtimeMs(path);
  if (second === null || second === first) return false;
  await sleep(stableMs);
  const third = safeMtimeMs(path);
  return third !== null && third !== second;
}

function safeMtimeMs(path: string): number | null {
  try {
    return statSync(path).mtimeMs;
  } catch {
    return null;
  }
}

function safeSize(path: string): number | null {
  try {
    return statSync(path).size;
  } catch {
    return null;
  }
}

function parsePositiveInt(value: string | undefined, fallback: number): number {
  if (!value) return fallback;
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function filesChangedForCapture(
  entireCheckpoint: string | null,
  cwd: string
): Promise<FileChange[]> {
  if (entireCheckpoint && /^[0-9a-f]{7,40}$/i.test(entireCheckpoint)) {
    return filesChangedSince(entireCheckpoint, cwd);
  }
  return uncommittedChanges(cwd);
}

function manifestExistsForSession(vaultPath: string, sessionId: string): boolean {
  const sessionsDir = join(vaultPath, "sessions");
  if (!existsSync(sessionsDir)) return false;

  const entries = readdirSync(sessionsDir, { withFileTypes: true }).filter(
    (entry) => entry.isFile() && entry.name.endsWith(".md")
  );
  const suffix = manifestFilenameSuffix(sessionId);
  const candidates = suffix ? entries.filter((entry) => entry.name.endsWith(suffix)) : entries;
  if (suffix && candidates.length === 0) return false;

  for (const entry of candidates) {
    try {
      const manifest = readManifest(join(sessionsDir, entry.name));
      if (manifest.session_id === sessionId) return true;
    } catch {
      // Legacy session files are ignored by the manifest idempotency scan.
    }
  }
  return false;
}

function manifestFilenameSuffix(sessionId: string): string | null {
  try {
    return `-${shortSessionId(sessionId)}.md`;
  } catch {
    return null;
  }
}

async function appendSessionLog(
  vaultPath: string,
  manifest: SessionManifest
): Promise<void> {
  const today = manifest.timestamp.slice(0, 10);
  const branch = manifest.branch ?? "unknown-branch";
  const line = `\n## [${today}] session | ${branch} | ${manifest.files_changed.length} files | ${shortSessionId(manifest.session_id)}\n`;
  await withLogLock(vaultPath, async () => {
    const logPath = join(vaultPath, "log.md");
    if (!existsSync(logPath)) writeFileSync(logPath, "# Vault Log\n");
    appendFileSync(logPath, line);
  });
}

function ensureCapturePaths(vaultPath: string): void {
  mkdirSync(vaultPath, { recursive: true });
  for (const dir of VAULT_DIRS) mkdirSync(join(vaultPath, dir), { recursive: true });
  mkdirSync(join(vaultPath, ".cairn", "sessions"), { recursive: true });
  if (!existsSync(join(vaultPath, "log.md"))) writeFileSync(join(vaultPath, "log.md"), "# Vault Log\n");
}

function appendCaptureError(vaultPath: string, err: unknown): void {
  mkdirSync(join(vaultPath, ".cairn"), { recursive: true });
  const payload = {
    ts: new Date().toISOString(),
    error: errorMessage(err),
    stack: err instanceof Error ? err.stack : undefined,
  };
  appendFileSync(join(vaultPath, CAPTURE_ERRORS_LOG), `${JSON.stringify(payload)}\n`);
}

function writeTextAtomic(path: string, content: string): void {
  const tmp = `${path}.tmp-${process.pid}-${Date.now()}`;
  writeFileSync(tmp, content);
  try {
    renameSync(tmp, path);
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code !== "EXDEV") throw err;
    copyFileSync(tmp, path);
    unlinkSync(tmp);
  }
}

function serializeDateParts(date: string): string {
  return date.slice(0, 19).replace(/:/g, "-");
}

function filenameTimestamp(timestamp: string): string {
  return serializeDateParts(timestamp);
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
