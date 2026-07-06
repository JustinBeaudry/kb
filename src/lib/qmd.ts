/**
 * qmd CLI detection.
 *
 * qmd is an optional BM25 + vector search layer over markdown vaults. When
 * installed and the vault is registered as a collection, KB workflows use
 * qmd_deep_search as the primary search step.
 */
import { realpathSync } from "node:fs";
import { resolve } from "node:path";
import { isValidNodeId } from "./map/node-id";

export function isQmdOnPath(): boolean {
  return !!Bun.which("qmd");
}

export interface QmdCollection {
  name: string;
}

/**
 * Parse `qmd collection list` output into collection names. Handles the
 * block format (`kb (qmd://kb/)` followed by indented detail lines) and a
 * flat `name path` format. Headers like `Collections (3):`, indented lines,
 * and comments are skipped. The list output carries no filesystem path in
 * either format — resolve paths per collection via `qmd collection show`.
 */
export function parseQmdCollectionList(output: string): QmdCollection[] {
  const collections: QmdCollection[] = [];
  for (const line of output.split("\n")) {
    if (/^\s/.test(line)) continue;
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#") || trimmed.endsWith(":")) continue;
    const [name] = trimmed.split(/\s+/);
    if (!name) continue;
    collections.push({ name });
  }
  return collections;
}

/**
 * Extract the filesystem path from `qmd collection show <name>` output
 * (the `Path:` line). Returns null when absent.
 */
export function parseQmdCollectionShow(output: string): string | null {
  const m = output.match(/^\s*Path:\s*(.+)$/m);
  return m ? m[1]!.trim() : null;
}

async function runQmd(args: string[]): Promise<string | null> {
  try {
    const proc = Bun.spawn(["qmd", ...args], {
      stdout: "pipe",
      stderr: "ignore",
    });
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;
    if (exitCode !== 0) return null;
    return output;
  } catch {
    return null;
  }
}

/**
 * List qmd collections. Returns null if qmd is not installed or the call
 * fails.
 */
export async function listQmdCollections(): Promise<QmdCollection[] | null> {
  if (!isQmdOnPath()) return null;
  const output = await runQmd(["collection", "list"]);
  return output === null ? null : parseQmdCollectionList(output);
}

function canonicalize(path: string): string {
  const resolved = resolve(path);
  try {
    return realpathSync(resolved);
  } catch {
    return resolved;
  }
}

/**
 * Whether some qmd collection is registered over the vault path. The list
 * output never includes filesystem paths, so each collection's path is
 * resolved via `qmd collection show <name>` and compared canonically.
 * Matching on path (not collection name) keeps a `kb` collection registered
 * over a different project's vault from counting as this vault.
 */
export async function isVaultRegistered(vaultPath: string): Promise<boolean> {
  const collections = await listQmdCollections();
  if (!collections) return false;
  const target = canonicalize(vaultPath);
  for (const { name } of collections) {
    const output = await runQmd(["collection", "show", name]);
    if (output === null) continue;
    const path = parseQmdCollectionShow(output);
    if (path && canonicalize(path) === target) return true;
  }
  return false;
}

/**
 * Parse qmd search output into wiki-relative page IDs. Tokens are normalized
 * (./ stripped, wiki/ prefix injected when absent) and validated against the
 * node-ID grammar so external output can never smuggle paths outside wiki/.
 */
export function parseQmdOutput(output: string, topK = 5): string[] {
  const seen = new Set<string>();
  for (const line of output.split("\n")) {
    const m = line.match(/(\S+\.md)\b/);
    if (!m) continue;
    const token = m[1]!.replace(/^\.\//, "");
    const wikiIdx = token.indexOf("wiki/");
    const id = wikiIdx >= 0 ? token.slice(wikiIdx) : `wiki/${token}`;
    if (!isValidNodeId(id)) continue;
    seen.add(id);
    if (seen.size >= topK) break;
  }
  return [...seen];
}

const QMD_SEARCH_TIMEOUT_MS = 2500;

/**
 * Best-effort candidate hints from qmd search. Returns wiki-relative page IDs
 * parsed from the output, or null when qmd is absent, fails, or exceeds the
 * deadline — callers treat null as "no hints" and never surface an error.
 * The deadline races the COMBINED stdout read + exit so verbose output that
 * fills the pipe buffer cannot deadlock the command.
 */
export async function qmdSearchHints(query: string, topK = 5): Promise<string[] | null> {
  if (!isQmdOnPath()) return null;

  try {
    const proc = Bun.spawn(["qmd", "search", query], {
      stdin: "ignore",
      stdout: "pipe",
      stderr: "ignore",
    });
    // Read stdout through a reader we own so the timeout path can cancel our
    // end of the pipe — Response(...).text() would lock the stream and leave
    // nothing to cancel.
    const reader = proc.stdout.getReader();
    const decoder = new TextDecoder();
    let output = "";
    const readAll = (async () => {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        output += decoder.decode(value, { stream: true });
      }
      output += decoder.decode();
    })();
    readAll.catch(() => {});
    const completion = Promise.all([readAll, proc.exited]);
    completion.catch(() => {});

    let timer: ReturnType<typeof setTimeout> | undefined;
    const deadline = new Promise<null>((resolve) => {
      timer = setTimeout(() => resolve(null), QMD_SEARCH_TIMEOUT_MS);
    });
    // try/finally so the deadline timer is cleared on every exit path —
    // including when the completion chain rejects and throws past this point
    // into the outer catch. A leaked pending timer would otherwise keep the
    // event loop (and the CLI process) alive until it fires.
    try {
      const winner = await Promise.race([completion.then(() => true as const), deadline]);
      if (winner === null) {
        // SIGKILL, not SIGTERM: a qmd that traps TERM would survive and keep
        // running. Cancelling the reader closes our end of the stdout pipe so
        // neither the killed child nor an orphaned grandchild that inherited
        // the pipe can keep the CLI process alive.
        proc.kill("SIGKILL");
        await reader.cancel().catch(() => {});
        proc.unref();
        return null;
      }
      const exitCode = await proc.exited;
      if (exitCode !== 0) return null;
      return parseQmdOutput(output, topK);
    } finally {
      clearTimeout(timer);
    }
  } catch {
    return null;
  }
}

export const QMD_INSTALL_HINT = `qmd is optional — install for hybrid BM25 + vector search:

  npm install -g @tobilu/qmd
  qmd collection add <vault-path> --name kb --mask "**/*.md"
  qmd embed

Then add the MCP server to your Claude Code config:

  { "mcpServers": { "qmd": { "command": "qmd", "args": ["mcp"] } } }

See https://github.com/qntx-labs/qmd for details.`;
