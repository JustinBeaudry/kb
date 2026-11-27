import { createHash } from "node:crypto";
import { stringify as yamlStringify } from "yaml";
import type { ExportedClaim, ExportPage, ExportScope } from "./transfer.service";

/** One Markdown file of a synced Obsidian view, relative to the sync folder. */
export type VaultPage = { path: string; content: string };

export const INDEX_PAGE = "index.md";
const TITLE_CHARS = 60;

/**
 * Claim text is written by agents and is untrusted. In Obsidian, code blocks
 * and inline code can execute (Dataview JS and similar plugins), HTML can load
 * remote frames, and image embeds fetch remote URLs on view. Escape all three
 * so a synced page only ever renders as text and ordinary links. Backslashes
 * are escaped first so text cannot cancel the escapes that follow.
 */
export function inertMarkdown(text: string): string {
  return text.replaceAll("\\", "\\\\").replaceAll("`", "\\`").replaceAll("~", "\\~").replaceAll("<", "&lt;").replaceAll("![", "!\\[");
}

/** A readable file name: the claim's first line, without characters that
 * Obsidian, wikilinks or common file systems treat specially. */
export function claimTitle(text: string): string {
  const first = text.split(/\r?\n/).map((l) => l.trim()).find((l) => l.length > 0) ?? "";
  const cleaned = first
    .replace(/^(#+|[-*+]|>|\d+[.)])\s+/, "")
    .replace(/[\p{Cc}\p{Cf}\\/:*?"<>|#^[\]`%$!]/gu, " ")
    .replace(/\s+/g, " ")
    .trim();
  const cut = [...cleaned.replace(/^[\s.]+/, "")].slice(0, TITLE_CHARS).join("").replace(/[\s.]+$/, "");
  return cut.length > 0 ? cut : "Claim";
}

const shortID = (claimID: string) => createHash("sha256").update(claimID).digest("hex").slice(0, 8);
const tagPart = (v: string) => v.toLowerCase().replace(/[^a-z0-9_-]+/g, "-").replace(/^-+|-+$/g, "") || "other";

function statusNote(status: ExportedClaim["claim_status"]): string | undefined {
  if (status === "verified") return undefined;
  if (status === "candidate") return "> [!warning] Candidate\n> Submitted by an agent and not verified by the owner. Treat as unconfirmed.";
  return `> [!caution] ${status[0]!.toUpperCase()}${status.slice(1)}\n> The owner has not accepted this claim as current knowledge.`;
}

function claimPage(c: ExportedClaim): VaultPage & { title: string } {
  const title = claimTitle(c.text);
  const path = `claims/${title} (${shortID(c.claim_id)}).md`;
  // Same provenance fields as a `kb transfer export` bundle, so a page can be
  // submitted back as a candidate with its provenance kept as metadata.
  const front = {
    kb_export: 1,
    claim_id: c.claim_id,
    revision: c.revision,
    kind: c.kind,
    claim_status: c.claim_status,
    origin_trust: c.origin_trust,
    scope: c.scope,
    evidence: c.evidence.map((e) => ({ artifact_id: e.artifact_id, digest: e.digest, locator: e.locator, source_revision: e.source_revision, origin_trust: e.origin.trust })),
    tags: [`kb/${tagPart(c.claim_status)}`, `kb/kind/${tagPart(c.kind)}`],
  };
  const parts = [`---\n${yamlStringify(front).trimEnd()}\n---`];
  const note = statusNote(c.claim_status);
  if (note) parts.push(note);
  parts.push(inertMarkdown(c.text.trim()));
  if (c.evidence.length > 0) {
    parts.push("## Evidence\n\n" + c.evidence.map((e) => `- ${inertMarkdown(e.locator.replace(/\s+/g, " "))} (${tagPart(e.origin.trust)})`).join("\n"));
  }
  return { path, title, content: parts.join("\n\n") + "\n" };
}

const SECTIONS: { heading: string; statuses: ExportedClaim["claim_status"][] }[] = [
  { heading: "Verified", statuses: ["verified"] },
  { heading: "Candidates (unverified)", statuses: ["candidate"] },
  { heading: "Disputed, superseded or rejected", statuses: ["disputed", "superseded", "invalidated", "rejected"] },
];

/**
 * Renders a read-only Obsidian view of exported memory: one page per claim and
 * an index grouped by status. Output depends only on the claims, so a rerun
 * with unchanged memory produces identical files.
 */
export function renderVaultPages(scope: ExportScope, claims: ExportedClaim[], completeness: ExportPage["completeness"]): VaultPage[] {
  const sorted = [...claims].sort((a, b) => (a.claim_id < b.claim_id ? -1 : a.claim_id > b.claim_id ? 1 : 0));
  const pages = sorted.map((c) => ({ ...claimPage(c), status: c.claim_status, kind: c.kind }));
  const lines = [
    `---\n${yamlStringify({ kb_vault_index: 1, scope }).trimEnd()}\n---`,
    `# ${inertMarkdown(scope.project_id)} memory`,
    "Synced from the kb memory service by `kb transfer vault-sync`. Edits here are replaced on the next sync; to correct memory, submit a candidate.",
  ];
  if (completeness !== "complete") lines.push(`> [!warning] Incomplete\n> Some records were withheld or unavailable during this sync (${completeness}).`);
  for (const section of SECTIONS) {
    const entries = pages.filter((p) => section.statuses.includes(p.status)).sort((a, b) => a.title.localeCompare(b.title, "en") || (a.path < b.path ? -1 : 1));
    if (entries.length === 0) continue;
    lines.push(`## ${section.heading}\n\n` + entries.map((p) => `- [[${p.path.slice(0, -3)}|${p.title}]] · ${inertMarkdown(p.kind)}`).join("\n"));
  }
  if (pages.length === 0) lines.push("No authorized claims in this scope yet.");
  return [{ path: INDEX_PAGE, content: lines.join("\n\n") + "\n" }, ...pages.map(({ path, content }) => ({ path, content }))];
}
