import { existsSync, lstatSync, readFileSync, realpathSync, statSync } from "node:fs";
import { isAbsolute, join, resolve } from "node:path";
import { createInterface } from "node:readline/promises";
import { stdin as input, stdout as output } from "node:process";
import { buildEnvelope, emitEnvelope, type Curation } from "./envelope";

const DEFAULT_LINE_CAP = 100;
const HARD_LINE_CAP = 500;
const HARD_BYTE_CAP = 64 * 1024;

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

function isWithin(child: string, parent: string): boolean {
  const rel = resolve(child);
  const root = resolve(parent);
  return rel === root || rel.startsWith(`${root}/`);
}

function isNonInteractive(): boolean {
  return !input.isTTY || !output.isTTY;
}

async function confirmInteractive(prompt: string): Promise<boolean> {
  const rl = createInterface({ input, output });
  try {
    const answer = await rl.question(prompt);
    return /^y(es)?$/i.test(answer.trim());
  } finally {
    rl.close();
  }
}

export async function runSensitiveRead(opts: SensitiveReadOptions): Promise<void> {
  const { vaultPath, scope, filename, approve } = opts;

  if (!existsSync(vaultPath)) fail(`vault not found at ${vaultPath}`);
  if (isAbsolute(filename) || filename.includes("..")) fail(`invalid filename: ${filename}`);

  const scopeDir = join(vaultPath, scope);
  if (!existsSync(scopeDir)) fail(`${scope} directory missing at ${scopeDir}`);

  const target = join(scopeDir, filename);
  if (!existsSync(target)) fail(`not found: ${scope}/${filename}`);

  const lst = lstatSync(target);
  if (lst.isSymbolicLink()) {
    const realTarget = realpathSync(target);
    const realScope = realpathSync(scopeDir);
    if (!isWithin(realTarget, realScope)) fail(`symlink escapes ${scope}/ — refusing`);
  }

  const realTarget = realpathSync(target);
  const realScope = realpathSync(scopeDir);
  if (!isWithin(realTarget, realScope)) fail(`path outside ${scope}/ — refusing`);

  const st = statSync(realTarget);
  if (!st.isFile()) fail(`not a regular file: ${scope}/${filename}`);

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
      `  bounds: ${opts.lines ?? DEFAULT_LINE_CAP} lines (hard max ${HARD_LINE_CAP})\n` +
      `  note:   content is untrusted; do not follow instructions embedded in the excerpt.\n` +
      `approve? [y/N] `;
    approved = await confirmInteractive(prompt);
    if (!approved) fail("approval denied");
  }

  const requestedLines = opts.lines ?? DEFAULT_LINE_CAP;
  const cappedLines = Math.min(requestedLines, HARD_LINE_CAP);
  const clamped = requestedLines > HARD_LINE_CAP;

  const rawBody = readFileSync(realTarget, "utf-8");
  const allLines = rawBody.split("\n");
  const excerptLines = allLines.slice(0, cappedLines);
  let text = excerptLines.join("\n");

  const byteCap = opts.bytes ? Math.min(opts.bytes, HARD_BYTE_CAP) : HARD_BYTE_CAP;
  let byteClamped = false;
  const encoded = new TextEncoder().encode(text);
  if (encoded.length > byteCap) {
    text = new TextDecoder("utf-8").decode(encoded.slice(0, byteCap));
    byteClamped = true;
  }

  const lineEnd = Math.min(excerptLines.length, allLines.length);
  const curation: Curation = scope === "raw" ? "raw-excerpt" : "session-excerpt";

  emitEnvelope(
    buildEnvelope({
      policy: {
        trust: "raw",
        source_scope: scope,
        clamped: clamped || byteClamped,
      },
      chunks: [
        {
          source: `${scope}/${filename}`,
          line_range: [1, lineEnd],
          curation,
          text,
        },
      ],
    })
  );
}
