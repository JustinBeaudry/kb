import {
  copyFileSync,
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  renameSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { basename, isAbsolute, join, resolve } from "node:path";
import { parseFrontmatter, serializeFrontmatter } from "./frontmatter";
import { sha256File } from "./hash";
import {
  computeManifestHash,
  readManifest,
  readSummaryFrontmatter,
  writeSummaryFrontmatter,
  type SessionManifest,
  type SessionSummaryFrontmatter,
} from "./manifest";

export interface SummarizeOptions {
  force?: boolean;
  destructive?: boolean;
}

export interface SummarizeResult {
  path: string;
  cached: boolean;
  degraded: boolean;
  chunked: boolean;
  truncated_turns: number;
}

interface SourceText {
  text: string;
  degraded: boolean;
}

interface ChunkedText {
  chunks: string[];
  truncatedTurns: number;
}

const DEFAULT_CHUNK_BYTES = 60_000;
const TRUNCATION_MARKER = "[... oversized turn truncated ...]";

export async function summarizeSession(
  vaultPath: string,
  sessionArg: string,
  options: SummarizeOptions = {}
): Promise<SummarizeResult> {
  const manifestPath = resolveManifestPath(vaultPath, sessionArg);
  const manifest = readManifest(manifestPath);
  const manifestHash = computeManifestHash(manifest);
  const summaryPath = summaryPathForManifest(vaultPath, manifestPath);
  mkdirSync(join(vaultPath, "sessions", "summaries"), { recursive: true });

  const cached = readCachedSummary(summaryPath);
  if (cached && !options.force) {
    if (cached.data.user_edited === true) {
      return cachedResult(summaryPath, cached.data);
    }
    if (cached.data.manifest_hash === manifestHash) {
      return cachedResult(summaryPath, cached.data);
    }
    if (manifest.transcript_path === null && cached.body.trim().length > 0) {
      return cachedResult(summaryPath, cached.data);
    }
  }

  if (cached && options.force && !options.destructive) {
    trashSummary(vaultPath, summaryPath);
  }

  const source = await loadSourceText(manifest);
  const threshold = summarizeThreshold();
  const chunkedText = chunkSourceText(source.text, threshold);
  const body = await summarizeText(chunkedText.chunks, threshold);
  const outputBody = stripModelFrontmatter(body);

  writeTextAtomic(
    summaryPath,
    buildSummaryDocument({
      manifestHash,
      transcriptHash: manifest.transcript_hash,
      degraded: source.degraded,
      chunked: chunkedText.chunks.length > 1,
      truncatedTurns: chunkedText.truncatedTurns,
      body: outputBody,
    })
  );

  return {
    path: summaryPath,
    cached: false,
    degraded: source.degraded,
    chunked: chunkedText.chunks.length > 1,
    truncated_turns: chunkedText.truncatedTurns,
  };
}

export async function summarizeAll(
  vaultPath: string,
  options: SummarizeOptions = {},
  onProgress?: (line: string) => void
): Promise<SummarizeResult[]> {
  const sessionsDir = join(vaultPath, "sessions");
  const manifests = readdirSync(sessionsDir)
    .filter((entry) => entry.endsWith(".md"))
    .flatMap((entry) => {
      const path = join(sessionsDir, entry);
      try {
        readManifest(path);
        return [path];
      } catch {
        return [];
      }
    });
  const results: SummarizeResult[] = [];

  for (let i = 0; i < manifests.length; i++) {
    const manifestPath = manifests[i]!;
    onProgress?.(`[${i + 1}/${manifests.length}] summarizing ${basename(manifestPath)}...`);
    results.push(await summarizeSession(vaultPath, manifestPath, options));
  }

  return results;
}

export function setSummaryPinned(
  vaultPath: string,
  sessionArg: string,
  pinned: boolean
): string {
  const manifestPath = resolveManifestPath(vaultPath, sessionArg);
  const summaryPath = summaryPathForManifest(vaultPath, manifestPath);
  const { data, body } = readSummaryFrontmatter(summaryPath);
  writeSummaryFrontmatter(summaryPath, { ...data, user_edited: pinned }, body);
  return summaryPath;
}

export function resolveManifestPath(vaultPath: string, sessionArg: string): string {
  const direct = isAbsolute(sessionArg) ? sessionArg : resolve(process.cwd(), sessionArg);
  if (existsSync(direct)) return direct;

  if (sessionArg.startsWith("sessions/")) {
    const fromVault = join(vaultPath, sessionArg);
    if (existsSync(fromVault)) return fromVault;
  }

  const fromSessions = join(vaultPath, "sessions", sessionArg);
  if (existsSync(fromSessions)) return fromSessions;

  if (sessionArg.length < 8) {
    throw new Error("session id prefix must be at least 8 characters");
  }

  const matches: string[] = [];
  for (const entry of readdirSync(join(vaultPath, "sessions"))) {
    if (!entry.endsWith(".md")) continue;
    const path = join(vaultPath, "sessions", entry);
    try {
      const manifest = readManifest(path);
      if (manifest.session_id.startsWith(sessionArg)) matches.push(path);
    } catch {
      // Legacy session files are not resolvable by manifest prefix.
    }
  }

  if (matches.length === 1) return matches[0]!;
  if (matches.length > 1) {
    throw new Error(
      `ambiguous session id prefix ${sessionArg}; matches: ${matches.map((path) => basename(path)).join(", ")}`
    );
  }
  throw new Error(`no session manifest found for ${sessionArg}`);
}

function summaryPathForManifest(vaultPath: string, manifestPath: string): string {
  return join(vaultPath, "sessions", "summaries", basename(manifestPath));
}

function readCachedSummary(path: string): ReturnType<typeof readSummaryFrontmatter> | null {
  if (!existsSync(path)) return null;
  try {
    return readSummaryFrontmatter(path);
  } catch {
    return null;
  }
}

function cachedResult(path: string, data: SessionSummaryFrontmatter): SummarizeResult {
  return {
    path,
    cached: true,
    degraded: data.degraded === true,
    chunked: data.chunked === true,
    truncated_turns: data.truncated_turns ?? 0,
  };
}

async function loadSourceText(manifest: SessionManifest): Promise<SourceText> {
  if (manifest.transcript_path) {
    const currentHash = await sha256File(manifest.transcript_path);
    if (currentHash !== null && currentHash === manifest.transcript_hash) {
      return { text: extractTranscriptText(manifest.transcript_path), degraded: false };
    }
  }

  const text =
    manifest.excerpt.tail.length > 0
      ? `${manifest.excerpt.head}\n\n...\n\n${manifest.excerpt.tail}`
      : manifest.excerpt.head;
  return { text, degraded: true };
}

function extractTranscriptText(path: string): string {
  const content = readFileSync(path, "utf-8");
  const turns: string[] = [];
  for (const line of content.split("\n")) {
    if (line.trim().length === 0) continue;
    const turn = extractTurnText(line);
    if (turn) turns.push(turn);
  }
  return turns.join("\n\n");
}

function extractTurnText(line: string): string | null {
  try {
    const parsed = JSON.parse(line) as {
      type?: string;
      message?: { content?: unknown };
    };
    if (parsed.type !== "human" && parsed.type !== "assistant") return null;
    const label = parsed.type === "human" ? "User" : "Assistant";
    const text = extractContentText(parsed.message?.content);
    return text.length > 0 ? `## ${label}\n${text}` : null;
  } catch {
    return null;
  }
}

function extractContentText(content: unknown): string {
  if (typeof content === "string") return content;
  if (!Array.isArray(content)) return "";
  return content
    .filter((part): part is { type: string; text: string } => {
      if (typeof part !== "object" || part === null) return false;
      const record = part as Record<string, unknown>;
      return record.type === "text" && typeof record.text === "string";
    })
    .map((part) => part.text)
    .join("\n");
}

function summarizeThreshold(): number {
  const raw = process.env.CAIRN_SUMMARIZE_CHUNK_BYTES;
  if (!raw) return DEFAULT_CHUNK_BYTES;
  const parsed = Number.parseInt(raw, 10);
  return Number.isFinite(parsed) && parsed > 100 ? parsed : DEFAULT_CHUNK_BYTES;
}

function chunkSourceText(text: string, threshold: number): ChunkedText {
  if (Buffer.byteLength(text, "utf-8") <= threshold) {
    return { chunks: [text], truncatedTurns: 0 };
  }

  const units = text.split(/\n{2,}/);
  const chunks: string[] = [];
  let current = "";
  let truncatedTurns = 0;

  for (const rawUnit of units) {
    let unit = rawUnit;
    if (Buffer.byteLength(unit, "utf-8") > threshold) {
      unit = truncateToBytes(unit, Math.max(1, threshold - Buffer.byteLength(TRUNCATION_MARKER)));
      unit = `${unit}\n${TRUNCATION_MARKER}`;
      truncatedTurns++;
    }

    const next = current.length === 0 ? unit : `${current}\n\n${unit}`;
    if (Buffer.byteLength(next, "utf-8") <= threshold) {
      current = next;
    } else {
      if (current.length > 0) chunks.push(current);
      current = unit;
    }
  }

  if (current.length > 0) chunks.push(current);
  return { chunks: chunks.length > 0 ? chunks : [""], truncatedTurns };
}

function truncateToBytes(text: string, maxBytes: number): string {
  let out = "";
  for (const ch of text) {
    if (Buffer.byteLength(out + ch, "utf-8") > maxBytes) break;
    out += ch;
  }
  return out;
}

async function summarizeText(chunks: string[], threshold: number): Promise<string> {
  if (chunks.length === 1) return runSummarizer(singlePrompt(chunks[0]!));

  const partials: string[] = [];
  for (const chunk of chunks) partials.push(await runSummarizer(chunkPrompt(chunk)));
  return reduceSummaries(partials, threshold);
}

async function reduceSummaries(partials: string[], threshold: number): Promise<string> {
  if (partials.length === 1) return partials[0]!;

  const groups: string[][] = [];
  let current: string[] = [];
  for (const partial of partials) {
    const candidate = [...current, partial];
    if (Buffer.byteLength(reducePrompt(candidate), "utf-8") <= threshold || current.length === 0) {
      current = candidate;
    } else {
      groups.push(current);
      current = [partial];
    }
  }
  if (current.length > 0) groups.push(current);

  const reduced: string[] = [];
  for (const group of groups) reduced.push(await runSummarizer(reducePrompt(group)));
  return groups.length === 1 ? reduced[0]! : reduceSummaries(reduced, threshold);
}

async function runSummarizer(prompt: string): Promise<string> {
  const command = process.env.CAIRN_SUMMARIZE_COMMAND ?? "claude";
  const proc = Bun.spawn([command, "-p", "--model", "haiku"], {
    stdin: new Response(prompt),
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
  ]);
  const exitCode = await proc.exited;
  if (exitCode !== 0) {
    throw new Error(stderr.trim() || `summarizer exited ${exitCode}`);
  }
  return stdout.trim();
}

function singlePrompt(text: string): string {
  return `${promptPreamble()}\n\nSESSION TRANSCRIPT:\n\n${text}\n`;
}

function chunkPrompt(text: string): string {
  return `${promptPreamble()}\n\nSummarize this chunk of a larger session. Preserve concrete decisions, files, and extraction candidates.\n\nSESSION CHUNK:\n\n${text}\n`;
}

function reducePrompt(partials: string[]): string {
  return `${promptPreamble()}\n\nMerge these partial session summaries into one coherent final summary. Resolve duplicates and contradictions.\n\nPARTIAL SUMMARIES:\n\n${partials.join("\n\n---\n\n")}\n`;
}

function promptPreamble(): string {
  return `You are the Cairn session summarizer. Return markdown with "## Summary" and "## Extraction Candidates" sections. Do not wrap the output in a code fence.`;
}

function stripModelFrontmatter(output: string): string {
  try {
    const parsed = parseFrontmatter(output);
    return parsed.body.trim().length > 0 ? parsed.body : output.trim();
  } catch {
    return output.trim();
  }
}

function buildSummaryDocument(input: {
  manifestHash: string;
  transcriptHash: string | null;
  degraded: boolean;
  chunked: boolean;
  truncatedTurns: number;
  body: string;
}): string {
  const frontmatter: SessionSummaryFrontmatter = {
    manifest_hash: input.manifestHash,
    transcript_hash: input.transcriptHash,
    generated_at: new Date().toISOString(),
    degraded: input.degraded || undefined,
    chunked: input.chunked || undefined,
    truncated_turns: input.truncatedTurns > 0 ? input.truncatedTurns : undefined,
  };
  return serializeFrontmatter(
    frontmatter as unknown as Record<string, unknown>,
    `${input.body.trim()}\n`
  );
}

function trashSummary(vaultPath: string, summaryPath: string): void {
  const trashDir = join(vaultPath, "sessions", ".trash", "summaries");
  mkdirSync(trashDir, { recursive: true });
  const stamp = new Date().toISOString().slice(0, 19).replace(/:/g, "-");
  const target = join(trashDir, `${basename(summaryPath, ".md")}-${stamp}.md`);
  moveFile(summaryPath, target);
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
