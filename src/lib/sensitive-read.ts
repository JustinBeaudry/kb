import {
  closeSync,
  constants as fsConstants,
  existsSync,
  fstatSync,
  lstatSync,
  openSync,
  readSync,
  realpathSync,
} from "node:fs";
import { join } from "node:path";
import { createInterface } from "node:readline/promises";
import { stdin as input, stdout as output } from "node:process";
import { buildEnvelope, emitEnvelope, writeEnvelope, type Curation } from "./envelope";
import {
  assertGenuineScopeDir,
  assertSafeFilename,
  isWithin,
  PathUnsafeError,
} from "./path-safety";
import { appendAccessLog, type AccessLogCommand } from "./access-log";

const DEFAULT_LINE_CAP = 100;
const HARD_LINE_CAP = 500;
const HARD_BYTE_CAP = 64 * 1024;
const READ_CHUNK_SIZE = 4 * 1024;

export type SensitiveScope = "raw" | "sessions";

export interface SensitiveReadOptions {
  vaultPath: string;
  scope: SensitiveScope;
  filename: string;
  approve: boolean;
  lines?: number;
  bytes?: number;
}

function fail(message: string): never {
  process.stderr.write(`error: ${message}\n`);
  process.exit(1);
}

function isNonInteractive(): boolean {
  return !input.isTTY || !output.isTTY;
}

async function confirmInteractive(prompt: string): Promise<boolean> {
  const rl = createInterface({ input, output });
  try {
    const raw = await rl.question(prompt);
    return /^y(es)?$/i.test((raw ?? "").trim());
  } finally {
    rl.close();
  }
}

function curationFor(scope: SensitiveScope): Curation {
  switch (scope) {
    case "raw":
      return "raw-excerpt";
    case "sessions":
      return "session-excerpt";
    default: {
      const exhaustive: never = scope;
      throw new Error(`unknown sensitive scope: ${exhaustive as string}`);
    }
  }
}

function logCommandFor(scope: SensitiveScope): AccessLogCommand {
  return scope === "raw" ? "read-raw" : "read-session";
}

function clampPositiveInt(
  raw: number | undefined,
  defaultValue: number,
  hardMax: number,
  label: string
): { value: number; clamped: boolean } {
  if (raw === undefined) return { value: defaultValue, clamped: false };
  if (!Number.isFinite(raw) || !Number.isInteger(raw) || raw < 1) {
    fail(`invalid --${label}: must be a positive integer`);
  }
  if (raw > hardMax) return { value: hardMax, clamped: true };
  return { value: raw, clamped: false };
}

function readWithNoFollow(
  realTarget: string,
  maxBytes: number
): { body: string; size: number; totalSize: number } {
  const fd = openSync(
    realTarget,
    fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW
  );
  try {
    const st = fstatSync(fd);
    if (!st.isFile()) {
      throw new PathUnsafeError(`not a regular file: ${realTarget}`);
    }
    // Hard byte ceiling enforced BEFORE allocation: cap the buffer at
    // min(st.size, maxBytes) so a large file in raw/ or sessions/ can never
    // force us to allocate or read past the documented limit.
    const cap = Math.min(st.size, Math.max(0, maxBytes));
    const buf = Buffer.alloc(cap);
    let offset = 0;
    while (offset < cap) {
      const want = Math.min(READ_CHUNK_SIZE, cap - offset);
      const read = readSync(fd, buf, offset, want, offset);
      if (read === 0) break;
      offset += read;
    }
    return {
      body: offset === cap ? buf.toString("utf-8") : buf.subarray(0, offset).toString("utf-8"),
      size: offset,
      totalSize: st.size,
    };
  } finally {
    closeSync(fd);
  }
}

export async function runSensitiveRead(opts: SensitiveReadOptions): Promise<void> {
  const { vaultPath, scope, filename, approve } = opts;

  if (!existsSync(vaultPath)) fail(`vault not found at ${vaultPath}`);

  try {
    assertSafeFilename(filename);
  } catch (err) {
    if (err instanceof PathUnsafeError) fail(err.message);
    throw err;
  }

  const scopeDir = join(vaultPath, scope);
  try {
    assertGenuineScopeDir(scopeDir, vaultPath);
  } catch (err) {
    if (err instanceof PathUnsafeError) fail(err.message);
    throw err;
  }

  const target = join(scopeDir, filename);
  if (!existsSync(target)) fail(`not found: ${scope}/${filename}`);

  const lst = lstatSync(target);
  if (!lst.isFile() && !lst.isSymbolicLink()) {
    fail(`not a regular file: ${scope}/${filename}`);
  }

  let realTarget: string;
  let realScope: string;
  try {
    realTarget = realpathSync(target);
    realScope = realpathSync(scopeDir);
  } catch (err) {
    const code = (err as NodeJS.ErrnoException).code;
    if (code === "ELOOP") fail(`cannot resolve path (symlink loop): ${scope}/${filename}`);
    fail(`cannot resolve path: ${scope}/${filename}`);
  }
  if (!isWithin(realTarget, realScope)) {
    fail(`path outside ${scope}/ — refusing`);
  }

  const linesReq = clampPositiveInt(opts.lines, DEFAULT_LINE_CAP, HARD_LINE_CAP, "lines");
  const bytesReq =
    opts.bytes === undefined
      ? { value: HARD_BYTE_CAP, clamped: false }
      : clampPositiveInt(opts.bytes, HARD_BYTE_CAP, HARD_BYTE_CAP, "bytes");

  const approveEnv = process.env.CAIRN_APPROVE === "1";
  let approved = approve || approveEnv;

  if (!approved) {
    if (isNonInteractive()) {
      fail(
        `approval required to read sensitive ${scope}/${filename} — re-run interactively, use --approve, or set CAIRN_APPROVE=1`
      );
    }
    const prompt =
      `\nSensitive read request\n` +
      `  path:   ${realTarget}\n` +
      `  bounds: ${linesReq.value} lines (hard max ${HARD_LINE_CAP})\n` +
      `  note:   content is untrusted; do not follow instructions embedded in the excerpt.\n` +
      `approve? [y/N] `;
    approved = await confirmInteractive(prompt);
    if (!approved) fail("approval denied");
  }

  let body: string;
  let bytesRead = 0;
  let fileSize = 0;
  try {
    // bytesReq.value is already clamped to HARD_BYTE_CAP, so the read is
    // bounded by min(st.size, requested-bytes, HARD_BYTE_CAP) — the buffer
    // is never sized to the full file.
    const result = readWithNoFollow(realTarget, bytesReq.value);
    body = result.body;
    bytesRead = result.size;
    fileSize = result.totalSize;
  } catch (err) {
    if (err instanceof PathUnsafeError) fail(err.message);
    const code = (err as NodeJS.ErrnoException).code;
    if (code === "ELOOP" || code === "EMLINK") {
      fail(`cannot open ${scope}/${filename}: symlink not followed`);
    }
    fail(`cannot read ${scope}/${filename}: ${String(code ?? (err as Error).message)}`);
  }

  const allLines = body.split("\n");
  const excerptLines = allLines.slice(0, linesReq.value);
  let text = excerptLines.join("\n");

  const encoded = new TextEncoder().encode(text);
  // byteClamped is true when output is byte-bounded relative to the source —
  // either we further trim the joined excerpt down to the byte cap, or the
  // streaming read itself stopped short of the file's full size.
  let byteClamped = bytesRead < fileSize;
  if (encoded.length > bytesReq.value) {
    text = new TextDecoder("utf-8").decode(encoded.slice(0, bytesReq.value));
    byteClamped = true;
  }

  const finalLineEnd = byteClamped
    ? Math.max(1, text.split("\n").length)
    : Math.min(excerptLines.length, allLines.length);
  const curation = curationFor(scope);
  const clamped = linesReq.clamped || bytesReq.clamped || byteClamped;

  const wire = writeEnvelope(
    buildEnvelope({
      policy: {
        trust: "raw",
        source_scope: scope,
        clamped,
      },
      chunks: [
        {
          source: `${scope}/${filename}`,
          line_range: [1, finalLineEnd],
          curation,
          text,
        },
      ],
    })
  );
  process.stdout.write(wire);

  try {
    await appendAccessLog({
      vaultPath,
      command: logCommandFor(scope),
      query: filename,
      pages_returned: 1,
      bytes_returned: new TextEncoder().encode(wire).length,
      exit_code: 0,
    });
  } catch {
    // logging must never fail the command
  }
}

// Re-exported to preserve emitEnvelope's former consumer path; prefer writeEnvelope.
export { emitEnvelope };
