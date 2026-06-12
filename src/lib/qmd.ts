/**
 * qmd CLI detection.
 *
 * qmd is an optional BM25 + vector search layer over markdown vaults. When
 * installed and the vault is registered as a collection, KB workflows use
 * qmd_deep_search as the primary search step.
 */
import { isValidNodeId } from "./map/node-id";

export function isQmdOnPath(): boolean {
  return !!Bun.which("qmd");
}

export interface QmdCollection {
  name: string;
  path?: string;
}

/**
 * List qmd collections. Returns null if qmd is not installed or the call
 * fails. Output parsing is best-effort — qmd output format may vary by
 * version.
 */
export async function listQmdCollections(): Promise<QmdCollection[] | null> {
  if (!isQmdOnPath()) return null;

  try {
    const proc = Bun.spawn(["qmd", "collection", "list"], {
      stdout: "pipe",
      stderr: "ignore",
    });
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;
    if (exitCode !== 0) return null;

    const collections: QmdCollection[] = [];
    for (const line of output.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith("#")) continue;
      const [name, ...rest] = trimmed.split(/\s+/);
      if (!name) continue;
      collections.push({ name, path: rest.join(" ") || undefined });
    }
    return collections;
  } catch {
    return null;
  }
}

/**
 * Whether a vault path appears in the qmd collection list.
 */
export async function isVaultRegistered(vaultPath: string): Promise<boolean> {
  const collections = await listQmdCollections();
  if (!collections) return false;
  return collections.some((c) => c.path === vaultPath);
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
      stdout: "pipe",
      stderr: "ignore",
    });
    const completion = Promise.all([new Response(proc.stdout).text(), proc.exited]);
    completion.catch(() => {});

    let timer: ReturnType<typeof setTimeout> | undefined;
    const deadline = new Promise<null>((resolve) => {
      timer = setTimeout(() => resolve(null), QMD_SEARCH_TIMEOUT_MS);
    });
    const winner = await Promise.race([completion, deadline]);
    clearTimeout(timer);
    if (winner === null) {
      proc.kill();
      // An orphaned grandchild (e.g. a sleep spawned by a wrapper script) can
      // inherit the stdout pipe; unref so it cannot keep our process alive.
      proc.unref();
      return null;
    }
    const [output, exitCode] = winner;
    if (exitCode !== 0) return null;
    return parseQmdOutput(output, topK);
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
