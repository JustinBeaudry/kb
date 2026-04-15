// src/lib/templates.ts
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const TEMPLATES_DIR = join(__dirname, "..", "..", "templates");

export function getCairnMdTemplate(): string {
  return readFileSync(join(TEMPLATES_DIR, "CAIRN.md"), "utf-8");
}

export const INDEX_MD_STUB = `# Vault Index

Pages are listed newest first. Each entry is ~150 characters.

<!-- Cairn will add entries here as you ingest sources and build knowledge. -->
`;

export const LOG_MD_STUB = `# Vault Log

Chronological record of vault operations.

<!-- Cairn will append entries here automatically. -->
`;

export const CONTEXT_MD_STUB = `# Working Set

Current focus areas for context injection. Updated by the agent when focus shifts.

## Active
<!-- Pages and topics currently being worked on -->

## Background
<!-- Reference material relevant to active work -->
`;
