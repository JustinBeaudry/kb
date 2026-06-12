/**
 * qmd CLI detection.
 *
 * qmd is an optional BM25 + vector search layer over markdown vaults. When
 * installed and the vault is registered as a collection, KB workflows use
 * qmd_deep_search as the primary search step.
 */

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
 * Best-effort candidate hints from qmd search. Returns wiki-relative page IDs
 * parsed from the output, or null when qmd is absent or the call fails —
 * callers treat null as "no hints" and never surface an error.
 */
export async function qmdSearchHints(query: string, topK = 5): Promise<string[] | null> {
  if (!isQmdOnPath()) return null;

  try {
    const proc = Bun.spawn(["qmd", "search", query], {
      stdout: "pipe",
      stderr: "ignore",
    });
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;
    if (exitCode !== 0) return null;

    const ids: string[] = [];
    for (const line of output.split("\n")) {
      const m = line.match(/(?:^|[\s/])((?:wiki\/)?[A-Za-z0-9._-]+\.md)/);
      if (!m) continue;
      const id = m[1]!.startsWith("wiki/") ? m[1]! : `wiki/${m[1]!}`;
      if (!ids.includes(id)) ids.push(id);
      if (ids.length >= topK) break;
    }
    return ids;
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
